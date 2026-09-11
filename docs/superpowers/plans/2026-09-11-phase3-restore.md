# Phase 3 — Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `snapback restore`: verify a backup archive against its manifest, extract it, confirm the restored disk chain is consistent, and place it as a new, non-destructively-named `.vmwarevm` next to the source — never overwriting anything.

**Architecture:** A new `backup.Restore(ctx, ctrl, reporter, opts) (*RestoreResult, error)` in `internal/backup`, alongside the existing `Run`, reusing `Manifest`, `ListArchives`, `sha256File`-shaped helpers, and a new `extractArchive` (the untar counterpart to `createArchive`). `internal/tui`'s `Model` is generalized behind two small interfaces (`pipelineResult`, `pipelineError`) so `run` and `restore` share one bubbletea component instead of duplicating it. `internal/cli/restore.go` wires it up following `run.go`'s existing plain-vs-interactive split.

**Tech Stack:** Go 1.26.5, cobra, bubbletea/bubbles/lipgloss, `zstd`/`vmware-vdiskmanager` external binaries (already-established patterns).

**Spec:** `docs/superpowers/specs/2026-09-11-restore-design.md` (ADR-004). GitHub epic: #70, sub-issues #71 (archive lookup + naming), #72 (`extract.go`), #73 (`backup.Restore` pipeline), #74 (`internal/tui` generalization), #75 (CLI wiring).

## Global Constraints

- `RestoreOptions` takes **exactly one** of `TargetDir` or `VMXPath` — neither or both is a validation error tagged `Stage: Verifying`.
- New `progress.Stage` values (`Verifying`, `Extracting`, `CheckingDiskConsistency`, `Placing`) are appended **after** the existing `Notifying`, not interleaved — nothing depends on `Stage`'s numeric order, only on the per-pipeline `[]progress.Stage` lists matching by equality.
- A zstd-compressed archive **requires** the `zstd` binary to restore — no gzip-style fallback at restore time (unlike `createArchive`'s create-time fallback).
- No new locking for `Restore` (out of scope per ADR-004 — `Restore` never touches the source VM, only the archive and the restore target).
- `restore` never registers the restored bundle with Fusion (`vmrun register` or equivalent) — it places a normal `.vmwarevm` on disk and stops.
- `RunInteractive`'s existing exported signature is unchanged; it becomes a thin wrapper over generalized internals.
- `restoreTargetName`'s collision loop is capped at 100 attempts (`N = 2..101`) before erroring.

---

## Task 1: `progress.Stage` additions + `RunError.FailedStage()`

**Files:**
- Modify: `internal/progress/progress.go`
- Modify: `internal/progress/progress_test.go`
- Modify: `internal/backup/run_error.go`
- Modify: `internal/backup/run_error_test.go`

**Interfaces:**
- Produces: `progress.Verifying`, `progress.Extracting`, `progress.CheckingDiskConsistency`, `progress.Placing` (new `Stage` consts); `(*backup.RunError).FailedStage() progress.Stage`.

- [ ] **Step 1: Write the failing tests**

In `internal/progress/progress_test.go`, update `TestStages_AreDistinctValues` and `TestStage_String`:

```go
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
		progress.Verifying,
		progress.Extracting,
		progress.CheckingDiskConsistency,
		progress.Placing,
		progress.Done,
	}
	seen := map[progress.Stage]bool{}
	for _, s := range stages {
		if seen[s] {
			t.Errorf("stage %v appears more than once in the const block", s)
		}
		seen[s] = true
	}
	if len(seen) != 13 {
		t.Errorf("got %d distinct stages, want 13", len(seen))
	}
}

func TestStage_String(t *testing.T) {
	cases := map[progress.Stage]string{
		progress.CheckingTools:           "checking tools",
		progress.Snapshotting:            "snapshotting",
		progress.Verifying:               "verifying",
		progress.Extracting:              "extracting",
		progress.CheckingDiskConsistency: "checking disk consistency",
		progress.Placing:                 "placing",
		progress.Done:                    "done",
		progress.Stage(99):               "stage(99)",
	}
	for stage, want := range cases {
		if got := stage.String(); got != want {
			t.Errorf("Stage(%d).String() = %q, want %q", stage, got, want)
		}
	}
}
```

In `internal/backup/run_error_test.go`, add:

```go
func TestRunError_FailedStage(t *testing.T) {
	err := &backup.RunError{Stage: progress.Merging, Err: errors.New("boom")}
	if got := err.FailedStage(); got != progress.Merging {
		t.Errorf("FailedStage() = %v, want %v", got, progress.Merging)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/progress/... ./internal/backup/... -run 'TestStages_AreDistinctValues|TestStage_String|TestRunError_FailedStage' -v`
Expected: FAIL — `progress.Verifying` etc. undefined; `FailedStage` undefined on `*RunError`.

- [ ] **Step 3: Implement**

In `internal/progress/progress.go`, replace the const block and `stageNames`:

```go
const (
	CheckingTools Stage = iota
	Snapshotting
	Copying
	Merging
	Compressing
	Checksumming
	Pruning
	Notifying
	Verifying
	Extracting
	CheckingDiskConsistency
	Placing
	Done
)

// stageNames is indexed by Stage; must stay in sync with the const block
// above.
var stageNames = [...]string{
	CheckingTools:            "checking tools",
	Snapshotting:             "snapshotting",
	Copying:                  "copying",
	Merging:                  "merging",
	Compressing:              "compressing",
	Checksumming:             "checksumming",
	Pruning:                  "pruning",
	Notifying:                "notifying",
	Verifying:                "verifying",
	Extracting:               "extracting",
	CheckingDiskConsistency:  "checking disk consistency",
	Placing:                  "placing",
	Done:                     "done",
}
```

In `internal/backup/run_error.go`, add below `Unwrap`:

```go
// FailedStage implements the pipelineError interface internal/tui's
// generalized Model matches failures against -- see RestoreError, which
// carries the same method for backup.Restore's failures.
func (e *RunError) FailedStage() progress.Stage { return e.Stage }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/progress/... ./internal/backup/... -v`
Expected: PASS (all existing progress/backup tests plus the new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/progress/progress.go internal/progress/progress_test.go internal/backup/run_error.go internal/backup/run_error_test.go
git commit -m "feat(progress,backup): add restore pipeline stages and RunError.FailedStage"
```

---

## Task 2: `extractArchive` — decompress+untar counterpart to `createArchive`

**Files:**
- Create: `internal/backup/extract.go`
- Test: `internal/backup/extract_test.go` (package `backup`, mirrors `archive_test.go`'s internal-access convention)

**Interfaces:**
- Consumes: `lookZstd func() (string, error)` (`archive.go`, package-level var).
- Produces: `extractArchive(srcPath, destDir, compression string, onWrite func(cumulativeBytes int64)) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/backup/extract_test.go`:

```go
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExtractArchive_GzipRoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	var calls []int64
	if err := extractArchive(archivePath, destDir, "gzip", func(cumulative int64) { calls = append(calls, cumulative) }); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}

	gotA, err := os.ReadFile(filepath.Join(destDir, "a.txt"))
	if err != nil || string(gotA) != "hello" {
		t.Errorf("a.txt = %q, %v, want %q, nil", gotA, err, "hello")
	}
	gotB, err := os.ReadFile(filepath.Join(destDir, "sub", "b.txt"))
	if err != nil || string(gotB) != "world" {
		t.Errorf("sub/b.txt = %q, %v, want %q, nil", gotB, err, "world")
	}
	if len(calls) == 0 {
		t.Error("onWrite was never called, want at least one call")
	}
}

