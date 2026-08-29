# PR #33 Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every finding from the `/code-review ultra 33` report on `feat(cli): implement snapback list` (PR #33) that isn't already covered by a tracked issue.

**Architecture:** Each finding is fixed in place with a matching unit test, in small commits on top of the existing `feat/list-command` branch (PR #33 is already open against it). No new packages are introduced; one new file (`internal/cli/config_load.go`) holds a helper shared between `run` and `list`, and two small new test-only files hold shared test scaffolding.

**Tech Stack:** Go 1.26 (`go.mod` says `go 1.26.5`), standard `testing` package, `github.com/spf13/cobra`.

**Spec:** `/code-review ultra 33` findings (reproduced below; no separate spec doc exists for this fix set). Two findings from that report are deliberately **excluded** because they're already tracked elsewhere and would be redundant to fix here:
- The unexpanded-`~`-in-destination finding is the exact subject of issue #15 ("Expand ~ in config paths").
- The duplicated `"destination is required"` string (between `backup.Run` and `backup.ListArchives`) is a symptom of there being no config-field validation yet, which is issue #16's scope ("Validate Config fields on load... required fields"); once #16 lands this duplication is superseded, not worth a one-off dedup now.

```
1. internal/cli/list.go (formatSize) — divides before rounding, so a size
   just under a 1024^n boundary displays as e.g. "1024.0 MiB" instead of
   "1.0 GiB".
2. internal/backup/list.go (ListArchives) — any manifest.json read/parse
   error other than ErrNotExist aborts the whole call, hiding every other
   valid archive, even though the doc comment says the goal is to skip
   just the bad one. writeManifest isn't atomic, so this can be hit for
   real if `list` races a concurrent, still-running `snapback run`.
3. internal/backup/list.go (ListArchives) — entry.IsDir() doesn't follow
   symlinks, so a symlinked archive directory is silently skipped.
4. internal/backup/list.go (ListArchives) — sort.Slice's comparator has no
   tiebreaker for equal Manifest.Timestamp values (one-second resolution),
   so same-second archives can print in a different relative order between
   runs.
5. internal/cli/list.go (runList) — Manifest.Comment is written straight
   into a tab-separated tabwriter row with no sanitization, so an embedded
   tab or newline breaks column alignment.
6. internal/cli/root.go — unlike `run` (newRunCmd() wraps
   newRunCmdWithDeps()), `list` has no no-arg newListCmd() wrapper;
   root.go constructs listDeps inline instead, breaking the established
   convention.
7. internal/cli/run.go / internal/cli/list.go — both hand-roll "read
   --config flag, call loadConfig, wrap the error as `load config: %w`"
   instead of sharing one helper.
8. internal/cli/list_internal_test.go — newTestRootForList near-duplicates
   the newTestRoot helper already in run_internal_test.go for swapping a
   real subcommand for a fake-deps one.
```

## Global Constraints

- Go 1.26 toolchain; build/test with the `go` on `PATH`.
- `make lint` currently fails in this environment with a `golangci-lint`/Go-toolchain version mismatch (`golangci-lint` built with go1.26.0, local toolchain go1.27.0) — confirmed to reproduce identically on `main`, unrelated to this work. Use `go build ./...`, `go vet ./...`, and `gofmt -l .` as the verification gate for each task instead of `make lint`.
- Test files in `internal/cli` and `internal/backup` are `package cli`/`package backup` (white-box, internal test files named `*_internal_test.go`), not the black-box `package cli_test` style `root_test.go` uses — any new test of an unexported function must go in an internal test file.
- Preserve all currently-passing behavior — no task in this plan should regress an existing test. In particular, `TestListArchives_MalformedManifestReturnsError` currently asserts malformed manifests are an *error*; Task 1 deliberately changes that test's expectation as part of the fix (spelled out in that task), which is the one intentional behavior-changing exception.
- Commit messages follow this repo's convention: real Conventional Commit type (`fix`/`test`/`refactor`), package name as scope.

---

## File Structure

- Modify `internal/backup/list.go` — skip (not abort on) unreadable/malformed manifests; follow symlinked archive directories; break sort ties deterministically.
- Modify `internal/backup/list_test.go` — update the malformed-manifest test's expectation, add an unreadable-manifest test, a symlink test, and a sort-tiebreak test.
- Modify `internal/cli/list.go` — fix `formatSize`'s boundary rounding; sanitize `Comment` before printing; add a `newListCmd()` no-arg wrapper; use the new shared config-loading helper.
- Modify `internal/cli/list_internal_test.go` — add direct `formatSize` tests, a comment-sanitization test; switch `newTestRootForList` to the new shared swap helper.
- Modify `internal/cli/run.go` — use the new shared config-loading helper.
- Modify `internal/cli/run_internal_test.go` — switch `newTestRoot` to the new shared swap helper.
- Modify `internal/cli/root.go` — use `newListCmd()` instead of constructing `listDeps` inline; drop the now-unused `backup`/`config` imports.
- Create `internal/cli/config_load.go` — `loadConfigForCmd`, the shared config-loading helper.
- Create `internal/cli/config_load_internal_test.go` — tests for `loadConfigForCmd`.
- Create `internal/cli/testcmd_internal_test.go` — `swapSubcommand`, the shared test helper for swapping a real subcommand for a fake-deps one.

## Task 1: `backup.ListArchives` — skip unreadable/malformed manifests instead of aborting

**Files:**
- Modify: `internal/backup/list.go` (the `for _, entry := range entries` loop inside `ListArchives`)
- Test: `internal/backup/list_test.go`

**Interfaces:**
- Produces: no signature change to `ListArchives(destination string) ([]Archive, error)`. Behavior change only: a manifest.json that fails to read (for any reason, not just `ErrNotExist`) or fails to `json.Unmarshal` is now skipped like a missing one, instead of returning an error for the whole call.

- [ ] **Step 1: Update the existing malformed-manifest test to the new expectation, and add an unreadable-manifest test**

In `internal/backup/list_test.go`, replace `TestListArchives_MalformedManifestReturnsError`:

```go
func TestListArchives_MalformedManifestReturnsError(t *testing.T) {
	destination := t.TempDir()
	dir := filepath.Join(destination, "myvm-20260101T000000Z")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if _, err := ListArchives(destination); err == nil {
		t.Fatal("ListArchives() error = nil, want error for malformed manifest.json")
	}
}
```

with:

```go
func TestListArchives_SkipsMalformedManifest_ReturnsOtherArchives(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	badDir := filepath.Join(destination, "myvm-20260202T000000Z")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil (malformed manifest skipped, not fatal)", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (only the well-formed archive)", len(archives))
	}
	if archives[0].ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("archives[0].ArchiveID = %q, want %q", archives[0].ArchiveID, "myvm-20260101T000000Z")
	}
}

func TestListArchives_SkipsUnreadableManifest_ReturnsOtherArchives(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	// A manifest.json that's actually a directory can never be read as a
	// file -- this simulates a non-ErrNotExist os.ReadFile failure without
	// relying on platform-specific permission bits.
	badManifestPath := filepath.Join(destination, "myvm-20260202T000000Z", "manifest.json")
	if err := os.MkdirAll(badManifestPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil (unreadable manifest skipped, not fatal)", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (only the well-formed archive)", len(archives))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run 'TestListArchives_SkipsMalformedManifest_ReturnsOtherArchives|TestListArchives_SkipsUnreadableManifest_ReturnsOtherArchives' -v`
Expected: both FAIL — `ListArchives() error = ...` is non-nil, since the current implementation still returns an error for both cases.

- [ ] **Step 3: Change `ListArchives` to skip instead of abort**

In `internal/backup/list.go`, change:

```go
		manifestPath := filepath.Join(destination, entry.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse manifest %s: %w", manifestPath, err)
		}
```

to:

```go
		manifestPath := filepath.Join(destination, entry.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			// Missing (a run that died before Checksumming), truncated (raced
			// a concurrent in-flight run's non-atomic writeManifest), or
			// otherwise unreadable -- every case is "this one archive isn't
			// ready to be listed yet", not a reason to hide every other
			// archive by aborting the whole scan.
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
```

Also update the function's doc comment (directly above `func ListArchives`) — replace the sentence starting "A directory with no manifest.json is skipped..." with:

```go
// A directory whose manifest.json is missing, unreadable, or malformed is
// skipped rather than aborting the whole scan -- writeManifest
// (manifest.go) isn't atomic, so a manifest can legitimately be mid-write
// if ListArchives races a concurrent, still-running `snapback run`;
// treating that as "this one archive isn't ready yet" keeps every other,
// already-complete archive visible instead of hiding all of them behind
// one in-progress or corrupt entry.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run 'TestListArchives_SkipsMalformedManifest_ReturnsOtherArchives|TestListArchives_SkipsUnreadableManifest_ReturnsOtherArchives' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/...`
Expected: PASS — including `TestListArchives_ReturnsManifestsNewestFirst`, `TestListArchives_SkipsDirectoriesWithoutManifest`, `TestListArchives_IgnoresNonDirectoryEntries`, `TestListArchives_MissingDestinationReturnsEmptyNotError`, `TestListArchives_EmptyDestinationReturnsError`, `TestListArchives_PreservesManifestFields`.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/list.go internal/backup/list_test.go
git commit -m "fix(backup): skip archives with unreadable or malformed manifests instead of aborting"
```

## Task 2: `backup.ListArchives` — deterministic sort order for same-second archives

**Files:**
- Modify: `internal/backup/list.go` (the `sort.Slice` call at the end of `ListArchives`)
- Test: `internal/backup/list_test.go`

**Interfaces:**
- Produces: no signature change. Two archives with equal `Manifest.Timestamp` now sort by `ArchiveID` ascending as a tiebreaker, instead of in whatever order `sort.Slice` (not stable) happens to leave them in.

- [ ] **Step 1: Write the failing test**

Add to `internal/backup/list_test.go`, after `TestListArchives_ReturnsManifestsNewestFirst`:

```go
func TestListArchives_TiesBreakOnArchiveID(t *testing.T) {
	destination := t.TempDir()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeTestManifest(t, destination, "zeta-20260101T000000Z", Manifest{VMName: "zeta", Timestamp: ts})
	writeTestManifest(t, destination, "alpha-20260101T000000Z", Manifest{VMName: "alpha", Timestamp: ts})

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 2 {
		t.Fatalf("len(archives) = %d, want 2", len(archives))
	}
	if archives[0].ArchiveID != "alpha-20260101T000000Z" || archives[1].ArchiveID != "zeta-20260101T000000Z" {
		t.Errorf("archives = [%s, %s], want alpha before zeta for equal timestamps", archives[0].ArchiveID, archives[1].ArchiveID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup/... -run TestListArchives_TiesBreakOnArchiveID -v -count=20`
Expected: FAILs at least once across repeated runs — `sort.Slice`'s order for equal keys isn't guaranteed, so this may pass by chance on any single run; `-count=20` makes the non-determinism visible.

- [ ] **Step 3: Add the tiebreaker**

In `internal/backup/list.go`, change:

```go
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].Manifest.Timestamp.After(archives[j].Manifest.Timestamp)
	})
```

to:

```go
	sort.Slice(archives, func(i, j int) bool {
		ti, tj := archives[i].Manifest.Timestamp, archives[j].Manifest.Timestamp
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return archives[i].ArchiveID < archives[j].ArchiveID
	})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backup/... -run TestListArchives_TiesBreakOnArchiveID -v -count=20`
Expected: PASS on every repetition.

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/backup/list.go internal/backup/list_test.go
git commit -m "fix(backup): break archive sort ties deterministically by archive ID"
```

## Task 3: `backup.ListArchives` — follow symlinked archive directories

**Files:**
- Modify: `internal/backup/list.go` (the `if !entry.IsDir()` check inside `ListArchives`)
- Test: `internal/backup/list_test.go`

**Interfaces:**
- Produces: new unexported helper `isArchiveDir(destination string, entry os.DirEntry) bool` in `internal/backup/list.go`, used only by `ListArchives`.

- [ ] **Step 1: Write the failing test**

Add to `internal/backup/list_test.go`:

```go
func TestListArchives_FollowsSymlinkedArchiveDirectory(t *testing.T) {
	destination := t.TempDir()
	realParent := t.TempDir()
	writeTestManifest(t, realParent, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	linkPath := filepath.Join(destination, "myvm-20260101T000000Z")
	if err := os.Symlink(filepath.Join(realParent, "myvm-20260101T000000Z"), linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (symlinked archive directory followed)", len(archives))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup/... -run TestListArchives_FollowsSymlinkedArchiveDirectory -v`
Expected: FAIL — `len(archives) = 0`, since `entry.IsDir()` is `false` for the symlink itself.

- [ ] **Step 3: Add `isArchiveDir` and use it**

In `internal/backup/list.go`, add `"io/fs"` to the import block:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)
```

Change:

```go
	var archives []Archive
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
```

to:

```go
	var archives []Archive
	for _, entry := range entries {
		if !isArchiveDir(destination, entry) {
			continue
		}
```

Add this function after `ListArchives`:

```go
// isArchiveDir reports whether entry (a child of destination) is a
// directory, following a symlink if entry itself is one -- a symlinked
// archive directory (e.g. one relocated onto slower storage) should list
// like any other.
func isArchiveDir(destination string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(destination, entry.Name()))
	return err == nil && info.IsDir()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backup/... -run TestListArchives_FollowsSymlinkedArchiveDirectory -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/backup/list.go internal/backup/list_test.go
git commit -m "fix(backup): follow symlinked archive directories when listing"
```

## Task 4: `formatSize` — fix boundary rounding

**Files:**
- Modify: `internal/cli/list.go` (the `formatSize` function)
- Test: `internal/cli/list_internal_test.go`

**Interfaces:**
- Produces: no signature change to `formatSize(n int64) string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/list_internal_test.go`:

```go
func TestFormatSize(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero", 0, "0 B"},
		{"under a KiB", 512, "512 B"},
		{"exactly a KiB", 1024, "1.0 KiB"},
		{"a couple KiB", 2048, "2.0 KiB"},
		{"just under a GiB boundary", 1073741823, "1.0 GiB"},
		{"just under a MiB boundary", 1048575, "1.0 MiB"},
		{"exactly a GiB", 1 << 30, "1.0 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSize(tt.bytes); got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestFormatSize -v`
Expected: FAIL on `"just under a GiB boundary"` (`formatSize(1073741823) = "1024.0 MiB"`) and `"just under a MiB boundary"` (`formatSize(1048575) = "1024.0 KiB"`).

- [ ] **Step 3: Fix `formatSize`**

In `internal/cli/list.go`, add `"math"` to the import block:

```go
import (
	"fmt"
	"math"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
)
```

Replace `formatSize`:

```go
// formatSize renders n bytes as a human-readable IEC size (KiB/MiB/...),
// matching the units Finder/du -h use on macOS rather than SI (KB/MB).
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
```

with:

```go
// formatSize renders n bytes as a human-readable IEC size (KiB/MiB/...),
// matching the units Finder/du -h use on macOS rather than SI (KB/MB).
func formatSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024.0
	const units = "KMGTPE"
	value := float64(n)
	exp := -1
	for value >= unit && exp < len(units)-1 {
		value /= unit
		exp++
	}
	// %.1f rounds to one decimal place, which can round a value just under
	// unit (e.g. 1023.95) up to "1024.0" -- display that as 1.0 of the next
	// unit instead of 1024.0 of this one.
	if rounded := math.Round(value*10) / 10; rounded >= unit && exp < len(units)-1 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, units[exp])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/... -run TestFormatSize -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS — in particular `TestListCmd_PrintsArchiveTable` (asserts `"2.0 KiB"` for 2048 bytes) still passes.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/list.go internal/cli/list_internal_test.go
git commit -m "fix(cli): round display value before choosing the size unit in formatSize"
```

## Task 5: `runList` — sanitize `Comment` before printing

**Files:**
- Modify: `internal/cli/list.go` (`runList`, plus a new `sanitizeForTable` helper)
- Test: `internal/cli/list_internal_test.go`

**Interfaces:**
- Produces: new unexported helper `sanitizeForTable(s string) string` in `internal/cli/list.go`.

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/list_internal_test.go`:

```go
func TestListCmd_SanitizesCommentForTable(t *testing.T) {
	root := newTestRootForList(t, listDeps{
		loadConfig: func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		listArchives: func(string) ([]backup.Archive, error) {
			return []backup.Archive{
				{ArchiveID: "myvm-1", Manifest: backup.Manifest{VMName: "myvm", Comment: "line1\tline2\nline3"}},
			}, nil
		},
	})
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout had %d lines, want 2 (header + one archive row) -- an unsanitized embedded newline in Comment would split it into a third line: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[1], "line1 line2 line3") {
		t.Errorf("row = %q, want Comment's embedded tab/newline replaced with spaces (\"line1 line2 line3\")", lines[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestListCmd_SanitizesCommentForTable -v`
Expected: FAIL — `stdout had 3 lines`, since the embedded `\n` in `Comment` currently passes straight through to the tabwriter, and `tabwriter` reads it as an additional row.

- [ ] **Step 3: Add `sanitizeForTable` and use it**

In `internal/cli/list.go`, add `"strings"` to the import block:

```go
import (
	"fmt"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"

	"github.com/spf13/cobra"
)
```

Change:

```go
	for _, a := range archives {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.ArchiveID,
			a.Manifest.VMName,
			a.Manifest.Timestamp.Local().Format(time.RFC3339),
			formatSize(a.Manifest.SizeBytes),
			a.Manifest.Comment,
		)
```

to:

```go
	for _, a := range archives {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.ArchiveID,
			a.Manifest.VMName,
			a.Manifest.Timestamp.Local().Format(time.RFC3339),
			formatSize(a.Manifest.SizeBytes),
			sanitizeForTable(a.Manifest.Comment),
		)
```

Add this function next to `formatSize`:

```go
// sanitizeForTable strips characters that would confuse tabwriter's
// column (tab) and row (newline) delimiters out of free-form text -- e.g.
// Manifest.Comment, which comes from a user-configured comment_template
// and isn't otherwise constrained.
func sanitizeForTable(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/... -run TestListCmd_SanitizesCommentForTable -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/cli/list.go internal/cli/list_internal_test.go
git commit -m "fix(cli): sanitize free-form comment text before printing the archive table"
```

## Task 6: `list.go`/`root.go` — add `newListCmd()` for convention parity with `run`

**Files:**
- Modify: `internal/cli/list.go` (add `newListCmd()`)
- Modify: `internal/cli/root.go` (use it, drop now-unused imports)

**Interfaces:**
- Produces: `newListCmd() *cobra.Command` in `internal/cli/list.go`, mirroring `newRunCmd()` in `run.go` — no-arg, builds the real `listDeps` (`config.Load`, `backup.ListArchives`) and delegates to `newListCmdWithDeps`.

- [ ] **Step 1: Add `newListCmd()`**

In `internal/cli/list.go`, add this function directly above `newListCmdWithDeps`:

```go
func newListCmd() *cobra.Command {
	return newListCmdWithDeps(listDeps{
		loadConfig:   config.Load,
		listArchives: backup.ListArchives,
	})
}

```

- [ ] **Step 2: Wire it up in `root.go` and drop the now-unused imports**

In `internal/cli/root.go`, change:

```go
import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
)
```

to:

```go
import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)
```

and change:

```go
	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmdWithDeps(listDeps{
			loadConfig:   config.Load,
			listArchives: backup.ListArchives,
		}),
		newStatusCmd(),
	)
```

to:

```go
	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
	)
```

- [ ] **Step 3: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: builds clean (no unused-import errors), all tests PASS.

- [ ] **Step 4: Manually verify the real wiring still works**

```bash
go build -o /tmp/snapback-verify ./cmd/snapback
DEST=$(mktemp -d)
CFG=$(mktemp)
printf 'destination: %s\n' "$DEST" >| "$CFG"
/tmp/snapback-verify --config "$CFG" list
```

Expected: `no backup archives found` (empty but configured destination), proving `newListCmd()` reaches the real `config.Load`/`backup.ListArchives` exactly as the inline construction did before.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/list.go internal/cli/root.go
git commit -m "refactor(cli): add newListCmd wrapper for parity with run's no-arg constructor"
```

## Task 7: share config-loading boilerplate between `run` and `list`

**Files:**
- Create: `internal/cli/config_load.go`
- Test: `internal/cli/config_load_internal_test.go` (new file)
- Modify: `internal/cli/run.go` (`runVM`)
- Modify: `internal/cli/list.go` (`runList`)

**Interfaces:**
- Produces: `loadConfigForCmd(cmd *cobra.Command, loadConfig func(path string) (*config.Config, error)) (cfg *config.Config, configPath string, err error)` in `internal/cli/config_load.go`.
- Consumes (in `run.go`/`list.go`): `deps.loadConfig` (existing `runDeps`/`listDeps` field, unchanged).

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/config_load_internal_test.go`:

```go
// Package cli (internal test package) so this file can call
// loadConfigForCmd directly.
package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

func TestLoadConfigForCmd_WrapsLoaderError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "unused.yaml", "")

	_, _, err := loadConfigForCmd(cmd, func(string) (*config.Config, error) { return nil, errBoom })
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("loadConfigForCmd() error = %v, want it to wrap errBoom", err)
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("loadConfigForCmd() error = %v, want \"load config\" context", err)
	}
}

