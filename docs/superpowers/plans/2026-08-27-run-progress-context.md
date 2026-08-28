# Run() Progress/Context Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gap issue #21 identified — wire `internal/backup.Run` to a `progress.Reporter` and `context.Context` per ADR-002, exactly as designed in ADR-003.

**Architecture:** A new, dependency-free `internal/progress` package defines the event vocabulary. `internal/backup` gains a typed `RunError`, a `dirSize` helper, and optional progress callbacks on `copyDir`/`createArchive` — each a small, independently testable addition. The final task rewires `Run()`'s signature and body to compose all of them, plus updates every existing test call site in the same commit (the signature change is not backward compatible, so this last step is necessarily atomic).

**Tech Stack:** Go 1.26.5, standard library only (no new dependencies — this ADR explicitly excludes the Charm-stack renderer work). `testing.T.Context()` (Go 1.24+) for test contexts.

**Spec:** `docs/superpowers/specs/2026-08-27-run-progress-context-design.md` (ADR-003), which itself extends `docs/superpowers/specs/2026-08-23-cli-ux-design.md` (ADR-002). Executors should read ADR-003 for the full rationale; this plan implements it verbatim.

## Global Constraints

- Go 1.26.5, module `github.com/xortim/snapback`. No new dependencies.
- `internal/progress` imports nothing beyond the standard library; `internal/backup` depends on `internal/progress`, never the reverse.
- `vm.Controller`'s method signatures do NOT change (explicitly out of scope per ADR-003) — cancellation is coarse-grained, checked between stages only.
- Every error `Run()` returns must be a `*backup.RunError` (via `errors.As`), carrying the `progress.Stage` active when the failure occurred.
- `Run()` emits events for exactly 6 stages: `CheckingTools`, `Snapshotting`, `Copying`, `Merging`, `Compressing`, `Checksumming`, plus a final `Done` — never `Pruning`/`Notifying` (those belong to a future caller wrapping `Run()`).
- Match existing test conventions in `internal/backup/*_test.go` and `internal/vm/*_test.go`: `t.TempDir()` for scratch paths, `t.Fatalf` for setup/precondition failures, `t.Errorf` for assertion failures, package `backup_test` for tests exercising the public API (`choreography_test.go`) vs. plain `package backup` for tests of unexported helpers (`copy_test.go`, `archive_test.go`).
- `make test`/`go test ./...` and `go vet ./...` must stay green after every task. (`make lint` may be unrunnable in some environments due to a known golangci-lint/Go-toolchain export-data mismatch unrelated to this work — use `go vet`/`gofmt -l` as the available proxy if so.)

---

## File Structure

- `internal/progress/progress.go` — **new**. `Stage`, `Event`, `Reporter`, `NoOpReporter`.
- `internal/progress/progress_test.go` — **new**.
- `internal/backup/run_error.go` — **new**. `RunError`.
- `internal/backup/run_error_test.go` — **new**.
- `internal/backup/size.go` — **new**. `dirSize`.
- `internal/backup/size_test.go` — **new**.
- `internal/backup/copy.go` — **modify**. `copyDir` gains an `onCopy` callback parameter.
- `internal/backup/copy_test.go` — **modify**. Existing calls updated; new callback test added.
- `internal/backup/archive.go` — **modify**. `createArchive`/`tarToZstd`/`tarTo` gain an `onRead` callback parameter; new `countingReader` type.
- `internal/backup/archive_test.go` — **modify**. Existing calls updated; new callback test added.
- `internal/backup/choreography.go` — **modify**. `Run()`'s signature and body rewritten.
- `internal/backup/choreography_test.go` — **modify**. All 16 existing `backup.Run(...)` call sites updated; new tests added (`fakeReporter`, stage-order, cancellation, `RunError.Stage` orphan-detection).

Tasks 1-5 are independent, additive, and each leaves the package compiling and green on its own. Task 6 depends on all of them and touches `choreography.go`/`choreography_test.go` as one atomic unit — `Run()`'s signature changes, so every caller (today, only its own tests) must update in the same commit or the package won't build.

---

### Task 1: `internal/progress` package

**Files:**
- Create: `internal/progress/progress.go`
- Test: `internal/progress/progress_test.go`