func TestExtractArchive_ZstdRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.zst")
	if _, err := createArchive(srcDir, archivePath, "zstd", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "zstd", nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("a.txt = %q, %v, want %q, nil", got, err, "hello")
	}
}

func TestExtractArchive_DestDirAlreadyExists_ReturnsError(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := t.TempDir() // already exists
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a pre-existing destDir")
	}
}

func TestExtractArchive_CorruptedArchive_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not a valid gzip stream"), 0o644); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a corrupted archive")
	}
}

func TestExtractArchive_UnknownCompression_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.out")
	if err := os.WriteFile(archivePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "bogus", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for unknown compression")
	}
}

func TestExtractArchive_PathTraversalEntry_IsRejected(t *testing.T) {
	// Hand-craft a tar.gz with a "../escape.txt" entry -- extractArchive
	// must reject this rather than write outside destDir.
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.txt", Size: int64(len(content)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want a path-traversal rejection")
	}
	if _, err := os.Stat(filepath.Join(destParent, "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("escape.txt exists outside destDir, want it never written")
	}
	_ = bytes.NewReader // keep bytes imported for future assertions in this file
}
```

(The final `_ = bytes.NewReader` line is a placeholder-avoidance no-op only if `bytes` ends up otherwise unused — if `go vet`/`goimports` flags it as an unused import instead, simply drop the `"bytes"` import; it isn't needed once this test is written as-is.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestExtractArchive -v`
Expected: FAIL with `undefined: extractArchive`.

- [ ] **Step 3: Implement**

Create `internal/backup/extract.go`:

```go
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractArchive decompresses+untars srcPath (compressed as identified by
// compression, "zstd" or "gzip" -- Manifest.Compression, not re-sniffed)
// into destDir, which must not already exist. Mirrors createArchive's
// shape in reverse. If onWrite is non-nil, it's invoked with the running
// cumulative bytes written across all files.
func extractArchive(srcPath, destDir, compression string, onWrite func(cumulativeBytes int64)) error {
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("extract archive: %s already exists", destDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()

	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return untarFrom(gz, destDir, onWrite)
	case "zstd":
		return untarFromZstd(in, destDir, onWrite)
	default:
		return fmt.Errorf("unknown compression %q", compression)
	}
}

// untarFromZstd streams in through the external zstd binary's decompressor
// and untars the result into destDir -- the reverse of tarToZstd
// (archive.go). Manifest.Compression already records that this archive
// was compressed with zstd, so there's no fallback-detection logic here:
// if zstd isn't on PATH, restoring this archive fails outright.
func untarFromZstd(in io.Reader, destDir string, onWrite func(cumulativeBytes int64)) error {
	if _, err := lookZstd(); err != nil {
		return fmt.Errorf("zstd not found on PATH (required to restore a zstd-compressed archive): %w", err)
	}
	cmd := exec.Command("zstd", "-d", "-q")
	cmd.Stdin = in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("zstd stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	untarErr := untarFrom(stdout, destDir, onWrite)
	waitErr := cmd.Wait()

	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	if untarErr != nil {
		return untarErr
	}
	return nil
}

// untarFrom reads a tar stream from r and writes its entries under destDir,
// rejecting any entry whose resolved path would land outside destDir
// (rejecting ".."-escaping or absolute entry names) -- a cheap, standard
// guard against a corrupted or tampered archive writing outside the
// staging directory. If onWrite is non-nil, it's invoked as file bytes are
// written, with the running cumulative total across all files.
func untarFrom(r io.Reader, destDir string, onWrite func(cumulativeBytes int64)) error {
	tr := tar.NewReader(r)
	var cumulative int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		if !isWithinDir(destDir, target) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			n, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
			cumulative += n
			if onWrite != nil {
				onWrite(cumulative)
			}
		default:
			// Anything else (device, fifo, ...) isn't something
			// createArchive ever produces from a .vmwarevm bundle -- skip
			// rather than fail the whole restore over it.
		}
	}
}

// isWithinDir reports whether target, once resolved relative to dir,
// stays under dir -- i.e. its relative path doesn't start with "..".
func isWithinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run TestExtractArchive -v`
Expected: PASS. Also run `go build ./...` to confirm the `bytes` import in the test file doesn't trip `goimports`/`go vet` on an unused import — if it does, delete the `"bytes"` import and the trailing `_ = bytes.NewReader` line from the test (the test doesn't otherwise need it).

- [ ] **Step 5: Commit**

```bash
git add internal/backup/extract.go internal/backup/extract_test.go
git commit -m "feat(backup): add extractArchive, the decompress+untar counterpart to createArchive"
```

---

## Task 3: Archive lookup and non-destructive target naming

**Files:**
- Create: `internal/backup/restore_naming.go`
- Test: `internal/backup/restore_naming_test.go` (package `backup`)

**Interfaces:**
- Consumes: `ListArchives(destination string) ([]Archive, error)` (`list.go`).
- Produces: `FindArchive(destination, archiveID string) (Archive, error)`, `LatestArchiveForVM(destination, vmName string) (Archive, error)`, `restoreTargetName(parent, bundleBase string, now time.Time) (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/backup/restore_naming_test.go`:

```go
package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreTargetName_NoCollision(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11.vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_OneCollision_AppendsSuffix(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision: %v", err)
	}
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11 (2).vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_TwoCollisions_AppendsNextSuffix(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision 1: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11 (2).vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision 2: %v", err)
	}
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11 (3).vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_ExhaustsAttempts_ReturnsError(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create base: %v", err)
	}
	for n := 2; n <= 100; n++ {
		name := filepath.Join(parent, filepath_sprintf(n))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatalf("pre-create (%d): %v", n, err)
		}
	}
	_, err := restoreTargetName(parent, "myvm", now)
	if err == nil {
		t.Fatal("restoreTargetName() error = nil, want an error once every attempt up to 100 collides")
	}
}

func filepath_sprintf(n int) string {
	return "myvm - backup 2026-09-11 (" + itoa(n) + ").vmwarevm"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestFindArchive_ResolvesByExactID(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})

	archive, err := FindArchive(destination, "myvm-20260101T000000Z")
	if err != nil {
		t.Fatalf("FindArchive() error = %v, want nil", err)
	}
	if archive.ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("ArchiveID = %q, want %q", archive.ArchiveID, "myvm-20260101T000000Z")
	}
}

func TestFindArchive_UnresolvableID_ReturnsError(t *testing.T) {
	destination := t.TempDir()
	if _, err := FindArchive(destination, "does-not-exist"); err == nil {
		t.Fatal("FindArchive() error = nil, want an error for an unresolvable archive ID")
	}
}

func TestLatestArchiveForVM_ReturnsNewest(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	writeTestManifest(t, destination, "myvm-20260601T000000Z", Manifest{VMName: "myvm", Timestamp: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	writeTestManifest(t, destination, "other-20260901T000000Z", Manifest{VMName: "other", Timestamp: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})

	archive, err := LatestArchiveForVM(destination, "myvm")
	if err != nil {
		t.Fatalf("LatestArchiveForVM() error = %v, want nil", err)
	}
	if archive.ArchiveID != "myvm-20260601T000000Z" {
		t.Errorf("ArchiveID = %q, want the newer myvm archive", archive.ArchiveID)
	}
}

func TestLatestArchiveForVM_NoArchivesForVM_ReturnsError(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "other-20260101T000000Z", Manifest{VMName: "other"})

	if _, err := LatestArchiveForVM(destination, "myvm"); err == nil {
		t.Fatal("LatestArchiveForVM() error = nil, want an error when no archive matches the VM name")
	}
}
```