func TestLoadConfigForCmd_ReturnsConfigAndPath(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "/some/path.yaml", "")

	want := &config.Config{Destination: "/dest"}
	cfg, path, err := loadConfigForCmd(cmd, func(p string) (*config.Config, error) {
		if p != "/some/path.yaml" {
			t.Errorf("loadConfig called with %q, want %q", p, "/some/path.yaml")
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("loadConfigForCmd() error = %v, want nil", err)
	}
	if cfg != want {
		t.Errorf("loadConfigForCmd() cfg = %v, want %v", cfg, want)
	}
	if path != "/some/path.yaml" {
		t.Errorf("loadConfigForCmd() path = %q, want %q", path, "/some/path.yaml")
	}
}
```

(`errBoom` is the shared `errors.New("boom")` already declared in `run_internal_test.go` — same package, same test binary.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/... -run TestLoadConfigForCmd -v`
Expected: FAIL with `undefined: loadConfigForCmd`.

- [ ] **Step 3: Implement `loadConfigForCmd`**

Create `internal/cli/config_load.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

// loadConfigForCmd reads the --config flag from cmd and calls loadConfig
// with it, wrapping any load error with enough context to identify that
// it came from config loading. Shared by every subcommand that needs the
// user's config.yaml (run, list, and eventually status/init), so a
// wording change (e.g. the friendlier missing-file message tracked in
// #32) only needs to be made once.
func loadConfigForCmd(cmd *cobra.Command, loadConfig func(path string) (*config.Config, error)) (cfg *config.Config, configPath string, err error) {
	configPath, err = cmd.Flags().GetString("config")
	if err != nil {
		return nil, "", err
	}

	cfg, err = loadConfig(configPath)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return cfg, configPath, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run TestLoadConfigForCmd -v`
Expected: PASS

- [ ] **Step 5: Use it in `runVM`**

In `internal/cli/run.go`, change:

```go
func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
```

to:

```go
func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
```

- [ ] **Step 6: Use it in `runList`**

In `internal/cli/list.go`, change:

```go
func runList(cmd *cobra.Command, deps listDeps) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	archives, err := deps.listArchives(cfg.Destination)
```

to:

```go
func runList(cmd *cobra.Command, deps listDeps) error {
	cfg, _, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	archives, err := deps.listArchives(cfg.Destination)
```

- [ ] **Step 7: Run the full test suite to check for regressions**

Run: `go build ./... && go test ./...`
Expected: PASS — in particular `TestRunCmd_ConfigLoadError_DoesNotDuplicatePath` (path appears exactly once) and `TestListCmd_ConfigLoadError_IsWrapped` (message contains `"load config"` and the wrapped error) both still pass, since the wrap text (`"load config: %w"`) is unchanged, just relocated.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/config_load.go internal/cli/config_load_internal_test.go internal/cli/run.go internal/cli/list.go
git commit -m "refactor(cli): share config-loading boilerplate between run and list"
```

## Task 8: share the subcommand-swap test helper between `run` and `list` tests

**Files:**
- Create: `internal/cli/testcmd_internal_test.go`
- Modify: `internal/cli/run_internal_test.go` (`newTestRoot`)
- Modify: `internal/cli/list_internal_test.go` (`newTestRootForList`)

**Interfaces:**
- Produces: `swapSubcommand(t *testing.T, name string, replacement *cobra.Command) *cobra.Command` in `internal/cli/testcmd_internal_test.go`.
- Consumes (in both test files): unchanged — `newTestRoot(t *testing.T, deps runDeps) *cobra.Command` and `newTestRootForList(t *testing.T, deps listDeps) *cobra.Command` keep their existing signatures and behavior, now implemented in terms of `swapSubcommand`.

- [ ] **Step 1: Create the shared helper**

Create `internal/cli/testcmd_internal_test.go`:

```go
// Package cli (internal test package) so this file can call NewRootCmd
// and manipulate its subcommands directly.
package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// swapSubcommand builds the real root command via NewRootCmd() -- so the
// --config persistent flag and everything else the real command wiring
// depends on can't drift from root.go -- then replaces the subcommand
// named name with replacement (typically built from a *WithDeps
// constructor so a test can inject fakes).
func swapSubcommand(t *testing.T, name string, replacement *cobra.Command) *cobra.Command {
	t.Helper()
	root := NewRootCmd()
	removed := false
	for _, sub := range root.Commands() {
		if sub.Name() == name {
			root.RemoveCommand(sub)
			removed = true
			break
		}
	}
	if !removed {
		t.Fatalf("swapSubcommand: NewRootCmd() has no %q subcommand to replace", name)
	}
	root.AddCommand(replacement)
	return root
}
```

- [ ] **Step 2: Switch `newTestRoot` (run) to use it**

In `internal/cli/run_internal_test.go`, replace:

```go
// newTestRoot builds the real root command via NewRootCmd() -- so the
// --config persistent flag and everything else run.go's runVM depends on
// can't drift from root.go -- then swaps in a run subcommand wired to a
// fake deps.
func newTestRoot(t *testing.T, deps runDeps) *cobra.Command {
	t.Helper()
	root := NewRootCmd()
	removed := false
	for _, sub := range root.Commands() {
		if sub.Name() == "run" {
			root.RemoveCommand(sub)
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("newTestRoot: NewRootCmd() has no \"run\" subcommand to replace")
	}
	root.AddCommand(newRunCmdWithDeps(deps))
	return root
}
```

with:

```go
// newTestRoot builds the real root command with a run subcommand wired to
// a fake deps -- see swapSubcommand for why it's built on NewRootCmd().
func newTestRoot(t *testing.T, deps runDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "run", newRunCmdWithDeps(deps))
}
```

- [ ] **Step 3: Switch `newTestRootForList` (list) to use it**

In `internal/cli/list_internal_test.go`, replace:

```go
// newTestRootForList builds the real root command via NewRootCmd() -- so
// the --config persistent flag can't drift from root.go -- then swaps in
// a list subcommand wired to fake deps.
func newTestRootForList(t *testing.T, deps listDeps) *cobra.Command {
	t.Helper()
	root := NewRootCmd()
	removed := false
	for _, sub := range root.Commands() {
		if sub.Name() == "list" {
			root.RemoveCommand(sub)
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("newTestRootForList: NewRootCmd() has no \"list\" subcommand to replace")
	}
	root.AddCommand(newListCmdWithDeps(deps))
	return root
}
```

with:

```go
// newTestRootForList builds the real root command with a list subcommand
// wired to a fake deps -- see swapSubcommand for why it's built on
// NewRootCmd().
func newTestRootForList(t *testing.T, deps listDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "list", newListCmdWithDeps(deps))
}
```

- [ ] **Step 4: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/... -v`
Expected: PASS — every existing `TestRunCmd_*` and `TestListCmd_*` test still passes unchanged, since `newTestRoot`/`newTestRootForList`'s external behavior is preserved.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/testcmd_internal_test.go internal/cli/run_internal_test.go internal/cli/list_internal_test.go
git commit -m "test(cli): share the subcommand-swap test helper between run and list tests"
```

---

## Final Verification

- [ ] **Run the full suite one more time end to end**

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./...
```

Expected: all green, `gofmt -l .` prints nothing. At this point every non-tracked `/code-review ultra 33` finding is fixed:
1. `formatSize` boundary rounding (Task 4).
2. `ListArchives` no longer aborts the whole listing on one bad manifest (Task 1).
3. Symlinked archive directories are followed (Task 3).
4. Same-second archives sort deterministically (Task 2).
5. `Comment` is sanitized before hitting the tabwriter (Task 5).
6. `newListCmd()` restores convention parity with `run` (Task 6).
7. Config-loading boilerplate is shared, not duplicated (Task 7).
8. The subcommand-swap test helper is shared, not duplicated (Task 8).

- [ ] **Push the fix commits to the existing PR branch**

```bash
git push origin feat/list-command
```

Expected: PR #33 picks up the eight new commits automatically (same branch, no new PR needed).
