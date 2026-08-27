# PR #22 Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 4 findings from the `/code-review` of PR #22 ("Implement backup choreography against the fake VMController") without changing `Run()`'s public signature or behavior on the happy path.

**Architecture:** All 4 fixes land as additional commits on the existing `feat/backup-choreography` branch, in the existing worktree at `.claude/worktrees/feat+backup-choreography` — this is review remediation on an open PR (#22), not new feature work, so it does not get a new branch. Each task is a small, independently testable diff to `internal/backup/archive.go`, `internal/backup/manifest.go`, or `internal/backup/choreography.go`, following the package's existing TDD-with-`vm.FakeVMController` pattern (no real VMware Fusion install needed to test any of this).

**Tech Stack:** Go 1.26.5, standard library only for these fixes (`os`, `os/exec`, `fmt`), package `backup` under `github.com/xortim/snapback/internal/backup`, tests in `package backup_test` using `go test`.

**Spec:** This plan implements review findings, not a design doc section. The findings are recorded verbatim in the PR #22 code-review conversation; the underlying behavior being hardened is `docs/design.md` § "Backup choreography". No new spec doc.

## Global Constraints

- Work happens in the existing worktree: `/Users/tim/workspace/snapback/.claude/worktrees/feat+backup-choreography`, branch `feat/backup-choreography`. Do not create a new branch.
- Go 1.26.5, module `github.com/xortim/snapback`.
- Every task must leave `go test ./...`, `make lint`, and `make build` green (per Makefile targets) — these are the same 3 checks PR #22's CI runs (`lint`, `test (go 1.26)`, `build`).
- Match existing test conventions in `internal/backup/*_test.go`: `package backup_test`, `t.TempDir()` for all scratch paths, `vm.NewFakeVMController()` for the controller, table-free one-scenario-per-test style, `t.Fatalf` for setup/precondition failures and `t.Errorf` for assertion failures.
- Do not use permission-bit manipulation (`chmod 0000`, etc.) to force test failures — the codebase has already hit and deliberately avoided this (see the comment on `TestRun_CopyDirPartialFailure_CleansUpStagingDir` in `choreography_test.go`: root bypasses permission checks in some CI environments, making such tests flaky). Use deterministic, root-proof failure triggers instead (`ENOTDIR`-style conflicts, file-removal side effects, etc.), following the same pattern already used in that test.
- No new dependencies. No new exported API on the `backup` package.

---

## File Structure

No new files. All changes are to existing files:

- `internal/backup/archive.go` — fix stderr-swallowing bug in `tarToZstd` (Task 1); harden the archive file's permission bits (Task 2).
- `internal/backup/manifest.go` — harden `manifest.json`'s permission bits (Task 2).
- `internal/backup/choreography.go` — harden the output directory's permission bits (Task 2); move `readGuestOS` earlier (Task 3); add a `VMXPath`-is-a-directory pre-flight check (Task 4).
- `internal/backup/archive_test.go` — new test for Task 1.
- `internal/backup/choreography_test.go` — new tests for Tasks 2, 3, 4.

Tasks 3 and 4 both touch `Run()`'s pre-flight validation block in `choreography.go`, in that order — Task 4's diff is written against the code as it stands after Task 3's edit.

---

### Task 1: Stop swallowing zstd's stderr when the tar-side write also fails

**Problem:** In `tarToZstd` (`internal/backup/archive.go:70-97`), when `zstd` exits early (crashes, gets killed, hits an internal error) while a large backup is still being tar'd, the pipe it's reading from fills and the in-progress write in `tarTo` fails first with a generic "broken pipe" `io.Copy` error. `tarErr != nil` is checked before `waitErr`, so that generic broken-pipe error is returned and `zstd`'s actual stderr output (the real reason it died) is discarded — exactly the diagnosability problem this PR's commit `351a525` ("Capture zstd stderr in archive errors") already claims to have fixed, for the specific case where the tar side also errors.

**Files:**
- Modify: `internal/backup/archive.go:84-97` (function `tarToZstd`)
- Test: `internal/backup/archive_test.go`

**Interfaces:**
- Consumes: nothing new — same `tarToZstd(srcDir string, out io.Writer) error` signature and `lookZstd` test seam already in `archive.go`.
- Produces: no interface change. `createArchive`'s behavior is unchanged on success; only the *error message* changes when both `zstd` and the tar-side write fail.

- [ ] **Step 1: Write the failing test**

Add to `internal/backup/archive_test.go` (place it after `TestCreateArchive_ZstdFailureIncludesStderr`):

```go
func TestCreateArchive_ZstdFailureDuringTarWrite_IncludesStderr(t *testing.T) {
	// Simulate zstd dying immediately (before draining stdin) while a large
	// backup is still being tar'd into it -- realistic for multi-GB VM
	// disks. The fake zstd script below exits right away without reading
	// stdin, so once the OS pipe buffer fills, tarTo's write to stdin fails
	// with a broken-pipe error *before* cmd.Wait() returns. The bug this
	// guards against: that broken-pipe error was returned as-is, discarding
	// zstd's real stderr diagnosis of why it died.
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "zstd")
	script := "#!/bin/sh\necho 'zstd: forced early exit failure abc789' >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture script needs to be executable
		t.Fatalf("write fake zstd script: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srcDir := t.TempDir()
	// Larger than any realistic OS pipe buffer (typically 64KB on Linux,
	// 16-64KB on macOS) so tarTo's write blocks until the reader is gone,
	// then fails with a broken-pipe error instead of completing cleanly.
	bigContent := bytes.Repeat([]byte("A"), 8*1024*1024)
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), bigContent, 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	_, err := createArchive(srcDir, destPath, "zstd")
	if err == nil {
		t.Fatal("createArchive() error = nil, want error from failing zstd")
	}
	const wantSubstr = "forced early exit failure abc789"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("createArchive() error = %q, want it to contain fake zstd's stderr output %q even though the tar-side write also failed with a broken pipe", err.Error(), wantSubstr)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestCreateArchive_ZstdFailureDuringTarWrite_IncludesStderr -v`
Expected: FAIL — the error returned is the generic broken-pipe/write error from `tarTo`, which does not contain `"forced early exit failure abc789"`.

- [ ] **Step 3: Fix `tarToZstd` to prioritize `zstd`'s own exit error and stderr**

In `internal/backup/archive.go`, replace the error-priority block in `tarToZstd` (currently lines 84-97):

```go
	tarErr := tarTo(srcDir, stdin)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if tarErr != nil {
		return tarErr
	}
	if closeErr != nil {
		return fmt.Errorf("close zstd stdin: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	return nil
```

with:

```go
	tarErr := tarTo(srcDir, stdin)
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestCreateArchive_ZstdFailureDuringTarWrite_IncludesStderr -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS — in particular `TestCreateArchive_ZstdFailureIncludesStderr` (the existing stderr test, which fails only via `waitErr`, must still pass) and `TestRun_HappyPath_ProducesArchiveAndManifest`.

- [ ] **Step 6: Commit**

```bash
cd /Users/tim/workspace/snapback/.claude/worktrees/feat+backup-choreography
git add internal/backup/archive.go internal/backup/archive_test.go
git commit -m "backup: prioritize zstd's own exit error over a tar-side broken pipe"
```

---

### Task 2: Harden final backup output permissions to match the staging copy

**Problem:** `copyDir` (`internal/backup/copy.go`) hardens the staging copy of VM disk data to `0700`/file-mode-preserved, but the *final* backup output is less restrictive: `choreography.go:106` creates `outputDir` with `os.MkdirAll(outputDir, 0o755)` (world-readable+executable), `archive.go:36` writes the archive via `os.Create(destPath)` (mode `0666` before umask, typically `0644`), and `manifest.go`'s `writeManifest` writes `manifest.json` with `os.WriteFile(path, data, 0o644)`. The data that ends up world-readable at rest (a full VM disk image plus a manifest describing it) is the same sensitivity as the staging copy — the policy is inverted for no reason.

**Files:**
- Modify: `internal/backup/choreography.go:106` (`os.MkdirAll(outputDir, ...)`)
- Modify: `internal/backup/archive.go:36` (`os.Create(destPath)`)
- Modify: `internal/backup/manifest.go` (`writeManifest`'s `os.WriteFile` call)
- Test: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: `backup.Run`'s existing `Result` (`OutputDir`, `ArchivePath`, `ManifestPath` fields) — no changes to `Result` or `Options`.
- Produces: no interface change. Only the on-disk permission bits of `Result.OutputDir`, `Result.ArchivePath`, and `Result.ManifestPath` change, from `0755`/`0644` to `0700`/`0600`.

- [ ] **Step 1: Write the failing test**

Add to `internal/backup/choreography_test.go` (place it after `TestRun_HappyPath_ProducesArchiveAndManifest`):

```go
func TestRun_OutputPermissions_MatchStagingHardening(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	result, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	outputInfo, err := os.Stat(result.OutputDir)
	if err != nil {
		t.Fatalf("Stat(OutputDir) error = %v", err)
	}
	if perm := outputInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("OutputDir perm = %o, want %o (matching copyDir's staging-directory hardening)", perm, 0o700)
	}

	archiveInfo, err := os.Stat(result.ArchivePath)
	if err != nil {
		t.Fatalf("Stat(ArchivePath) error = %v", err)
	}
	if perm := archiveInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("ArchivePath perm = %o, want %o", perm, 0o600)
	}

	manifestInfo, err := os.Stat(result.ManifestPath)
	if err != nil {
		t.Fatalf("Stat(ManifestPath) error = %v", err)
	}
	if perm := manifestInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("ManifestPath perm = %o, want %o", perm, 0o600)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestRun_OutputPermissions_MatchStagingHardening -v`
Expected: FAIL — `OutputDir` perm is `0755`, `ArchivePath` and `ManifestPath` perm is `0644` (both before umask stripping, which only removes bits and can't add the group/other bits back — so these mismatches are deterministic regardless of the test machine's umask).

- [ ] **Step 3: Harden `outputDir`'s permissions in `choreography.go`**

In `internal/backup/choreography.go:106`, change:

```go
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
```

to:

```go
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
```

- [ ] **Step 4: Harden the archive file's permissions in `archive.go`**

In `internal/backup/archive.go:36`, change:

```go
	out, err := os.Create(destPath)