(`writeTestManifest` already exists in `list_test.go`, same `backup` package — reused here rather than duplicated. The small `itoa`/`filepath_sprintf` helpers avoid pulling in `fmt` or `strconv` purely for building the 99 collision fixture names in the exhaustion test; `strconv.Itoa` is equally fine to use instead if preferred — either is a trivial substitution.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run 'TestRestoreTargetName|TestFindArchive|TestLatestArchiveForVM' -v`
Expected: FAIL with `undefined: restoreTargetName` / `undefined: FindArchive` / `undefined: LatestArchiveForVM`.

- [ ] **Step 3: Implement**

Create `internal/backup/restore_naming.go`:

```go
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FindArchive resolves archiveID to its Archive via ListArchives, returning
// a clear error if it isn't found -- before any I/O beyond the directory
// scan ListArchives itself already does. Exported for internal/cli's
// restore command, which needs to resolve an archive-id to its
// Manifest.VMName (for the missing-VM-requires-`--dest` check) before
// calling backup.Restore.
func FindArchive(destination, archiveID string) (Archive, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return Archive{}, err
	}
	for _, a := range archives {
		if a.ArchiveID == archiveID {
			return a, nil
		}
	}
	return Archive{}, fmt.Errorf("no archive %q found in %s", archiveID, destination)
}

// LatestArchiveForVM returns the newest archive whose Manifest.VMName
// matches vmName (ListArchives already returns newest-first), or an error
// if none exist. Backs `snapback restore --vm <name> --latest`.
func LatestArchiveForVM(destination, vmName string) (Archive, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return Archive{}, err
	}
	for _, a := range archives {
		if a.Manifest.VMName == vmName {
			return a, nil
		}
	}
	return Archive{}, fmt.Errorf("no archive found for VM %q in %s", vmName, destination)
}

// restoreTargetName returns "<bundleBase> - backup <yyyy-mm-dd>.vmwarevm"
// under parent, or that name with " (N)" inserted before the extension if
// the plain name already exists, trying N = 2, 3, ... until a free name is
// found. Capped at 100 attempts -- 100 same-day restores of the same VM
// without cleanup is almost certainly a bug, not a real use case.
func restoreTargetName(parent, bundleBase string, now time.Time) (string, error) {
	date := now.Format("2006-01-02")
	base := fmt.Sprintf("%s - backup %s.vmwarevm", bundleBase, date)
	if !pathExists(filepath.Join(parent, base)) {
		return base, nil
	}
	stem := fmt.Sprintf("%s - backup %s", bundleBase, date)
	for n := 2; n <= 101; n++ {
		candidate := fmt.Sprintf("%s (%d).vmwarevm", stem, n)
		if !pathExists(filepath.Join(parent, candidate)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("restoreTargetName: exhausted 100 collision attempts for %q under %q", base, parent)
}

// pathExists reports whether path exists (as anything -- file, dir, or
// symlink), without following a symlink to check its target.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run 'TestRestoreTargetName|TestFindArchive|TestLatestArchiveForVM' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/restore_naming.go internal/backup/restore_naming_test.go
git commit -m "feat(backup): add archive lookup and non-destructive restore target naming"
```

---

## Task 4: `backup.Restore` pipeline

**Files:**
- Create: `internal/backup/restore.go`
- Modify: `internal/backup/choreography.go` (add `(*Result).Summary()`, needed by Task 5's `pipelineResult`)
- Test: `internal/backup/restore_test.go` (package `backup`, needs unexported `createArchive`/`sha256File`/`writeManifest` to build fixture archives per ADR-004's testing section)

**Interfaces:**
- Consumes: `extractArchive` (Task 2), `FindArchive`/`restoreTargetName` (Task 3), `checkDisksConsistent`/`readDiskFiles` (existing, `choreography.go`/`vmx.go`), `copyDir` (existing, `copy.go`), `percentOf` (existing, `choreography.go`), `vm.Controller.CheckDiskConsistency`, `progress.Verifying/Extracting/CheckingDiskConsistency/Placing/Done` (Task 1).
- Produces: `RestoreOptions{ArchiveID, Destination, TargetDir, VMXPath, StagingDir, Now}`, `RestoreResult{ArchiveID, TargetPath, Manifest}` with `Summary() string`, `RestoreError{Stage, Err}` with `Error()/Unwrap()/FailedStage()`, `Restore(ctx, ctrl, reporter, opts) (*RestoreResult, error)`. Also `(*Result).Summary() string` on the existing `Result` type.

- [ ] **Step 1: Write the failing tests**

Create `internal/backup/restore_test.go`:

```go
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

var errRestoreBoom = errors.New("boom")

// buildFixtureArchive writes a small fake .vmwarevm bundle, archives it
// with a real createArchive call (round-tripping the same code path Run
// uses, per ADR-004's testing note, rather than hand-crafting tar bytes),
// and writes a matching manifest.json under destination/<archiveID>/. It
// returns the archiveID and the Manifest actually written, so tests can
// mutate and rewrite it (e.g. to force a checksum mismatch).
func buildFixtureArchive(t *testing.T, destination, vmName, compression string) (archiveID string, m Manifest) {
	t.Helper()

	bundleName := vmName + ".vmwarevm"
	stagingRoot := t.TempDir()
	stagedBundle := filepath.Join(stagingRoot, bundleName)
	if err := os.MkdirAll(stagedBundle, 0o700); err != nil {
		t.Fatalf("mkdir staged bundle: %v", err)
	}
	vmxContent := "guestOS = \"ubuntu-64\"\nscsi0:0.fileName = \"disk.vmdk\"\n"
	if err := os.WriteFile(filepath.Join(stagedBundle, vmName+".vmx"), []byte(vmxContent), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	archiveID = vmName + "-20260911T120000Z"
	outputDir := filepath.Join(destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("mkdir output dir: %v", err)
	}
	ext := "tar.gz"
	if compression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	usedCompression, err := createArchive(stagingRoot, archivePath, compression, nil)
	if err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}

	m = Manifest{
		VMName:      vmName,
		GuestOS:     "ubuntu-64",
		SizeBytes:   info.Size(),
		Timestamp:   time.Now().UTC(),
		ToolsState:  vm.ToolsRunning,
		SHA256:      sum,
		Compression: usedCompression,
	}
	if err := writeManifest(filepath.Join(outputDir, "manifest.json"), m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return archiveID, m
}

func TestRestore_HappyPath_PlacesRestoredBundle(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	targetParent := t.TempDir()

	opts := RestoreOptions{
		ArchiveID:   archiveID,
		Destination: destination,
		TargetDir:   targetParent,
		Now:         func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) },
	}

	result, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	if err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}
	wantPath := filepath.Join(targetParent, "myvm - backup 2026-09-11.vmwarevm")
	if result.TargetPath != wantPath {
		t.Errorf("TargetPath = %q, want %q", result.TargetPath, wantPath)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "myvm.vmx")); err != nil {
		t.Errorf("restored vmx missing at %s: %v", wantPath, err)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "disk.vmdk")); err != nil {
		t.Errorf("restored disk missing at %s: %v", wantPath, err)
	}
}

func TestRestore_ChecksumMismatch_FailsBeforeExtracting(t *testing.T) {
	destination := t.TempDir()
	archiveID, m := buildFixtureArchive(t, destination, "myvm", "gzip")
	m.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	if err := writeManifest(filepath.Join(destination, archiveID, "manifest.json"), m); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	stagingParent := t.TempDir()
	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); !os.IsNotExist(statErr) {
		t.Errorf("staging dir %s exists, want it never created on a checksum failure", stagingDir)
	}
}