**Interfaces:**
- Produces: `progress.Stage` (int enum: `CheckingTools`, `Snapshotting`, `Copying`, `Merging`, `Compressing`, `Checksumming`, `Pruning`, `Notifying`, `Done`), `progress.Event{Stage, Message, Percent, Err}`, `progress.Reporter` interface (`Report(Event)`), `progress.NoOpReporter{}` (implements `Reporter`, discards everything). All consumed by every later task in this plan.

- [ ] **Step 1: Write the failing test**

Create `internal/progress/progress_test.go`:

```go
package progress_test

import (
	"testing"

	"github.com/xortim/snapback/internal/progress"
)

func TestNoOpReporter_ReportDoesNotPanic(t *testing.T) {
	var r progress.Reporter = progress.NoOpReporter{}
	r.Report(progress.Event{Stage: progress.Snapshotting, Message: "test", Percent: 0.5})
}

func TestStages_AreDistinctValues(t *testing.T) {
	stages := []progress.Stage{
		progress.CheckingTools,
		progress.Snapshotting,
		progress.Copying,
		progress.Merging,
		progress.Compressing,
		progress.Checksumming,
		progress.Pruning,
		progress.Notifying,
		progress.Done,
	}
	seen := map[progress.Stage]bool{}
	for _, s := range stages {
		if seen[s] {
			t.Errorf("stage %v appears more than once in the const block", s)
		}
		seen[s] = true
	}
	if len(seen) != 9 {
		t.Errorf("got %d distinct stages, want 9", len(seen))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/progress/... -v`
Expected: build failure — the `internal/progress` package does not exist yet. This is the expected RED for a brand-new package: there is nothing to run until Step 3 creates it.

- [ ] **Step 3: Create the package**

Create `internal/progress/progress.go`:

```go
// Package progress defines the event vocabulary backup choreography and
// its renderers share, so choreography code never imports a rendering
// library directly (see docs/superpowers/specs/2026-08-23-cli-ux-design.md).
package progress

// Stage identifies which step of a choreography pipeline an Event
// describes.
type Stage int

const (
	CheckingTools Stage = iota
	Snapshotting
	Copying
	Merging
	Compressing
	Checksumming
	Pruning
	Notifying
	Done
)

// Event is one progress update emitted by a choreography pipeline.
type Event struct {
	Stage   Stage
	Message string
	Percent float64 // set for Copying/Compressing, where a byte count is known
	Err     error
}

// Reporter receives progress Events. Implementations render them however
// they like (a TUI, a plain log line, or nothing at all); choreography
// code depends only on this interface, never on a specific renderer.
type Reporter interface {
	Report(Event)
}

// NoOpReporter discards every Event. Use it wherever progress reporting
// isn't needed -- e.g. tests that assert on a Result or error, not on
// emitted events.
type NoOpReporter struct{}

// Report implements Reporter by doing nothing.
func (NoOpReporter) Report(Event) {}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/progress/... -v`
Expected: PASS (2/2 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/progress/progress.go internal/progress/progress_test.go
git commit -m "progress: add Stage/Event/Reporter vocabulary per ADR-003"
```

---

### Task 2: `RunError`

**Files:**
- Create: `internal/backup/run_error.go`
- Test: `internal/backup/run_error_test.go`

**Interfaces:**
- Consumes: `progress.Stage` from Task 1.
- Produces: `backup.RunError{Stage, Err}` with `Error() string` and `Unwrap() error`, satisfying the standard `error` interface and `errors.As`/`errors.Is`. Consumed by Task 6.

- [ ] **Step 1: Write the failing test**

Create `internal/backup/run_error_test.go`:

```go
package backup_test

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestRunError_ErrorReturnsUnderlyingMessage(t *testing.T) {
	underlying := errors.New("snapshot failed")
	err := &backup.RunError{Stage: progress.Snapshotting, Err: underlying}

	if err.Error() != "snapshot failed" {
		t.Errorf("Error() = %q, want %q", err.Error(), "snapshot failed")
	}
}

func TestRunError_UnwrapAndErrorsAs(t *testing.T) {
	underlying := errors.New("boom")
	wrapped := error(&backup.RunError{Stage: progress.Copying, Err: underlying})

	if !errors.Is(wrapped, underlying) {
		t.Error("errors.Is(wrapped, underlying) = false, want true")
	}

	var runErr *backup.RunError
	if !errors.As(wrapped, &runErr) {
		t.Fatal("errors.As(wrapped, &runErr) = false, want true")
	}
	if runErr.Stage != progress.Copying {
		t.Errorf("runErr.Stage = %v, want %v", runErr.Stage, progress.Copying)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestRunError -v`
Expected: build failure — `backup.RunError` does not exist yet.

- [ ] **Step 3: Create `RunError`**

Create `internal/backup/run_error.go`:

```go
package backup

import "github.com/xortim/snapback/internal/progress"

// RunError wraps an error returned by Run with the Stage active when the
// failure occurred, so a caller can tell whether a snapshot may have been
// left behind on the source VM (Stage >= progress.Snapshotting) --
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback in Run itself.
type RunError struct {
	Stage progress.Stage
	Err   error
}

// Error implements the error interface, returning the underlying error's
// message unchanged.
func (e *RunError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error to errors.Is/errors.As.
func (e *RunError) Unwrap() error { return e.Err }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestRunError -v`
Expected: PASS (2/2 tests)

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS (all existing tests unaffected — `RunError` is a new, unused-so-far type)

- [ ] **Step 6: Commit**

```bash
git add internal/backup/run_error.go internal/backup/run_error_test.go
git commit -m "backup: add RunError for orphan-snapshot stage signaling"
```

---

### Task 3: `dirSize` helper

**Files:**
- Create: `internal/backup/size.go`
- Test: `internal/backup/size_test.go`

**Interfaces:**
- Produces: `dirSize(root string) (int64, error)` — sum of all regular file sizes under `root`. Consumed by Task 6 to compute the denominator for `Percent`.

- [ ] **Step 1: Write the failing test**

Create `internal/backup/size_test.go`:

```go
package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize_SumsRegularFilesRecursively(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize() error = %v, want nil", err)
	}
	const want = 5 + 10
	if got != want {
		t.Errorf("dirSize() = %d, want %d", got, want)
	}
}

func TestDirSize_IgnoresDirectoryEntriesThemselves(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty-sub"), 0o755); err != nil {
		t.Fatalf("mkdir empty-sub: %v", err)
	}

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize() error = %v, want nil", err)
	}
	if got != 0 {
		t.Errorf("dirSize() = %d, want 0 for a tree with only empty directories", got)
	}
}

func TestDirSize_MissingRootReturnsError(t *testing.T) {
	_, err := dirSize(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("dirSize() error = nil, want error for missing root")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestDirSize -v`
Expected: build failure — `dirSize` does not exist yet.

- [ ] **Step 3: Implement `dirSize`**

Create `internal/backup/size.go`:

```go
package backup

import (
	"io/fs"
	"path/filepath"
)

// dirSize returns the sum of all regular file sizes under root.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestDirSize -v`
Expected: PASS (3/3 tests)

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/backup/size.go internal/backup/size_test.go
git commit -m "backup: add dirSize helper for progress-percent denominators"
```

---

### Task 4: `copyDir` gains an `onCopy` progress callback

**Files:**
- Modify: `internal/backup/copy.go`
- Modify: `internal/backup/copy_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `copyDir(src, dst string, onCopy func(cumulativeBytes int64)) error` — `onCopy` is nil-safe and, if non-nil, is invoked after each regular file finishes copying with the running total of bytes copied so far. Consumed by Task 6.

- [ ] **Step 1: Update existing calls and write the failing test**

In `internal/backup/copy_test.go`, change the two existing calls:

```go
	if err := copyDir(src, dst); err != nil {
```

to:

```go
	if err := copyDir(src, dst, nil); err != nil {
```

(one occurrence in `TestCopyDir_CopiesNestedFilesAndPreservesSymlink`) and:

```go
	err := copyDir(filepath.Join(t.TempDir(), "does-not-exist"), dst)
```

to:

```go
	err := copyDir(filepath.Join(t.TempDir(), "does-not-exist"), dst, nil)
```

(in `TestCopyDir_MissingSourceReturnsError`). Then add a new test to the same file:

```go
func TestCopyDir_InvokesOnCopyWithCumulativeBytes(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	var calls []int64
	if err := copyDir(src, dst, func(cumulativeBytes int64) {
		calls = append(calls, cumulativeBytes)
	}); err != nil {
		t.Fatalf("copyDir() error = %v, want nil", err)
	}

	if len(calls) != 2 {
		t.Fatalf("onCopy called %d times, want 2 (one per file)", len(calls))
	}
	last := calls[len(calls)-1]
	if last != 15 {
		t.Errorf("final cumulative bytes = %d, want %d (5 + 10)", last, 15)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestCopyDir -v`
Expected: build failure — `copyDir` still takes 2 arguments, not 3.

- [ ] **Step 3: Add the `onCopy` parameter**

In `internal/backup/copy.go`, replace `copyDir`'s signature and body:

```go
// copyDir recursively copies the contents of src into dst, creating dst
// and any subdirectories as needed. Regular files are copied byte-for-byte;
// symlinks are recreated as symlinks (not followed). If onCopy is
// non-nil, it is invoked after each regular file finishes copying, with
// the running total of bytes copied so far across all files.
func copyDir(src, dst string, onCopy func(cumulativeBytes int64)) error {
	var cumulative int64
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			return os.Symlink(link, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		cumulative += info.Size()
		if onCopy != nil {
			onCopy(cumulative)
		}
		return nil
	})
}
```

`copyFile` is unchanged.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestCopyDir -v`
Expected: PASS (3/3 tests)

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS — note `choreography.go` still calls `copyDir(bundleDir, stagedBundle)` with 2 arguments at this point, so the package will NOT compile until this call site is also fixed. Update it now as part of this task, passing `nil`:

In `internal/backup/choreography.go`, change:

```go
	if err := copyDir(bundleDir, stagedBundle); err != nil {
```

to:

```go
	if err := copyDir(bundleDir, stagedBundle, nil); err != nil {
```

Then re-run `go test ./internal/backup/... -v` and confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/copy.go internal/backup/copy_test.go internal/backup/choreography.go
git commit -m "backup: add onCopy progress callback to copyDir"
```

---

### Task 5: `createArchive` gains an `onRead` progress callback

**Files:**
- Modify: `internal/backup/archive.go`
- Modify: `internal/backup/archive_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `createArchive(srcDir, destPath, requested string, onRead func(cumulativeBytes int64)) (string, error)` — `onRead` is nil-safe and, if non-nil, is invoked as bytes are read from `srcDir`'s files during tar streaming, with the running cumulative total. Consumed by Task 6.

- [ ] **Step 1: Update existing calls and write the failing test**

In `internal/backup/archive_test.go`, every existing call to `createArchive(srcDir, destPath, "...")` gains a trailing `, nil` argument. There are 6 call sites — apply this exact change to each:

```go
	used, err := createArchive(srcDir, destPath, "")
```
→
```go
	used, err := createArchive(srcDir, destPath, "", nil)
```
(in `TestCreateArchive_GzipWhenZstdUnavailable`)

```go
	used, err := createArchive(srcDir, destPath, "gzip")
```
→
```go
	used, err := createArchive(srcDir, destPath, "gzip", nil)
```
(in `TestCreateArchive_ExplicitGzipIgnoresZstdAvailability`)

```go
	used, err := createArchive(srcDir, destPath, "zstd")
```
→
```go
	used, err := createArchive(srcDir, destPath, "zstd", nil)
```
(in `TestCreateArchive_UsesZstdWhenAvailable` — the ONLY occurrence in that function)

```go
	_, err := createArchive(srcDir, destPath, "zstd")
```
→
```go
	_, err := createArchive(srcDir, destPath, "zstd", nil)
```
(this exact line appears in BOTH `TestCreateArchive_ZstdFailureIncludesStderr` and `TestCreateArchive_ZstdFailureDuringTarWrite_IncludesStderr` — update both occurrences)

```go
	_, err := createArchive(srcDir, destPath, "bogus")
```
→
```go
	_, err := createArchive(srcDir, destPath, "bogus", nil)
```
(in `TestCreateArchive_UnknownCompressionReturnsError`)

Then add a new test to the same file:

```go
func TestCreateArchive_InvokesOnReadWithCumulativeBytes(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	var calls []int64
	_, err := createArchive(srcDir, destPath, "gzip", func(cumulativeBytes int64) {
		calls = append(calls, cumulativeBytes)
	})
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}

	if len(calls) == 0 {
		t.Fatal("onRead was never called, want at least one call")
	}
	last := calls[len(calls)-1]
	if last != 15 {
		t.Errorf("final cumulative bytes = %d, want %d (5 + 10)", last, 15)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestCreateArchive -v`
Expected: build failure — `createArchive` still takes 3 arguments, not 4.

- [ ] **Step 3: Add the `onRead` parameter and a counting reader**

In `internal/backup/archive.go`, update `createArchive`, `tarToZstd`, and `tarTo`'s signatures and bodies, and add a `countingReader` type:

```go
// createArchive tars srcDir's contents and compresses the result to
// destPath. requested is "zstd", "gzip", or "" (prefer zstd, fall back to
// gzip if the zstd binary isn't on PATH). Returns which compression was
// actually used. If onRead is non-nil, it is invoked as bytes are read
// from srcDir's files, with the running cumulative total across all
// files.
func createArchive(srcDir, destPath, requested string, onRead func(cumulativeBytes int64)) (string, error) {
	useZstd := false
	switch requested {
	case "gzip":
		useZstd = false
	case "zstd", "":
		_, err := lookZstd()
		useZstd = err == nil
	default:
		return "", fmt.Errorf("unknown compression %q", requested)
	}

	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	if useZstd {
		if err := tarToZstd(srcDir, out, onRead); err != nil {
			_ = out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("close archive: %w", err)
		}
		return "zstd", nil
	}

	gz := gzip.NewWriter(out)
	if err := tarTo(srcDir, gz, onRead); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("close gzip writer: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}
	return "gzip", nil
}

// tarToZstd streams a tar of srcDir through the external zstd binary,
// writing the compressed result to out.
func tarToZstd(srcDir string, out io.Writer, onRead func(cumulativeBytes int64)) error {
	cmd := exec.Command("zstd", "-q")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("zstd stdin pipe: %w", err)
	}
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	tarErr := tarTo(srcDir, stdin, onRead)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	// Check waitErr first: if zstd itself exited with an error, that's the
	// root cause and its stderr is the only place that explains why -- a
	// tarErr in this situation is just a symptom (zstd closed the pipe
	// early, so the in-progress write into it failed with a broken pipe).
	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	if tarErr != nil {
		return tarErr
	}
	if closeErr != nil {
		return fmt.Errorf("close zstd stdin: %w", closeErr)
	}
	return nil
}

// countingReader wraps r, invoking onRead with the cumulative byte count
// (shared across every countingReader via base) after each non-empty
// Read.
type countingReader struct {
	r      io.Reader
	base   *int64
	onRead func(cumulativeBytes int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		*c.base += int64(n)
		c.onRead(*c.base)
	}
	return n, err
}

// tarTo writes a tar stream of srcDir's contents to w. The root directory
// itself is not included as an entry, only its contents (with paths
// relative to srcDir) — the caller controls wrapping by choosing what
// srcDir points at. If onRead is non-nil, it is invoked as file bytes are
// read, with the running cumulative total across all files.
func tarTo(srcDir string, w io.Writer, onRead func(cumulativeBytes int64)) error {
	tw := tar.NewWriter(w)
	var cumulative int64

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			var src io.Reader = f
			if onRead != nil {
				src = &countingReader{r: f, base: &cumulative, onRead: onRead}
			}
			_, copyErr := io.Copy(tw, src)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})

	closeErr := tw.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestCreateArchive -v`
Expected: PASS (7/7 tests)

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: `choreography.go` still calls `createArchive(stagingRoot, tempArchivePath, opts.Compression)` with 3 arguments, so the package will NOT compile until this call site is also fixed. Update it now as part of this task, passing `nil`:

In `internal/backup/choreography.go`, change:

```go
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression)
```

to:

```go
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression, nil)
```

Then re-run `go test ./internal/backup/... -v` and confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/archive.go internal/backup/archive_test.go internal/backup/choreography.go
git commit -m "backup: add onRead progress callback to createArchive/tarTo"
```

---

### Task 6: Rewire `Run()` to `ctx`, `reporter`, and the new helpers

This task is not classic RED/GREEN TDD: `Run()`'s signature change is not backward compatible, so the implementation and every existing test call site must change together in one commit for the package to compile at all. The steps below front-load the new tests (written against the target signature) alongside the mechanical call-site updates, then implement, so there is still a clear "does it compile and pass" checkpoint — just not an isolated single-test RED step.

**Files:**
- Modify: `internal/backup/choreography.go`
- Modify: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: `progress.Stage`/`Event`/`Reporter`/`NoOpReporter` (Task 1), `backup.RunError` (Task 2), `dirSize` (Task 3), `copyDir`'s `onCopy` param (Task 4), `createArchive`'s `onRead` param (Task 5).
- Produces: `Run(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts Options) (*Result, error)` — the final public signature this whole plan exists to deliver.

- [ ] **Step 1: Update every existing `backup.Run(...)` call site in `choreography_test.go`**

Apply this exact substitution wherever it appears (16 call sites total):

- Every occurrence of `backup.Run(fake, opts)` → `backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)` (2 occurrences: `TestRun_HappyPath_ProducesArchiveAndManifest`, `TestRun_CopyDirPartialFailure_CleansUpStagingDir`).
- The one occurrence of `backup.Run(ctrl, backup.Options{` → `backup.Run(t.Context(), ctrl, progress.NoOpReporter{}, backup.Options{` (in `TestRun_ReadsGuestOSBeforeSnapshot`).
- Every remaining occurrence of `backup.Run(fake, backup.Options{` → `backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{` (13 occurrences: `TestRun_NonRunningToolsState_RecordsCrashConsistent`, `TestRun_CheckToolsStateError_AbortsBeforeSnapshot`, `TestRun_SnapshotError_Aborts`, `TestRun_ListSnapshotsError_Aborts`, `TestRun_DeleteSnapshotError_StillCleansUpStaging`, `TestRun_CreateArchiveError_RemovesOutputDir`, `TestRun_EmptyVMName_ReturnsError`, `TestRun_PathTraversalVMName_ReturnsError`, `TestRun_EmptyDestination_ReturnsError`, `TestRun_MissingVMXPath_ReturnsError`, `TestRun_VMXPathIsDirectory_ReturnsError`, `TestRun_CompressionAuto_PrefersZstdWhenAvailable`, `TestRun_OutputPermissions_MatchStagingHardening`).

Add two imports to `choreography_test.go`'s import block:

```go
	"context"
```
(alongside the existing standard-library imports) and:
```go
	"github.com/xortim/snapback/internal/progress"
```
(alongside the existing `github.com/xortim/snapback/internal/backup` and `.../internal/vm` imports).

- [ ] **Step 2: Add the new tests**

Append to `internal/backup/choreography_test.go`:

```go
type fakeReporter struct {
	events []progress.Event
}

func (r *fakeReporter) Report(e progress.Event) {
	r.events = append(r.events, e)
}

func TestRun_HappyPath_ReportsStagesInOrder(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	reporter := &fakeReporter{}

	_, err := backup.Run(t.Context(), fake, reporter, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	wantOrder := []progress.Stage{
		progress.CheckingTools,
		progress.Snapshotting,
		progress.Copying,
		progress.Merging,
		progress.Compressing,
		progress.Checksumming,
		progress.Done,
	}
	var gotOrder []progress.Stage
	seen := map[progress.Stage]bool{}
	for _, e := range reporter.events {
		if !seen[e.Stage] {
			seen[e.Stage] = true
			gotOrder = append(gotOrder, e.Stage)
		}
	}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("distinct stages reported = %v, want %v", gotOrder, wantOrder)
	}
	for i, stage := range wantOrder {
		if gotOrder[i] != stage {
			t.Errorf("stage[%d] = %v, want %v (full order: %v)", i, gotOrder[i], stage, gotOrder)
		}
	}
}

func TestRun_CanceledContext_AbortsBeforeAnyCtrlCall(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := backup.Run(ctx, fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for a pre-canceled context")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	if runErr.Stage != progress.CheckingTools {
		t.Errorf("runErr.Stage = %v, want %v (canceled before any ctrl call)", runErr.Stage, progress.CheckingTools)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, want true")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; a pre-canceled context must abort before touching ctrl", snapshots)
	}
}

func TestRun_SnapshotError_RunErrorStageBelowSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = errBoom

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// Snapshot itself failed, so no snapshot was ever created -- Stage
	// must NOT be >= Snapshotting, or a caller would wrongly point the
	// user at `snapback cleanup` for a snapshot that doesn't exist.
	if runErr.Stage >= progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want < %v (Snapshot itself failed, nothing to orphan)", runErr.Stage, progress.Snapshotting)
	}
}

func TestRun_DeleteSnapshotError_RunErrorStageAtOrAboveSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// The snapshot was taken successfully and DeleteSnapshot (the merge)
	// is what failed -- a snapshot may still exist on the source VM, so
	// Stage must be >= Snapshotting to trigger the cleanup pointer.
	if runErr.Stage < progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want >= %v (snapshot exists, merge failed)", runErr.Stage, progress.Snapshotting)
	}
}
```

- [ ] **Step 3: Confirm the test file alone doesn't compile yet**

Run: `go test ./internal/backup/... -v`
Expected: build failure — `Run` still has its old 2-argument signature, so every updated call site (and the new tests) fails to compile. This is the expected RED before Step 4.

- [ ] **Step 4: Rewrite `Run()`**

Replace the full contents of `internal/backup/choreography.go`:

```go
package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// Options configures a single backup run.
type Options struct {
	VMName      string
	VMXPath     string
	Comment     string
	Destination string           // parent directory the backup's output directory is created under
	StagingDir  string           // parent directory for the temporary staging copy; os.TempDir() if empty
	Compression string           // "zstd", "gzip", or "" (prefer zstd, fall back to gzip)
	Now         func() time.Time // defaults to time.Now if nil
}

// Result describes a completed backup.
type Result struct {
	ArchiveID    string
	OutputDir    string
	ArchivePath  string
	ManifestPath string
	Manifest     Manifest
}

// Run executes the snapshot -> copy -> merge -> archive choreography
// described in docs/design.md ("Backup choreography") against ctrl,
// reporting progress.Events to reporter at each stage. The source VM is
// never paused: ctrl.Snapshot freezes the disk state, the bundle is
// copied while the VM keeps running against a fresh delta, and
// ctrl.DeleteSnapshot merges that delta back once the copy is safely
// staged.
//
// ctx is checked between stages, not during an in-flight ctrl call --
// vm.Controller's methods don't take a context, so a call already in
// progress runs to completion regardless of cancellation (see ADR-003,
// docs/superpowers/specs/2026-08-27-run-progress-context-design.md).
//
// Every error Run returns is a *RunError carrying the Stage active when
// it occurred, so a caller can tell whether a snapshot may have been
// left orphaned on the source VM (Stage >= progress.Snapshotting) --
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback here. Once DeleteSnapshot succeeds
// (Stage: Merging and later), no snapshot actually remains even though
// Stage is still >= Snapshotting -- the caller's cleanup pointer becomes
// a harmless no-op in that case rather than a precise signal, which is
// an accepted simplification (see ADR-003's Risks section).
func Run(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts Options) (*Result, error) {
	// Stage: CheckingTools -- pre-flight validation, readGuestOS, and
	// ctrl.CheckToolsState. No ctrl call has happened yet, so any error
	// here means there is no snapshot to orphan.
	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: err}
	}
	if opts.VMName == "" || filepath.Base(opts.VMName) != opts.VMName {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("invalid VMName %q", opts.VMName)}
	}
	if opts.Destination == "" {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("destination is required")}
	}
	vmxInfo, err := os.Stat(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("vmx path: %w", err)}
	}
	if vmxInfo.IsDir() {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("vmx path %q is a directory, want a regular file", opts.VMXPath)}
	}

	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("read guest OS: %w", err)}
	}

	bundleDir := filepath.Dir(opts.VMXPath)
	totalBytes, err := dirSize(bundleDir)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("measure bundle size: %w", err)}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	startTime := now().UTC()
	ts := startTime.Format("20060102T150405Z")
	archiveID := fmt.Sprintf("%s-%s", opts.VMName, ts)
	snapshotName := "snapback-" + ts

	reporter.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking VMware Tools state"})
	toolsState, err := ctrl.CheckToolsState(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("check tools state: %w", err)}
	}

	// Stage: Snapshotting
	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.Snapshotting, Err: err}
	}
	reporter.Report(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot " + snapshotName})
	if err := ctrl.Snapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, &RunError{Stage: progress.Snapshotting, Err: fmt.Errorf("snapshot: %w", err)}
	}

	snapshots, err := ctrl.ListSnapshots(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.Snapshotting, Err: fmt.Errorf("list snapshots: %w", err)}
	}
	if !slices.Contains(snapshots, snapshotName) {
		return nil, &RunError{Stage: progress.Snapshotting, Err: fmt.Errorf("snapshot %q not found after creation", snapshotName)}
	}

	// Stage: Copying
	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.Copying, Err: err}
	}
	reporter.Report(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"})
	hostSync()

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingRoot := filepath.Join(stagingParent, "snapback-staging-"+archiveID)
	stagedBundle := filepath.Join(stagingRoot, filepath.Base(bundleDir))
	defer func() { _ = os.RemoveAll(stagingRoot) }()
	onCopy := func(cumulativeBytes int64) {
		reporter.Report(progress.Event{Stage: progress.Copying, Percent: percentOf(cumulativeBytes, totalBytes)})
	}
	if err := copyDir(bundleDir, stagedBundle, onCopy); err != nil {
		return nil, &RunError{Stage: progress.Copying, Err: fmt.Errorf("copy bundle: %w", err)}
	}

	// Stage: Merging
	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.Merging, Err: err}
	}
	reporter.Report(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"})
	if err := ctrl.DeleteSnapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, &RunError{Stage: progress.Merging, Err: fmt.Errorf("delete snapshot: %w", err)}
	}

	// Stage: Compressing
	outputDir := filepath.Join(opts.Destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("create output dir: %w", err)}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(outputDir)
		}
	}()

	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: err}
	}
	reporter.Report(progress.Event{Stage: progress.Compressing, Message: "compressing archive"})
	tempArchivePath := filepath.Join(outputDir, "archive.tmp")
	onRead := func(cumulativeBytes int64) {
		reporter.Report(progress.Event{Stage: progress.Compressing, Percent: percentOf(cumulativeBytes, totalBytes)})
	}
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression, onRead)
	if err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("create archive: %w", err)}
	}
	ext := "tar.gz"
	if usedCompression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	if err := os.Rename(tempArchivePath, archivePath); err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("rename archive: %w", err)}
	}

	// Stage: Checksumming
	if err := ctx.Err(); err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: err}
	}
	reporter.Report(progress.Event{Stage: progress.Checksumming, Message: "checksumming archive"})
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: fmt.Errorf("stat archive: %w", err)}
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: err}
	}

	manifest := Manifest{
		VMName:      opts.VMName,
		GuestOS:     guestOS,
		SizeBytes:   info.Size(),
		Comment:     opts.Comment,
		Timestamp:   startTime,
		ToolsState:  toolsState,
		SHA256:      sum,
		Compression: usedCompression,
	}
	manifestPath := filepath.Join(outputDir, "manifest.json")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: err}
	}

	succeeded = true
	reporter.Report(progress.Event{Stage: progress.Done, Message: "backup complete"})
	return &Result{
		ArchiveID:    archiveID,
		OutputDir:    outputDir,
		ArchivePath:  archivePath,
		ManifestPath: manifestPath,
		Manifest:     manifest,
	}, nil
}

// percentOf returns cumulative/total as a fraction in [0,1], or 0 if
// total is 0 (an empty bundle -- avoids a division by zero).
func percentOf(cumulative, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(cumulative) / float64(total)
}

// hostSync flushes the host page cache before copy.go reads the frozen
// disk files. Best-effort only: the snapshot already guarantees the
// source files are frozen at the VMware layer, so a failure here is not
// fatal — it just removes a class of doubt per docs/design.md.
func hostSync() {
	_ = exec.Command("sync").Run()
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/backup/... -v`
Expected: PASS — all pre-existing tests (now updated to the new call signature) plus the 4 new tests from Step 2, all green.

- [ ] **Step 6: Run the full repo test suite and vet**

Run: `go test ./... -v` and `go vet ./...`
Expected: both clean. (`internal/cli` and `internal/config` are untouched by this plan and should be unaffected.)

- [ ] **Step 7: Commit**

```bash
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "backup: wire Run() to context.Context and progress.Reporter per ADR-003"
```

---

## Final Verification

- [ ] `go test ./...` — all pass.
- [ ] `go vet ./...` — clean.
- [ ] `gofmt -l .` — no output (or `make lint` if the environment's golangci-lint/Go-toolchain versions are compatible).
- [ ] `go build ./...` — builds.
- [ ] Confirm `internal/cli` still builds and its existing tests still pass unchanged — this plan does not touch CLI wiring (that's issue #7).