```

to:

```go
	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
```

- [ ] **Step 5: Harden `manifest.json`'s permissions in `manifest.go`**

In `internal/backup/manifest.go`, in `writeManifest`, change:

```go
	if err := os.WriteFile(path, data, 0o644); err != nil {
```

to:

```go
	if err := os.WriteFile(path, data, 0o600); err != nil {
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestRun_OutputPermissions_MatchStagingHardening -v`
Expected: PASS

- [ ] **Step 7: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS — all existing tests, including the ones that read back `result.ArchivePath` and `result.ManifestPath` contents (e.g. `TestRun_HappyPath_ProducesArchiveAndManifest`), are unaffected since the test process owns the file either way.

- [ ] **Step 8: Commit**

```bash
cd /Users/tim/workspace/snapback/.claude/worktrees/feat+backup-choreography
git add internal/backup/choreography.go internal/backup/archive.go internal/backup/manifest.go internal/backup/choreography_test.go
git commit -m "backup: harden final output permissions to match staging (0700/0600)"
```

---

### Task 3: Read the guest OS before taking the snapshot

**Problem:** In `Run()` (`internal/backup/choreography.go`), `readGuestOS(opts.VMXPath)` (currently line 84) is called *after* `ctrl.Snapshot` has already succeeded and been confirmed via `ctrl.ListSnapshots`. If `readGuestOS` then fails — a pure `.vmx`-file-parsing operation with nothing to do with VM/snapshot state — `Run()` returns an error with a live `snapback-<timestamp>` snapshot left behind on the source VM, unnecessarily widening the window in which a failure orphans a snapshot (recoverable only via the separate `snapback cleanup` command per `docs/design.md`). Since `readGuestOS` only needs `opts.VMXPath`, which is already validated to exist before any `ctrl` call, it can run immediately after that validation and before any VM state is touched.

**Files:**
- Modify: `internal/backup/choreography.go` (`Run()`, pre-flight validation block and the removed call further down)
- Test: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: existing package-private `readGuestOS(vmxPath string) (string, error)` from `vmx.go` — signature unchanged.
- Produces: no interface change. `Manifest.GuestOS` is populated with the same value as before; only *when* it's read (and therefore what state is on disk if it fails) changes.

- [ ] **Step 1: Write the failing test**

This test proves the ordering by making a `vm.Controller`'s `Snapshot` method delete the `.vmx` file as a side effect. If `readGuestOS` still runs *after* `Snapshot` (today's bug), reading the now-deleted file fails and `Run()` errors. If `readGuestOS` runs *before* `Snapshot` (the fix), the file still exists when it's read and `Run()` succeeds.

Add to `internal/backup/choreography_test.go` (place it after `TestRun_HappyPath_ProducesArchiveAndManifest`):

```go
// vmxDeletingController wraps vm.FakeVMController and deletes the vmx
// file as a side effect of a successful Snapshot call. This is a
// deterministic, root-proof way to detect an ordering regression: it
// does not depend on permission bits (which root bypasses in some CI
// environments -- see TestRun_CopyDirPartialFailure_CleansUpStagingDir
// above), only on whether readGuestOS is called before or after
// ctrl.Snapshot.
type vmxDeletingController struct {
	*vm.FakeVMController
}

func (c *vmxDeletingController) Snapshot(vmxPath, name string) error {
	if err := c.FakeVMController.Snapshot(vmxPath, name); err != nil {
		return err
	}
	return os.Remove(vmxPath)
}

func TestRun_ReadsGuestOSBeforeSnapshot(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	ctrl := &vmxDeletingController{FakeVMController: fake}

	result, err := backup.Run(ctrl, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil; guestOS must be read from the vmx file before Snapshot() runs (this test's fake Snapshot deletes the vmx file afterward to catch ordering regressions)", err)
	}
	if result.Manifest.GuestOS != "ubuntu-64" {
		t.Errorf("Manifest.GuestOS = %q, want %q", result.Manifest.GuestOS, "ubuntu-64")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestRun_ReadsGuestOSBeforeSnapshot -v`
Expected: FAIL — `Run()` returns a "read guest OS" error because, at today's call site (after `Snapshot`), the vmx file has already been deleted by `vmxDeletingController.Snapshot`.

- [ ] **Step 3: Move `readGuestOS` before `ctrl.Snapshot`**

In `internal/backup/choreography.go`, remove this block from its current location (right after the `ListSnapshots`/`slices.Contains` check, before `stagingParent := ...`):

```go
	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("read guest OS: %w", err)
	}

```

and insert it immediately after the existing `VMXPath` validation (currently `if _, err := os.Stat(opts.VMXPath); err != nil { ... }`) and before the `now := opts.Now` block, so the top of `Run()` reads:

```go
	if _, err := os.Stat(opts.VMXPath); err != nil {
		return nil, fmt.Errorf("vmx path: %w", err)
	}

	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("read guest OS: %w", err)
	}

	now := opts.Now
```

`hostSync()` stays where it is (immediately before the staging copy) — only `readGuestOS` moves.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestRun_ReadsGuestOSBeforeSnapshot -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS — in particular `TestRun_CheckToolsStateError_AbortsBeforeSnapshot` and `TestRun_MissingVMXPath_ReturnsError` still pass since the `VMXPath` existence check still runs first.

- [ ] **Step 6: Commit**

```bash
cd /Users/tim/workspace/snapback/.claude/worktrees/feat+backup-choreography
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "backup: read guest OS before snapshotting to shrink the orphaned-snapshot window"
```

---

### Task 4: Reject a `VMXPath` that points at a directory during pre-flight validation

**Problem:** `Run()`'s pre-flight check (`internal/backup/choreography.go`, the `os.Stat(opts.VMXPath)` call) only checks that *something* exists at `VMXPath`; it doesn't check that it's a regular file. If `VMXPath` is misconfigured to point at a directory, `os.Stat` succeeds, and the failure only surfaces later as a low-level, confusing error out of `readGuestOS`'s file read (an `EISDIR`-family OS error surfacing as e.g. `"read guest OS: read vmx: read /path: is a directory"`) instead of a clean, actionable validation message at the same pre-flight point where `VMName`/`Destination` are already validated.

**Files:**
- Modify: `internal/backup/choreography.go` (`Run()`, `VMXPath` validation — this is the code as left by Task 3)
- Test: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no interface change. `Run()` still returns `(nil, error)` for a bad `VMXPath`; only the error's wrapping prefix changes for the directory case (from `"read guest OS: ..."` to `"vmx path: ..."`), and it now happens before any `ctrl` call.

- [ ] **Step 1: Write the failing test**

Add to `internal/backup/choreography_test.go` (place it after `TestRun_MissingVMXPath_ReturnsError`):

```go
func TestRun_VMXPathIsDirectory_ReturnsError(t *testing.T) {
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	vmxAsDir := t.TempDir() // exists, but is a directory -- a misconfigured VMXPath

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxAsDir,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for VMXPath pointing at a directory")
	}
	if !strings.HasPrefix(err.Error(), "vmx path") {
		t.Errorf("Run() error = %q, want it prefixed with %q from pre-flight validation, not a low-level file-read error surfacing later", err.Error(), "vmx path")
	}

	snapshots, _ := fake.ListSnapshots(vmxAsDir)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; validation should fail before touching ctrl", snapshots)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backup/... -run TestRun_VMXPathIsDirectory_ReturnsError -v`
Expected: FAIL — `Run()` does return a non-nil error (via `readGuestOS`'s directory-read failure, per Task 3's new call order), but that error's message starts with `"read guest OS"`, not `"vmx path"`, so the `strings.HasPrefix` assertion fails.

- [ ] **Step 3: Add the directory check**

In `internal/backup/choreography.go`, change the `VMXPath` validation (as left by Task 3):

```go
	if _, err := os.Stat(opts.VMXPath); err != nil {
		return nil, fmt.Errorf("vmx path: %w", err)
	}

	guestOS, err := readGuestOS(opts.VMXPath)
```

to:

```go
	vmxInfo, err := os.Stat(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("vmx path: %w", err)
	}
	if vmxInfo.IsDir() {
		return nil, fmt.Errorf("vmx path %q is a directory, want a regular file", opts.VMXPath)
	}

	guestOS, err := readGuestOS(opts.VMXPath)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backup/... -run TestRun_VMXPathIsDirectory_ReturnsError -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/... -v`
Expected: PASS — in particular `TestRun_MissingVMXPath_ReturnsError` (still hits the `os.Stat` error branch, unaffected) and `TestRun_ReadsGuestOSBeforeSnapshot` from Task 3.

- [ ] **Step 6: Commit**

```bash
cd /Users/tim/workspace/snapback/.claude/worktrees/feat+backup-choreography
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "backup: reject a VMXPath that points at a directory during pre-flight validation"
```

---

## Final Verification

After all 4 tasks:

- [ ] Run the full suite: `go test ./... -v` — all pass.
- [ ] Run `make lint` — 0 issues.
- [ ] Run `make build` — binary builds.
- [ ] Push the 4 new commits to `origin/feat/backup-choreography` to update PR #22 (`git push`), rather than opening a new PR.