func TestRestore_DiskConsistencyFailure_PreservesStagingDir(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	stagingParent := t.TempDir()
	ctrl := vm.NewFakeVMController()
	ctrl.DiskConsistencyErr = errRestoreBoom

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), ctrl, progress.NoOpReporter{}, opts)

	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.CheckingDiskConsistency {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.CheckingDiskConsistency)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); statErr != nil {
		t.Errorf("staging dir %s missing, want it preserved for inspection: %v", stagingDir, statErr)
	}
}

func TestRestore_TargetCollision_AppendsNumericSuffix(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	targetParent := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }

	if err := os.MkdirAll(filepath.Join(targetParent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetParent, "myvm - backup 2026-09-11 (2).vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create second collision target: %v", err)
	}

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: targetParent, Now: now}
	result, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	if err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}
	want := filepath.Join(targetParent, "myvm - backup 2026-09-11 (3).vmwarevm")
	if result.TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", result.TargetPath, want)
	}
}

func TestRestore_UnresolvableArchiveID_ReturnsErrorBeforeIO(t *testing.T) {
	destination := t.TempDir()
	opts := RestoreOptions{ArchiveID: "does-not-exist", Destination: destination, TargetDir: t.TempDir()}
	if _, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts); err == nil {
		t.Fatal("Restore() error = nil, want an error for an unresolvable archive ID")
	}
}

func TestRestore_MissingTargetDirAndVMXPath_ReturnsValidationError(t *testing.T) {
	opts := RestoreOptions{ArchiveID: "whatever", Destination: t.TempDir()}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
}

func TestRestore_BothTargetDirAndVMXPath_ReturnsValidationError(t *testing.T) {
	opts := RestoreOptions{ArchiveID: "whatever", Destination: t.TempDir(), TargetDir: "/a", VMXPath: "/b/myvm.vmx"}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
}

func TestResult_Summary(t *testing.T) {
	r := &Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}
	want := "backup complete: /dest/myvm-x/archive.tar.zst"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestRestoreResult_Summary(t *testing.T) {
	r := &RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}
	want := "restore complete: /vms/myvm - backup 2026-09-11.vmwarevm"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestRestoreError_FailedStage(t *testing.T) {
	err := &RestoreError{Stage: progress.Extracting, Err: errRestoreBoom}
	if got := err.FailedStage(); got != progress.Extracting {
		t.Errorf("FailedStage() = %v, want %v", got, progress.Extracting)
	}
	if !errors.Is(error(err), errRestoreBoom) {
		t.Error("errors.Is(err, errRestoreBoom) = false, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run 'TestRestore|TestResult_Summary|TestRestoreResult_Summary|TestRestoreError' -v`
Expected: FAIL — `RestoreOptions`, `Restore`, `RestoreResult`, `RestoreError`, `(*Result).Summary` all undefined.

- [ ] **Step 3: Implement**

In `internal/backup/choreography.go`, add after the `Result` struct definition:

```go
// Summary implements internal/tui's pipelineResult interface, letting the
// generalized Model render either a completed Run or a completed Restore
// without a type switch.
func (r *Result) Summary() string {
	return fmt.Sprintf("backup complete: %s", r.ArchivePath)
}
```

Create `internal/backup/restore.go`:

```go
package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// RestoreOptions configures a single restore.
type RestoreOptions struct {
	ArchiveID   string
	Destination string           // parent directory archives live under (cfg.Destination)
	TargetDir   string           // parent directory to place the restored bundle in; mutually exclusive with VMXPath
	VMXPath     string           // source VM's vmx path, if known; mutually exclusive with TargetDir
	StagingDir  string           // parent directory for the temporary extraction; os.TempDir() if empty
	Now         func() time.Time // defaults to time.Now if nil; drives the "backup yyyy-mm-dd" suffix
}

// RestoreResult describes a completed restore.
type RestoreResult struct {
	ArchiveID  string
	TargetPath string // the placed .vmwarevm's full path
	Manifest   Manifest
}

// Summary implements internal/tui's pipelineResult interface.
func (r *RestoreResult) Summary() string {
	return fmt.Sprintf("restore complete: %s", r.TargetPath)
}

// RestoreError mirrors RunError: which stage was active when the restore
// failed.
type RestoreError struct {
	Stage progress.Stage
	Err   error
}

func (e *RestoreError) Error() string               { return e.Err.Error() }
func (e *RestoreError) Unwrap() error                { return e.Err }
func (e *RestoreError) FailedStage() progress.Stage  { return e.Stage }

func checkRestoreCtx(ctx context.Context, stage progress.Stage) *RestoreError {
	if err := ctx.Err(); err != nil {
		return &RestoreError{Stage: stage, Err: err}
	}
	return nil
}

// Restore verifies a backup archive against its manifest checksum,
// extracts it, confirms the restored disk chain is consistent, and places
// it as a new, non-destructively-named .vmwarevm bundle -- never
// overwriting the source. See ADR-004
// (docs/superpowers/specs/2026-09-11-restore-design.md) for the full
// design. Restore never touches the source VM -- ctrl is used only for
// CheckDiskConsistency against the restored copy.
func Restore(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts RestoreOptions) (*RestoreResult, error) {
	if reporter == nil {
		reporter = progress.NoOpReporter{}
	}

	if opts.TargetDir == "" && opts.VMXPath == "" {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("one of TargetDir or VMXPath is required")}
	}
	if opts.TargetDir != "" && opts.VMXPath != "" {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("TargetDir and VMXPath are mutually exclusive")}
	}
	if runErr := checkRestoreCtx(ctx, progress.Verifying); runErr != nil {
		return nil, runErr
	}

	archive, err := FindArchive(opts.Destination, opts.ArchiveID)
	if err != nil {
		return nil, &RestoreError{Stage: progress.Verifying, Err: err}
	}

	ext := "tar.gz"
	if archive.Manifest.Compression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(opts.Destination, archive.ArchiveID, "archive."+ext)

	reporter.Report(progress.Event{Stage: progress.Verifying, Message: "verifying archive checksum"})
	if err := verifyChecksum(archivePath, archive.Manifest.SHA256, func(cumulative int64) {
		reporter.Report(progress.Event{Stage: progress.Verifying, Percent: percentOf(cumulative, archive.Manifest.SizeBytes)})
	}); err != nil {
		return nil, &RestoreError{Stage: progress.Verifying, Err: err}
	}

	if runErr := checkRestoreCtx(ctx, progress.Extracting); runErr != nil {
		return nil, runErr
	}

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+opts.ArchiveID)

	reporter.Report(progress.Event{Stage: progress.Extracting, Message: "extracting archive"})
	// archive.Manifest.SizeBytes is the compressed size, an approximation
	// for extraction's (uncompressed) total -- same clamped-at-1 tolerance
	// percentOf already documents for Run's own Compressing stage.
	onWrite := func(cumulative int64) {
		reporter.Report(progress.Event{Stage: progress.Extracting, Percent: percentOf(cumulative, archive.Manifest.SizeBytes)})
	}
	if err := extractArchive(archivePath, stagingDir, archive.Manifest.Compression, onWrite); err != nil {
		return nil, &RestoreError{Stage: progress.Extracting, Err: err}
	}

	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	bundleBase, bundleDir, err := singleTopLevelEntry(stagingDir)
	if err != nil {
		return nil, &RestoreError{Stage: progress.Extracting, Err: err}
	}

	if runErr := checkRestoreCtx(ctx, progress.CheckingDiskConsistency); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.CheckingDiskConsistency, Message: "checking restored disk consistency"})
	vmxPath, err := findVMX(bundleDir)
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	diskFiles, err := readDiskFiles(vmxPath)
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	if err := checkDisksConsistent(ctrl, bundleDir, diskFiles); err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: fmt.Errorf("restored disk consistency check failed -- the archive itself may be damaged; the extracted copy at %s has been preserved for inspection: %w", stagingDir, err)}
	}

	if runErr := checkRestoreCtx(ctx, progress.Placing); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Placing, Message: "placing restored bundle"})

	targetParent := opts.TargetDir
	if targetParent == "" {
		targetParent = filepath.Dir(filepath.Dir(opts.VMXPath))
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	targetName, err := restoreTargetName(targetParent, bundleBase, now())
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.Placing, Err: err}
	}
	targetPath := filepath.Join(targetParent, targetName)

	if err := placeBundle(bundleDir, targetPath); err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.Placing, Err: err}
	}

	reporter.Report(progress.Event{Stage: progress.Done, Message: "restore complete"})
	return &RestoreResult{ArchiveID: archive.ArchiveID, TargetPath: targetPath, Manifest: archive.Manifest}, nil
}

// countingHasher wraps a hash.Hash, invoking onWrite with the running
// cumulative byte count as it's written to -- lets verifyChecksum drive
// Verifying's Percent the same per-chunk way tarTo/copyDir do for their
// own stages.
type countingHasher struct {
	h          hash.Hash
	onWrite    func(cumulative int64)
	cumulative int64
}

func (c *countingHasher) Write(p []byte) (int, error) {
	n, err := c.h.Write(p)
	c.cumulative += int64(n)
	if c.onWrite != nil {
		c.onWrite(c.cumulative)
	}
	return n, err
}

// verifyChecksum streams path through SHA-256, comparing the result
// against want (lowercase hex). If onRead is non-nil, it's invoked with
// the running cumulative bytes read.
func verifyChecksum(path, want string, onRead func(cumulative int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	ch := &countingHasher{h: sha256.New(), onWrite: onRead}
	if _, err := io.Copy(ch, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	got := fmt.Sprintf("%x", ch.h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
}

// singleTopLevelEntry returns the base name (with ".vmwarevm" stripped)
// and full path of stagingDir's one and only child -- the .vmwarevm
// bundle directory extractArchive just produced. createArchive's tarTo
// always wraps exactly one top-level entry (the staged bundle dir, see
// choreography.go's stagedBundle/stagingRoot split), so anything other
// than exactly one directory here means the archive is malformed.
func singleTopLevelEntry(stagingDir string) (bundleBase, bundleDir string, err error) {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return "", "", fmt.Errorf("read extracted staging dir: %w", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", "", fmt.Errorf("extracted archive has %d top-level entries, want exactly one directory", len(entries))
	}
	name := entries[0].Name()
	return strings.TrimSuffix(name, ".vmwarevm"), filepath.Join(stagingDir, name), nil
}

// findVMX returns the path of the single .vmx file directly inside
// bundleDir.
func findVMX(bundleDir string) (string, error) {
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return "", fmt.Errorf("read bundle dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".vmx") {
			return filepath.Join(bundleDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .vmx file found in %s", bundleDir)
}

// placeBundle moves src to dst via rename, falling back to a recursive
// copy + remove on a cross-device rename error (EXDEV) -- StagingDir may
// not share a volume with dst's parent.
func placeBundle(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !errors.Is(linkErr.Err, syscall.EXDEV) {
		return fmt.Errorf("place bundle: %w", err)
	}
	if err := copyDir(src, dst, nil); err != nil {
		return fmt.Errorf("copy bundle cross-device: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove staged bundle after cross-device copy: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -v`
Expected: PASS — every test in the package, including the new `TestRestore_*`/`TestResult_Summary`/`TestRestoreResult_Summary`/`TestRestoreError_*` cases and every pre-existing test (`Run`'s own suite must be untouched).

- [ ] **Step 5: Commit**

```bash
git add internal/backup/restore.go internal/backup/restore_test.go internal/backup/choreography.go
git commit -m "feat(backup): add Restore pipeline (verify, extract, check disk, place)"
```

---

## Task 5: Generalize `internal/tui.Model` for run + restore

**Files:**
- Create: `internal/tui/interfaces.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/run.go`
- Create: `internal/tui/restore.go`
- Modify: `internal/tui/model_test.go` (mechanical rename only)
- Modify: `internal/tui/view_test.go` (mechanical rename only)
- Test: `internal/tui/restore_test.go` (new, mirrors `run_test.go`)

**Interfaces:**
- Consumes: `*backup.Result`, `*backup.RunError` (now `Summary()`/`FailedStage()`-bearing, Task 4/1), `*backup.RestoreResult`, `*backup.RestoreError` (Task 4).
- Produces: `pipelineResult{Summary() string}`, `pipelineError{error; FailedStage() progress.Stage}` (unexported interfaces); `RunInteractive` (signature unchanged); new `RestoreInteractive(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error), extraOpts ...tea.ProgramOption) (*backup.RestoreResult, error)`.

- [ ] **Step 1: Write the failing test for `RestoreInteractive`**

Create `internal/tui/restore_test.go`:

```go
package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestRestoreInteractive_Success_ReturnsResult(t *testing.T) {
	var out bytes.Buffer
	want := &backup.RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		r.Report(progress.Event{Stage: progress.Verifying, Message: "verifying archive checksum"})
		r.Report(progress.Event{Stage: progress.Done, Message: "restore complete"})
		return want, nil
	}

	got, err := RestoreInteractive(&out, "myvm-x", func() {}, restoreFn, tea.WithInput(strings.NewReader("")))
	if err != nil {
		t.Fatalf("RestoreInteractive() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("RestoreInteractive() result = %v, want %v", got, want)
	}
	if !strings.Contains(out.String(), "restore complete: /vms/myvm - backup 2026-09-11.vmwarevm") {
		t.Errorf("output = %q, want the completion line rendered", out.String())
	}
	if !strings.Contains(out.String(), "snapback restore myvm-x") {
		t.Errorf("output = %q, want the restore header rendered", out.String())
	}
}

func TestRestoreInteractive_Failure_ReturnsError(t *testing.T) {
	var out bytes.Buffer
	wantErr := &backup.RestoreError{Stage: progress.CheckingDiskConsistency, Err: errors.New("disk check failed")}

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		r.Report(progress.Event{Stage: progress.CheckingDiskConsistency, Message: "checking restored disk consistency"})
		return nil, wantErr
	}

	_, err := RestoreInteractive(&out, "myvm-x", func() {}, restoreFn, tea.WithInput(strings.NewReader("")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("RestoreInteractive() error = %v, want it to be (or wrap) %v", err, wantErr)
	}
	if !strings.Contains(out.String(), "✗ checking disk consistency") {
		t.Errorf("output = %q, want checking-disk-consistency marked failed", out.String())
	}
}

func TestRestoreInteractive_ProgramQuitsBeforeResult_ReturnsError(t *testing.T) {
	var out bytes.Buffer

	ctx, restoreCancel := context.WithCancel(context.Background())
	defer restoreCancel()
	restoreDone := make(chan struct{})

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		<-ctx.Done()
		close(restoreDone)
		return nil, ctx.Err()
	}
	cancel := func() { restoreCancel() }
	quitEarly := func(_ tea.Model, _ tea.Msg) tea.Msg { return tea.QuitMsg{} }

	got, err := RestoreInteractive(&out, "myvm-x", cancel, restoreFn, tea.WithInput(strings.NewReader("")), tea.WithFilter(quitEarly))
	if !errors.Is(err, ErrInteractiveRunIncomplete) {
		t.Fatalf("RestoreInteractive() error = %v, want ErrInteractiveRunIncomplete", err)
	}
	if got != nil {
		t.Errorf("RestoreInteractive() result = %v, want nil", got)
	}
	select {
	case <-restoreDone:
	default:
		t.Error("restoreFn had not returned by the time RestoreInteractive returned")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/... -run TestRestoreInteractive -v`
Expected: FAIL with `undefined: RestoreInteractive`.

- [ ] **Step 3: Implement the generalization**

Create `internal/tui/interfaces.go`:

```go
package tui

import "github.com/xortim/snapback/internal/progress"

// pipelineResult is implemented by *backup.Result and *backup.RestoreResult
// -- the two outcome types run.go's and restore.go's public entry points
// return. Decouples Model from importing internal/backup's concrete types
// directly.
type pipelineResult interface {
	Summary() string
}

// pipelineError is implemented by *backup.RunError and *backup.RestoreError
// -- both carry the progress.Stage active when their pipeline failed.
type pipelineError interface {
	error
	FailedStage() progress.Stage
}
```

Replace `internal/tui/model.go`:

```go
// Package tui renders a backup or restore pipeline's progress as an
// interactive bubbletea checklist for a real terminal, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md and
// docs/superpowers/specs/2026-09-11-restore-design.md. It depends on
// internal/progress (the Event vocabulary) and internal/backup (only for
// the plain result/error data types run.go/restore.go's exported entry
// points return) -- never the reverse. Choreography code stays free of
// any rendering import.
package tui

import (
	"context"
	"errors"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

type stageStatus int

const (
	pending stageStatus = iota
	active
	done
	failed
)

type stageRow struct {
	stage   progress.Stage
	status  stageStatus
	message string
}

// Model is a bubbletea model rendering one pipeline run's progress.
// Normal callers only interact with it via RunInteractive/
// RestoreInteractive.
type Model struct {
	header     string
	cancel     context.CancelFunc
	rows       []stageRow
	barStages  []progress.Stage
	percent    float64
	showBar    bool
	start      time.Time
	elapsed    time.Duration
	result     pipelineResult
	err        error
	finished   bool
	cancelling bool
}

func newModel(header string, cancel context.CancelFunc, stages, barStages []progress.Stage) Model {
	rows := make([]stageRow, len(stages))
	for i, s := range stages {
		rows[i] = stageRow{stage: s, status: pending}
	}
	return Model{
		header:    header,
		cancel:    cancel,
		rows:      rows,
		barStages: barStages,
		start:     time.Now(),
	}
}

type eventMsg progress.Event

type resultMsg struct {
	result pipelineResult
	err    error
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC && !m.finished {
			m.cancelling = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	case tickMsg:
		if m.finished {
			return m, nil
		}
		m.elapsed = time.Since(m.start)
		return m, tickCmd()
	case eventMsg:
		m.applyEvent(progress.Event(msg))
		return m, nil
	case resultMsg:
		m.result = msg.result
		m.err = msg.err
		m.finished = true
		m.applyFinalStatus()
		return m, tea.Quit
	}
	return m, nil
}

// applyEvent updates rows in place for a live progress.Event: every stage
// before e.Stage in the fixed display order is marked done, e.Stage itself
// becomes active, and a Percent-bearing event updates the bar without
// clearing that stage's last Message.
func (m *Model) applyEvent(e progress.Event) {
	idx := -1
	for i, row := range m.rows {
		if row.stage == e.Stage {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	for i := 0; i < idx; i++ {
		if m.rows[i].status != failed {
			m.rows[i].status = done
		}
	}
	m.rows[idx].status = active
	if e.Message != "" {
		m.rows[idx].message = e.Message
	}
	if slices.Contains(m.barStages, e.Stage) {
		m.showBar = true
		if e.Message == "" {
			m.percent = e.Percent
		}
	}
}

// applyFinalStatus marks every row done (success) or the row matching the
// failing error's Stage as failed, called once when resultMsg arrives.
func (m *Model) applyFinalStatus() {
	if m.err == nil {
		for i := range m.rows {
			m.rows[i].status = done
		}
		return
	}
	var perr pipelineError
	if errors.As(m.err, &perr) {
		stage := perr.FailedStage()
		for i := range m.rows {
			if m.rows[i].stage == stage {
				m.rows[i].status = failed
				m.rows[i].message = perr.Error()
				return
			}
		}
	}
	if len(m.rows) > 0 {
		m.rows[0].status = failed
		m.rows[0].message = m.err.Error()
	}
}
```

Replace `internal/tui/view.go`:

```go
package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	bprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/xortim/snapback/internal/style"
)

const barWidth = 40

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", m.header)

	for _, row := range m.rows {
		b.WriteString(renderRow(row))
		b.WriteString("\n")
		if row.status == active && m.showBar && slices.Contains(m.barStages, row.stage) {
			bar := bprogress.New(bprogress.WithDefaultGradient())
			bar.Width = barWidth
			b.WriteString("  " + bar.ViewAs(m.percent) + "\n")
		}
	}

	fmt.Fprintf(&b, "\nelapsed: %s\n", m.elapsed.Round(time.Second))

	if m.cancelling && !m.finished {
		b.WriteString(style.Degraded.Render("cancelling... (waiting for the current step to finish)") + "\n")
	}
	if m.finished {
		if m.err == nil && m.result != nil {
			b.WriteString(style.Done.Render(m.result.Summary()) + "\n")
		} else if m.err == nil {
			b.WriteString(style.Done.Render("complete") + "\n")
		} else {
			b.WriteString(style.Failed.Render(fmt.Sprintf("error: %v", m.err)) + "\n")
		}
	}
	return b.String()
}

func renderRow(row stageRow) string {
	icon, style := iconFor(row.status)
	line := icon + " " + row.stage.String()
	if row.message != "" {
		line += " - " + row.message
	}
	return style.Render(line)
}

func iconFor(s stageStatus) (string, lipgloss.Style) {
	switch s {
	case done:
		return "✓", style.Done
	case active:
		return "◐", style.Active
	case failed:
		return "✗", style.Failed
	default:
		return "○", style.Pending
	}
}
```

Replace `internal/tui/run.go`:

```go
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// ErrInteractiveRunIncomplete is returned by RunInteractive/
// RestoreInteractive when the bubbletea program exits (e.g. via its own
// SIGINT/SIGTERM handling) before ever processing a resultMsg -- unlike
// every other error these return, no "error: <err>" line was ever
// rendered to out, so callers must not treat this the same as an error
// the TUI already displayed (see internal/cli/run.go's use of errors.Is
// here).
var ErrInteractiveRunIncomplete = errors.New("interactive run ended before the pipeline finished")

// runStages is the fixed, display-order subset of progress.Stage values
// backup.Run actually reports today (Pruning/Notifying exist as Stage
// constants for future phases but aren't emitted yet, so they're left off
// this checklist rather than shown permanently pending).
var runStages = []progress.Stage{
	progress.CheckingTools,
	progress.Snapshotting,
	progress.Copying,
	progress.Merging,
	progress.Compressing,
	progress.Checksumming,
}

var runBarStages = []progress.Stage{progress.Copying, progress.Compressing}

// newRunModel builds a Model configured for run's header/stage
// list/bar stages -- the shape newModel had before this file's
// generalization. model_test.go and view_test.go call this (instead of
// newModel directly) so their assertions are unaffected by newModel's
// wider signature.
func newRunModel(vmName string, cancel context.CancelFunc) Model {
	return newModel(fmt.Sprintf("snapback run --vm %s", vmName), cancel, runStages, runBarStages)
}

// RunInteractive renders backupFn's progress as an interactive checklist
// written to out, and returns whatever backupFn returns. cancel is invoked
// if the user presses ctrl+c before the run finishes -- the caller is
// responsible for wiring cancel to the same context.Context backupFn's
// underlying backup.Run call actually respects (run.go does this via
// context.WithCancel(cmd.Context())). extraOpts is exposed purely for
// tests, to pass tea.WithInput on a non-terminal reader; production
// callers should leave it empty so bubbletea reads real keypresses
// (ctrl+c) from the real stdin.
func RunInteractive(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error), extraOpts ...tea.ProgramOption) (*backup.Result, error) {
	pipelineFn := func(r progress.Reporter) (pipelineResult, error) {
		result, err := backupFn(r)
		if result == nil {
			return nil, err
		}
		return result, err
	}
	header := fmt.Sprintf("snapback run --vm %s", vmName)
	res, err := runInteractivePipeline(out, header, cancel, runStages, runBarStages, pipelineFn, extraOpts...)
	if res == nil {
		return nil, err
	}
	result, ok := res.(*backup.Result)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T from interactive run", res)
	}
	return result, err
}

// runInteractivePipeline is the generalized internals RunInteractive and
// RestoreInteractive both wrap, supplying their own header/stage
// list/bar stages. pipelineFn runs the actual backup/restore against
// reporter, returning nil (not a nil-valued concrete pointer wrapped in a
// non-nil interface) on error.
func runInteractivePipeline(out io.Writer, header string, cancel context.CancelFunc, stages, barStages []progress.Stage, pipelineFn func(progress.Reporter) (pipelineResult, error), extraOpts ...tea.ProgramOption) (pipelineResult, error) {
	opts := append([]tea.ProgramOption{tea.WithOutput(out)}, extraOpts...)
	program := tea.NewProgram(newModel(header, cancel, stages, barStages), opts...)
	reporter := NewReporter(program)

	done := make(chan struct{})
	go func() {
		result, err := pipelineFn(reporter)
		program.Send(resultMsg{result: result, err: err})
		close(done)
	}()

	finalModel, err := program.Run()
	if err != nil {
		cancel()
		<-done
		return nil, err
	}
	m, ok := finalModel.(Model)
	if !ok {
		cancel()
		<-done
		return nil, fmt.Errorf("unexpected model type %T from bubbletea program", finalModel)
	}
	if !m.finished {
		cancel()
		<-done
		return nil, ErrInteractiveRunIncomplete
	}
	<-done
	return m.result, m.err
}
```

Create `internal/tui/restore.go`:

```go
package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// restoreStages is the fixed, display-order subset of progress.Stage
// values backup.Restore reports.
var restoreStages = []progress.Stage{
	progress.Verifying,
	progress.Extracting,
	progress.CheckingDiskConsistency,
	progress.Placing,
}

var restoreBarStages = []progress.Stage{progress.Verifying, progress.Extracting}

// RestoreInteractive renders restoreFn's progress as an interactive
// checklist written to out, mirroring RunInteractive's shape for restore.
// label is shown in the header ("snapback restore <label>") -- callers
// pass whatever identifies the restore being run (an archive ID, or a VM
// name for the --vm/--latest path).
func RestoreInteractive(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error), extraOpts ...tea.ProgramOption) (*backup.RestoreResult, error) {
	pipelineFn := func(r progress.Reporter) (pipelineResult, error) {
		result, err := restoreFn(r)
		if result == nil {
			return nil, err
		}
		return result, err
	}
	header := fmt.Sprintf("snapback restore %s", label)
	res, err := runInteractivePipeline(out, header, cancel, restoreStages, restoreBarStages, pipelineFn, extraOpts...)
	if res == nil {
		return nil, err
	}
	result, ok := res.(*backup.RestoreResult)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T from interactive restore", res)
	}
	return result, err
}
```

Update `internal/tui/model_test.go` and `internal/tui/view_test.go` — every call site is the same 2-arg `newModel("myvm", <cancelFunc>)` shape, so a mechanical rename to `newRunModel` (defined above) leaves every existing assertion unchanged:

```bash
sed -i '' 's/newModel(/newRunModel(/g' internal/tui/model_test.go internal/tui/view_test.go
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS — every existing `model_test.go`/`run_test.go`/`view_test.go` test plus the new `TestRestoreInteractive_*` tests. Also run `go build ./...` to catch any stale import (e.g. `view.go` no longer imports `internal/progress` since the hardcoded `Copying`/`Compressing` check was replaced by `m.barStages`).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/interfaces.go internal/tui/model.go internal/tui/view.go internal/tui/run.go internal/tui/restore.go internal/tui/restore_test.go internal/tui/model_test.go internal/tui/view_test.go
git commit -m "refactor(tui): generalize Model to render both run and restore progress"
```

---

## Task 6: `snapback restore` CLI command

**Files:**
- Create: `internal/cli/restore.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/restore_internal_test.go` (package `cli`, mirrors `run_internal_test.go`)

**Interfaces:**
- Consumes: `backup.Restore`, `backup.RestoreOptions`, `backup.FindArchive`, `backup.LatestArchiveForVM` (Task 3/4); `tui.RestoreInteractive` (Task 5); `loadConfigForCmd`, `findVMConfig`, `defaultVMCmdDeps`, `defaultIsTerminal` (existing, `config_load.go`/`run.go`/`deps.go`).
- Produces: `newRestoreCmd()`, wired into `root.go`.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/restore_internal_test.go`:

```go
// Package cli (internal test package, not cli_test) so this file can call
// newRestoreCmdWithDeps directly and inject a fake config loader and
// vm.Controller -- mirrors run_internal_test.go.
package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

func newTestRestoreRoot(t *testing.T, deps restoreDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "restore", newRestoreCmdWithDeps(deps))
}

// writeFixtureArchive writes a real backup archive (via backup.Run against
// a fake controller) to destination, for restore CLI tests that need a
// resolvable archive-id or --vm/--latest match. Returns the archive ID.
func writeFixtureArchive(t *testing.T, destination, vmName string) (archiveID string) {
	t.Helper()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	result, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      vmName,
		VMXPath:     vmxPath,
		Destination: destination,
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}
	return result.ArchiveID
}

func TestRestoreCmd_NoSelector_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error when neither archive-id nor --vm/--latest is given")
	}
}

func TestRestoreCmd_BothSelectors_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", "myvm-x", "--vm", "myvm", "--latest"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error when both an archive-id and --vm/--latest are given")
	}
}

func TestRestoreCmd_VMWithoutLatest_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", "--vm", "myvm"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for --vm without --latest")
	}
}

func TestRestoreCmd_MissingVMInConfig_RequiresDest(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil // no VMs configured
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", archiveID})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when the archive's VM isn't in config and --dest is absent")
	}
	if !strings.Contains(err.Error(), "myvm") {
		t.Errorf("Execute() error = %v, want it to name the missing VM", err)
	}
}

func TestRestoreCmd_HappyPath_ArchiveID_PrintsTargetPath(t *testing.T) {
	destination := t.TempDir()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	result, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName: "myvm", VMXPath: vmxPath, Destination: destination, Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination, VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	root.SetArgs([]string{"restore", result.ArchiveID})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want it to report restore completion", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
}

func TestRestoreCmd_HappyPath_VMLatest_PrintsTargetPath(t *testing.T) {
	destination := t.TempDir()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	if _, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName: "myvm", VMXPath: vmxPath, Destination: destination, Compression: "gzip",
	}); err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination, VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	root.SetArgs([]string{"restore", "--vm", "myvm", "--latest"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want it to report restore completion", out.String())
	}
}

func TestRestoreCmd_DestOverride_SkipsConfigLookup(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")
	dest := t.TempDir()

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil // no VMs configured -- --dest must make this unnecessary
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", archiveID, "--dest", dest})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), dest) {
		t.Errorf("stdout = %q, want the restored path under --dest %q", out.String(), dest)
	}
}

func TestRestoreCmd_InteractiveTerminal_UsesRestoreInteractiveAndSkipsPlainPrint(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")
	dest := t.TempDir()

	var calledWithLabel string
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
		isTerminal:    func(io.Writer) bool { return true },
		restoreInteractive: func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error) {
			calledWithLabel = label
			return restoreFn(progress.NoOpReporter{})
		},
	})
	root.SetArgs([]string{"restore", archiveID, "--dest", dest})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if calledWithLabel != archiveID {
		t.Errorf("restoreInteractive called with label = %q, want %q", calledWithLabel, archiveID)
	}
	if strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want no plain \"restore complete\" line -- the interactive renderer owns that", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/... -run TestRestoreCmd -v`
Expected: FAIL with `undefined: restoreDeps` / `undefined: newRestoreCmdWithDeps`.

- [ ] **Step 3: Implement**

Create `internal/cli/restore.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
	"github.com/xortim/snapback/internal/vm"
)

// restoreDeps groups restore's external dependencies -- shares
// loadConfig/newController's shape with runDeps but is its own struct
// (mirrors run.go's own reasoning for not reusing vmCmdDeps): restore
// needs isTerminal/restoreInteractive, not run's isTerminal/runInteractive.
type restoreDeps struct {
	loadConfig         func(path string) (*config.Config, error)
	newController      func() (vm.Controller, error)
	isTerminal         func(w io.Writer) bool
	restoreInteractive func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error)
}

func defaultRestoreDeps() restoreDeps {
	base := defaultVMCmdDeps()
	return restoreDeps{
		loadConfig:    base.loadConfig,
		newController: base.newController,
		isTerminal:    defaultIsTerminal,
		restoreInteractive: func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error) {
			return tui.RestoreInteractive(out, label, cancel, restoreFn)
		},
	}
}

func newRestoreCmd() *cobra.Command {
	return newRestoreCmdWithDeps(defaultRestoreDeps())
}

func newRestoreCmdWithDeps(deps restoreDeps) *cobra.Command {
	var vmName, dest string
	var latest bool

	cmd := &cobra.Command{
		Use:   "restore [archive-id]",
		Short: "Restore a backup archive",
		Long:  "Verify, extract, and place a backup archive as a new, non-destructively-named .vmwarevm bundle next to the source -- never overwriting anything. Either an archive-id positional argument or --vm with --latest must be given, not both.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag/arg validation this RunE performs itself returns errors
			// that should still print usage -- cmd.SilenceUsage is set only
			// after validateRestoreSelectors passes, mirroring run.go's
			// split between flag-misuse errors and operational errors.
			var archiveID string
			if len(args) == 1 {
				archiveID = args[0]
			}
			if err := validateRestoreSelectors(archiveID, vmName, latest); err != nil {
				return err
			}
			cmd.SilenceUsage = true
			return restoreArchive(cmd, deps, archiveID, vmName, latest, dest)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to restore the latest archive for, as configured")
	cmd.Flags().BoolVar(&latest, "latest", false, "restore the newest archive for --vm")
	cmd.Flags().StringVar(&dest, "dest", "", "parent directory to place the restored bundle in (required if the archive's VM isn't in the current config)")

	return cmd
}

// validateRestoreSelectors enforces "exactly one of archive-id or --vm
// with --latest" per ADR-004.
func validateRestoreSelectors(archiveID, vmName string, latest bool) error {
	haveArchiveID := archiveID != ""
	haveVMLatest := vmName != "" && latest
	switch {
	case haveArchiveID && (vmName != "" || latest):
		return fmt.Errorf("archive-id and --vm/--latest are mutually exclusive")
	case !haveArchiveID && vmName != "" && !latest:
		return fmt.Errorf("--vm requires --latest")
	case !haveArchiveID && latest && vmName == "":
		return fmt.Errorf("--latest requires --vm")
	case !haveArchiveID && !haveVMLatest:
		return fmt.Errorf("exactly one of an archive-id argument or --vm with --latest is required")
	}
	return nil
}

func restoreArchive(cmd *cobra.Command, deps restoreDeps, archiveID, vmName string, latest bool, dest string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	resolvedArchiveID := archiveID
	label := archiveID
	if vmName != "" && latest {
		archive, err := backup.LatestArchiveForVM(cfg.Destination, vmName)
		if err != nil {
			return fmt.Errorf("resolve latest archive for %q: %w", vmName, err)
		}
		resolvedArchiveID = archive.ArchiveID
		label = vmName
	}

	opts := backup.RestoreOptions{
		ArchiveID:   resolvedArchiveID,
		Destination: cfg.Destination,
	}

	if dest != "" {
		opts.TargetDir = dest
	} else {
		lookupName := vmName
		if lookupName == "" {
			archive, err := backup.FindArchive(cfg.Destination, resolvedArchiveID)
			if err != nil {
				return fmt.Errorf("resolve archive %q: %w", resolvedArchiveID, err)
			}
			lookupName = archive.Manifest.VMName
		}
		vmCfg, ok := findVMConfig(cfg.VMs, lookupName)
		if !ok {
			return fmt.Errorf("VM %q not found in config %s -- pass --dest to restore without it", lookupName, configPath)
		}
		opts.VMXPath = vmCfg.VMX
	}

	out := cmd.OutOrStdout()

	if deps.isTerminal != nil && deps.isTerminal(out) && deps.restoreInteractive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
			return backup.Restore(ctx, ctrl, r, opts)
		}
		_, err := deps.restoreInteractive(out, label, cancel, restoreFn)
		if err != nil {
			if !errors.Is(err, tui.ErrInteractiveRunIncomplete) {
				cmd.SilenceErrors = true
			}
			return err
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := backup.Restore(cmd.Context(), ctrl, reporter, opts)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "restore complete: %s\n", result.TargetPath)
	return nil
}
```

In `internal/cli/root.go`, add `newRestoreCmd()` to the `root.AddCommand(...)` list:

```go
	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
		newCleanupCmd(),
		newVMCmd(),
		newRestoreCmd(),
	)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -v`
Expected: PASS — every `TestRestoreCmd_*` case plus every pre-existing `internal/cli` test (in particular `root_test.go`, which may enumerate registered subcommands and would need `restore` added to its expected list if so — check it before assuming untouched).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/restore.go internal/cli/restore_internal_test.go internal/cli/root.go
git commit -m "feat(cli): wire snapback restore command"
```

---

## Final Verification

- [ ] **Run the full suite and lint**

```bash
make lint
make test
make build
```

Expected: all green. `make test` also regenerates `coverage.out` — skim it for `internal/backup/restore.go`, `internal/backup/extract.go`, and `internal/tui/restore.go` to confirm the new code paths (checksum mismatch, disk-consistency failure preserving staging, collision suffixing, cross-device `placeBundle` fallback if exercisable, CLI validation branches) are actually covered, not just compiled.

- [ ] **Manual smoke test against a real Fusion VM** (per `CLAUDE.md`'s integration-test convention — needs a real, disposable scratch VM, not CI)

```bash
SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
snapback run --vm <scratch-vm>
snapback restore --vm <scratch-vm> --latest
# open the restored "<scratch-vm> - backup <date>.vmwarevm" in Fusion by hand and confirm it boots
```

- [ ] **Update `docs/design.md`'s Roadmap**

Mark Phase 3 (Restore) as landed, and note Phase 2 (Scheduling) is next up per the epic's own stated ordering.

- [ ] **Close out GitHub issues**

Once each task's PR merges, its corresponding sub-issue (#71–#75) closes; close epic #70 once all five are done.
