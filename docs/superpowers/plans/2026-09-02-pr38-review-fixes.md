# PR #38 Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every finding from the `/code-review 38` report on `feat/cleanup-command` (PR #38, the `cleanup` command).

**Architecture:** An OS-level advisory file lock (`internal/backup.AcquireLock`, backed by `flock(2)` via `golang.org/x/sys/unix`) serializes `run` and `cleanup` per VM, closing the race where `cleanup` can delete a snapshot a concurrent `run` still owns. A new `vm.Controller.DeleteSnapshots` batch method resolves every requested snapshot's UID from one `vmcli Snapshot query` call instead of one per name. `internal/cli`'s `runDeps`/`cleanupDeps` duplication collapses into a single `vmCmdDeps` type (as a type alias, so no test call site changes) plus one shared flag-registration helper. Each finding is fixed in place with a matching unit test, in small commits on top of the existing `feat/cleanup-command` branch (PR #38 is already open against it).

**Tech Stack:** Go 1.26.5 (`go.mod`), standard `testing` package, `github.com/spf13/cobra`, `golang.org/x/sys/unix` (already an indirect dependency via `fsnotify`; this plan makes it direct).

**Spec:** `/code-review 38` findings (reproduced below).

```
1. internal/cli/cleanup.go:74 -- cleanup deletes every snapshot matching
   the "snapback-" prefix with no way to distinguish an orphan from a
   snapshot belonging to a currently in-progress run. A scheduled
   `snapback run --vm foo` between its Snapshot and DeleteSnapshot/merge
   steps can have its own live snapshot deleted out from under it by a
   concurrent `snapback cleanup --vm foo`, aborting or corrupting that
   backup. No lock/pid-file exists anywhere in the codebase to prevent
   this.
2. internal/cli/cleanup.go:18 -- cleanupDeps and
   newCleanupCmd/newCleanupCmdWithDeps duplicate run.go's runDeps and
   newRunCmd/newRunCmdWithDeps almost verbatim (identical fields,
   identical --vm flag + MarkFlagRequired panic pattern). A future fix to
   the shared scaffolding made in one place but not the other silently
   reintroduces a bug already fixed once.
3. internal/vm/vmcli.go:220 -- DeleteSnapshot re-resolves the snapshot
   name to a UID via a fresh querySnapshots shell-out on every call, even
   though cleanup.go's runCleanup already fetched the full snapshot list
   once via ListSnapshots just before looping over DeleteSnapshot.
   Cleaning up N orphaned snapshots costs 1+2N vmcli shell-outs instead
   of 1+N.
```

**Findings Disposition:**

| # | Finding | Disposition |
|---|---------|--------------|
| 3 | Redundant per-delete UID lookup | Task 1: new `Controller.DeleteSnapshots` batch method |
| 1 | `run`/`cleanup` snapshot race | Tasks 2-3 build the lock; Task 5 wires it into `cleanup` |
| 2 | Duplicated `runDeps`/`cleanupDeps` scaffolding | Task 4 |
| 1 (cleanup side) | `cleanup` uses the lock + the new batch delete | Task 5 |

## Global Constraints

- Go 1.26.5 toolchain; build/test with the `go` on `PATH`.
- Verification gate for each task: `go build ./...`, `go vet ./...`, `gofmt -l .`, and the relevant `go test` run. Run `make lint`, `make test`, and `make build` (this repo's own CI targets) at Final Verification.
- Test files in `internal/vm` are split `package vm` (white-box, `*_internal_test.go`) / `package vm_test` (black-box, `*_test.go`); `internal/backup` and `internal/cli` follow the same split. Match the existing file's package for any edit.
- Preserve all currently-passing behavior -- no task in this plan should regress an existing test.
- Commit messages follow this repo's convention: real Conventional Commit type (`fix`/`refactor`/`test`), package name as scope.
- `golang.org/x/sys` is already in `go.mod` as an indirect dependency (pulled in by `fsnotify`); after Task 2 imports it directly, run `go mod tidy` so it's promoted to a direct requirement with the correct version comment removed.

---

## File Structure

- Modify `internal/vm/controller.go` -- add `DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error)` to the `Controller` interface.
- Modify `internal/vm/fake.go` -- implement `FakeVMController.DeleteSnapshots` by looping the existing `DeleteSnapshot`.
- Modify `internal/vm/fake_test.go` -- cover the new method.
- Modify `internal/vm/vmcli.go` -- implement `VMCLIController.DeleteSnapshots`, resolving every name from one `querySnapshots` call.
- Modify `internal/vm/vmcli_internal_test.go` -- cover the new method, including the single-query assertion.
- Create `internal/backup/lock.go` -- `AcquireLock`/`(*Lock).Release`/`ErrLocked`, a non-blocking per-(destination, VM name) `flock(2)`-backed lock.
- Create `internal/backup/lock_test.go` -- cover acquire/release/contend/distinct-VM behavior.
- Modify `internal/backup/choreography.go` -- `Run` acquires the lock right after validating `Options`, releases it via `defer`.
- Modify `internal/backup/choreography_test.go` -- cover the lock-held-returns-error case and that a successful `Run` frees the lock again.
- Create `internal/cli/deps.go` -- `vmCmdDeps` (the deduplicated dependency struct), `defaultVMCmdDeps()`, `addRequiredVMFlag()`; `runDeps`/`cleanupDeps` become type aliases to it.
- Modify `internal/cli/run.go` -- use `defaultVMCmdDeps()`/`addRequiredVMFlag()`; drop the now-unused `vm` import.
- Modify `internal/cli/cleanup.go` -- use `defaultVMCmdDeps()`/`addRequiredVMFlag()`; compute the orphan list first, acquire the lock only when there's something to delete (skip gracefully if held), call the new batched `DeleteSnapshots` instead of looping `DeleteSnapshot`.
- Modify `internal/cli/cleanup_internal_test.go` -- add `Destination` to the two fixtures that now reach the lock; add coverage for lock-held-skips-without-error and for the single batched delete call.

## Task 1: `vm.Controller.DeleteSnapshots` -- resolve every UID from one query

**Files:**
- Modify: `internal/vm/controller.go`
- Modify: `internal/vm/vmcli.go`
- Modify: `internal/vm/fake.go`
- Test: `internal/vm/vmcli_internal_test.go`
- Test: `internal/vm/fake_test.go`

**Interfaces:**
- Produces: `Controller.DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error)` -- returns the subset of `names` actually removed; a failure on one name is joined into `err` via `errors.Join` (each error names the snapshot it's about) rather than aborting the rest.
- `Controller.DeleteSnapshot` (singular) is unchanged -- `internal/backup/choreography.go` keeps using it for its own single-snapshot merge step.

- [ ] **Step 1: Write the failing tests**

Add to `internal/vm/vmcli_internal_test.go`, after `TestVMCLIController_DeleteSnapshot_AmbiguousNameReturnsErrorWithoutDeleting`:

```go
func TestVMCLIController_DeleteSnapshots_ResolvesAllUIDsFromOneQuery(t *testing.T) {
	queryCalls := 0
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "query":
			queryCalls++
			return []byte(`{"currentUID":5,"helperUID":0,"snapshots":[
				{"displayName":"snapback-1","parentUID":0,"uid":3},
				{"displayName":"snapback-2","parentUID":3,"uid":5}
			]}`), nil, nil
		case args[1] == "Snapshot" && args[2] == "Delete":
			return nil, nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	deleted, err := c.DeleteSnapshots(testVMX, []string{"snapback-1", "snapback-2"})
	if err != nil {
		t.Fatalf("DeleteSnapshots() error = %v, want nil", err)
	}
	if len(deleted) != 2 {
		t.Errorf("DeleteSnapshots() deleted = %v, want both names", deleted)
	}
	if queryCalls != 1 {
		t.Errorf("Snapshot query called %d times, want exactly 1 for a 2-name batch", queryCalls)
	}
	var deleteUIDs []string
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			deleteUIDs = append(deleteUIDs, call[3])
		}
	}
	if len(deleteUIDs) != 2 {
		t.Errorf("Delete called %d times, want 2", len(deleteUIDs))
	}
}

func TestVMCLIController_DeleteSnapshots_PartialFailureReturnsDeletedAndJoinedError(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "query":
			return []byte(`{"currentUID":3,"helperUID":0,"snapshots":[{"displayName":"snapback-1","parentUID":0,"uid":3}]}`), nil, nil
		case args[1] == "Snapshot" && args[2] == "Delete":
			return nil, nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	deleted, err := c.DeleteSnapshots(testVMX, []string{"snapback-1", "does-not-exist"})
	if len(deleted) != 1 || deleted[0] != "snapback-1" {
		t.Errorf("DeleteSnapshots() deleted = %v, want only snapback-1", deleted)
	}
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("DeleteSnapshots() error = %v, want it to name the missing snapshot", err)
	}
}
```

Add to `internal/vm/fake_test.go`, after `TestFakeVMController_DeleteSnapshot_ReturnsInjectedErrorAndDoesNotMutate`:

```go
func TestFakeVMController_DeleteSnapshots_RemovesEachAndReportsMissing(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"
	if err := f.Snapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	deleted, err := f.DeleteSnapshots(vmx, []string{"snapback-1", "does-not-exist"})
	if len(deleted) != 1 || deleted[0] != "snapback-1" {
		t.Errorf("DeleteSnapshots() deleted = %v, want only snapback-1", deleted)
	}
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("DeleteSnapshots() error = %v, want it to name the missing snapshot", err)
	}
	remaining, lerr := f.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after DeleteSnapshots", remaining)
	}
}
```

`fake_test.go` needs `"strings"` added to its import block for the new test's `strings.Contains`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/vm/... -run 'DeleteSnapshots' -v`
Expected: FAIL with `undefined: DeleteSnapshots` / `c.DeleteSnapshots undefined` (method doesn't exist yet on either type).

- [ ] **Step 3: Add `DeleteSnapshots` to the `Controller` interface**

In `internal/vm/controller.go`, change:

```go
	Snapshot(vmxPath, name string) error
	ListSnapshots(vmxPath string) ([]string, error)
	DeleteSnapshot(vmxPath, name string) error
}
```

to:

```go
	Snapshot(vmxPath, name string) error
	ListSnapshots(vmxPath string) ([]string, error)
	DeleteSnapshot(vmxPath, name string) error
	// DeleteSnapshots removes every snapshot in names from vmxPath. An
	// implementation can resolve all of them from a single snapshot
	// listing instead of one lookup per name (see vmcli.go's
	// implementation) -- internal/cli's cleanup command uses this instead
	// of looping DeleteSnapshot, since it already knows the exact set of
	// orphaned names to remove up front. It returns the subset of names
	// actually removed; a failure on one name is joined into err rather
	// than aborting the rest, so a caller still removes everything it can
	// and reports what it couldn't.
	DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error)
}
```

- [ ] **Step 4: Implement `FakeVMController.DeleteSnapshots`**

In `internal/vm/fake.go`, change the import block:

```go
package vm

import "fmt"
```

to:

```go
package vm

import (
	"errors"
	"fmt"
)
```

Add this method after `DeleteSnapshot`:

```go
// DeleteSnapshots removes each snapshot in names, honoring
// DeleteSnapshotErr the same way DeleteSnapshot does. It doesn't
// short-circuit on the first failure -- callers rely on getting every
// name it could remove even when some fail.
func (f *FakeVMController) DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error) {
	var errs []error
	for _, name := range names {
		if delErr := f.DeleteSnapshot(vmxPath, name); delErr != nil {
			errs = append(errs, fmt.Errorf("remove snapshot %q: %w", name, delErr))
			continue
		}
		deleted = append(deleted, name)
	}
	return deleted, errors.Join(errs...)
}
```

- [ ] **Step 5: Implement `VMCLIController.DeleteSnapshots`**

In `internal/vm/vmcli.go`, change the import block:

```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)
```

to:

```go
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)
```

Add this method after `DeleteSnapshot`:

```go
// DeleteSnapshots satisfies Controller.DeleteSnapshots
// (internal/vm/controller.go): unlike calling DeleteSnapshot once per
// name, this resolves every uid from a single Snapshot query call, so
// cleaning up N orphaned snapshots costs one query plus N deletes
// instead of N queries plus N deletes.
func (c *VMCLIController) DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error) {
	if len(names) == 0 {
		return nil, nil
	}
	snapshots, qerr := c.querySnapshots(vmxPath)
	if qerr != nil {
		return nil, qerr
	}
	uidsByName := make(map[string][]int64, len(snapshots))
	for _, s := range snapshots {
		uidsByName[s.DisplayName] = append(uidsByName[s.DisplayName], s.UID)
	}

	var errs []error
	for _, name := range names {
		uids := uidsByName[name]
		switch len(uids) {
		case 0:
			errs = append(errs, fmt.Errorf("snapshot %q not found for %q", name, vmxPath))
			continue
		case 1:
			// exactly one match -- proceed to delete below
		default:
			errs = append(errs, fmt.Errorf("snapshot name %q is ambiguous: %d snapshots on %q share this display name", name, len(uids), vmxPath))
			continue
		}
		_, stderr, delErr := c.run(vmxPath, "Snapshot", "Delete", strconv.FormatInt(uids[0], 10))
		if delErr != nil {
			errs = append(errs, vmcliError(fmt.Sprintf("delete snapshot %q", name), stderr, delErr))
			continue
		}
		deleted = append(deleted, name)
	}
	return deleted, errors.Join(errs...)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/vm/... -run 'DeleteSnapshots' -v`
Expected: PASS

- [ ] **Step 7: Run the full package test suite to check for regressions**

Run: `go test ./internal/vm/...`
Expected: PASS -- including `var _ Controller = (*VMCLIController)(nil)` and `var _ vm.Controller = (*vm.FakeVMController)(nil)` compiling, which forces both types to actually implement the new method.

- [ ] **Step 8: Commit**

```bash
git add internal/vm/controller.go internal/vm/vmcli.go internal/vm/fake.go internal/vm/vmcli_internal_test.go internal/vm/fake_test.go
git commit -m "feat(vm): add DeleteSnapshots to resolve every UID from one query"
```

## Task 2: `backup.AcquireLock` -- per-VM advisory file lock

**Files:**
- Create: `internal/backup/lock.go`
- Test: `internal/backup/lock_test.go`

**Interfaces:**
- Produces: `ErrLocked error`, `AcquireLock(destination, vmName string) (*Lock, error)`, `(*Lock).Release() error` in package `backup`. `AcquireLock` is non-blocking: if another process already holds the lock for the same `(destination, vmName)` pair, it returns an error wrapping `ErrLocked` immediately instead of waiting.
- Consumed by Task 3 (`internal/backup/choreography.go`) and Task 5 (`internal/cli/cleanup.go`).

- [ ] **Step 1: Write the failing tests**

Create `internal/backup/lock_test.go`:

```go
package backup_test

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/backup"
)

func TestAcquireLock_SecondAcquireForSameVMFailsWithErrLocked(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = backup.AcquireLock(dest, "myvm")
	if !errors.Is(err, backup.ErrLocked) {
		t.Errorf("second AcquireLock() error = %v, want it to wrap ErrLocked", err)
	}
}

func TestAcquireLock_DistinctVMNamesDoNotContend(t *testing.T) {
	dest := t.TempDir()
	lockA, err := backup.AcquireLock(dest, "vm-a")
	if err != nil {
		t.Fatalf("AcquireLock(vm-a) error = %v, want nil", err)
	}
	defer func() { _ = lockA.Release() }()

	lockB, err := backup.AcquireLock(dest, "vm-b")
	if err != nil {
		t.Fatalf("AcquireLock(vm-b) error = %v, want nil (different VM, must not contend)", err)
	}
	defer func() { _ = lockB.Release() }()
}

func TestAcquireLock_ReleaseFreesTheLockForReacquisition(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}

	lock2, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after Release() error = %v, want nil", err)
	}
	_ = lock2.Release()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestAcquireLock -v`
Expected: FAIL with `undefined: backup.AcquireLock` (doesn't exist yet).

- [ ] **Step 3: Implement the lock**

Create `internal/backup/lock.go`:

```go
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrLocked is returned (wrapped) by AcquireLock when another process
// already holds the lock for this destination+VM pair -- most likely a
// concurrent `run` or `cleanup` invocation against the same VM.
var ErrLocked = errors.New("backup lock already held")

// Lock is an exclusive, advisory, per-VM file lock used to serialize
// `run` and `cleanup` against the same VM's snapshots. Run (choreography.go)
// holds it for the whole choreography, from before Snapshot through after
// DeleteSnapshot; cleanup (internal/cli/cleanup.go) takes it before
// deleting anything, so the two can never touch the same VM's snapshots
// at the same time. Backed by flock(2): if the holding process dies, the
// kernel releases the lock automatically when its file descriptor
// closes, so a crashed run can never leave a stale lock behind.
type Lock struct {
	f *os.File
}

// lockPath returns the fixed lock-file path for a given destination and
// VM name -- both run and cleanup derive the same path from the same
// config, so they contend on the same file regardless of which command
// gets there first.
func lockPath(destination, vmName string) string {
	return filepath.Join(destination, ".snapback-locks", vmName+".lock")
}

// AcquireLock takes an exclusive, non-blocking lock for vmName under
// destination. If another process already holds it, this returns
// immediately with an error wrapping ErrLocked instead of waiting.
func AcquireLock(destination, vmName string) (*Lock, error) {
	path := lockPath(destination, vmName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %q", ErrLocked, vmName)
		}
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release unlocks and closes the lock file. Safe to call at most once
// per successful AcquireLock (typically via defer).
func (l *Lock) Release() error {
	defer func() { _ = l.f.Close() }()
	return unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
}
```

- [ ] **Step 4: Add the direct dependency and run tests to verify they pass**

```bash
go mod tidy
go test ./internal/backup/... -run TestAcquireLock -v
```

Expected: `go.mod` now lists `golang.org/x/sys` as a direct requirement (no `// indirect` comment); the test run PASSes.

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/lock.go internal/backup/lock_test.go go.mod go.sum
git commit -m "feat(backup): add a per-VM advisory file lock"
```

## Task 3: `backup.Run` -- hold the lock for the whole choreography

**Files:**
- Modify: `internal/backup/choreography.go`
- Test: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: `backup.AcquireLock(destination, vmName string) (*Lock, error)`, `backup.ErrLocked` (Task 2).
- `Run`'s signature and `Result`/`Options` are unchanged. New behavior: `Run` now returns a `*RunError` tagged `Stage: progress.CheckingTools` if it can't acquire the per-VM lock (e.g. `cleanup` holds it, or another `run` for the same VM is already in flight).

- [ ] **Step 1: Write the failing tests**

Add to `internal/backup/choreography_test.go`, after `TestRun_EmptyDestination_ReturnsError`:

```go
func TestRun_LockAlreadyHeld_ReturnsErrorWithoutTouchingVM(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}

	destination := t.TempDir()
	lock, err := backup.AcquireLock(destination, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: destination,
	}

	_, err = backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)
	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run() error = %v, want a *RunError", err)
	}
	if runErr.Stage != progress.CheckingTools {
		t.Errorf("RunError.Stage = %v, want %v", runErr.Stage, progress.CheckingTools)
	}
	if !errors.Is(err, backup.ErrLocked) {
		t.Errorf("Run() error = %v, want it to wrap backup.ErrLocked", err)
	}
	snapshots, lerr := fake.ListSnapshots(vmxPath)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want no snapshot taken while the lock was held", snapshots)
	}
}

func TestRun_HappyPath_ReleasesLockForReacquisition(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "disk.vmdk"), []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	destination := t.TempDir()
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: destination,
		StagingDir:  t.TempDir(),
	}

	if _, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	lock, err := backup.AcquireLock(destination, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after Run() error = %v, want nil (Run must release its lock)", err)
	}
	_ = lock.Release()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run 'TestRun_LockAlreadyHeld|TestRun_HappyPath_ReleasesLockForReacquisition' -v`
Expected: `TestRun_LockAlreadyHeld_ReturnsErrorWithoutTouchingVM` FAILs -- `Run()` currently ignores locking entirely and proceeds to take a snapshot. `TestRun_HappyPath_ReleasesLockForReacquisition` currently PASSes already (nothing to release yet); it's here to pin the behavior once Step 3 adds locking, and must still pass afterward.

- [ ] **Step 3: Acquire and release the lock in `Run`**

In `internal/backup/choreography.go`, change:

```go
	if opts.Destination == "" {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("destination is required")}
	}
	vmxInfo, err := os.Stat(opts.VMXPath)
```

to:

```go
	if opts.Destination == "" {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("destination is required")}
	}

	lock, err := AcquireLock(opts.Destination, opts.VMName)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("acquire backup lock: %w", err)}
	}
	defer func() { _ = lock.Release() }()

	vmxInfo, err := os.Stat(opts.VMXPath)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run 'TestRun_LockAlreadyHeld|TestRun_HappyPath_ReleasesLockForReacquisition' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/backup/...`
Expected: PASS -- every existing `TestRun_*` test still passes since each already supplies a fresh `t.TempDir()` `Destination`, so no two tests contend for the same lock file.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "fix(backup): hold a per-VM lock for the whole Run choreography"
```

## Task 4: `internal/cli` -- collapse `runDeps`/`cleanupDeps` into one `vmCmdDeps`

**Files:**
- Create: `internal/cli/deps.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/cleanup.go`

**Interfaces:**
- Produces: `vmCmdDeps` struct, `defaultVMCmdDeps() vmCmdDeps`, `addRequiredVMFlag(cmd *cobra.Command, vmName *string, usage string)` in package `cli`.
- `runDeps` and `cleanupDeps` become `type runDeps = vmCmdDeps` / `type cleanupDeps = vmCmdDeps` (type aliases, not new named types) -- every existing `runDeps{...}` / `cleanupDeps{...}` struct literal in `run_internal_test.go` and `cleanup_internal_test.go` keeps compiling unchanged, since an alias is the same type, not a different one.
- No behavior change; this is a pure refactor, so there is no new externally-visible behavior to test. The gate is that every existing test in `internal/cli` still passes.

- [ ] **Step 1: Confirm the starting point compiles and tests pass**

Run: `go build ./... && go test ./internal/cli/...`
Expected: PASS.

- [ ] **Step 2: Create `internal/cli/deps.go`**

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

// vmCmdDeps groups the external dependencies shared by every subcommand
// that operates on a single named VM (run, cleanup): a config loader and
// a vm.Controller factory, both swappable in tests for a fake instead of
// touching the real filesystem or requiring a Fusion install. run.go and
// cleanup.go each keep their own name for this (runDeps, cleanupDeps) as
// type aliases below -- same shape, so the struct and its default/flag
// plumbing live here once instead of twice.
type vmCmdDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

// runDeps and cleanupDeps are aliases (not distinct types), so existing
// call sites and tests can keep referring to a command-specific name
// without duplicating vmCmdDeps's fields.
type runDeps = vmCmdDeps
type cleanupDeps = vmCmdDeps

// defaultVMCmdDeps returns the real, production dependencies: config.Load
// and a real vm.VMCLIController.
func defaultVMCmdDeps() vmCmdDeps {
	return vmCmdDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	}
}

// addRequiredVMFlag registers the --vm flag on cmd into *vmName with
// usage as its help text, and marks it required. Shared by every
// single-VM subcommand (run, cleanup) since MarkFlagRequired's own error
// only fires for a typo'd flag name -- a programmer error this package
// wants to catch immediately as a panic, not thread through as a runtime
// error from some future `--vm` invocation.
func addRequiredVMFlag(cmd *cobra.Command, vmName *string, usage string) {
	cmd.Flags().StringVar(vmName, "vm", "", usage)
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}
}
```

- [ ] **Step 3: Simplify `run.go`**

In `internal/cli/run.go`, change the whole top of the file, from `package cli` through the end of `newRunCmdWithDeps`:

```go
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// runDeps groups run's external dependencies so tests can substitute a
// fake config loader and vm.Controller instead of touching the real
// filesystem or requiring a Fusion install.
type runDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(runDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	})
}

func newRunCmdWithDeps(deps runDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runVM itself returns --
			// flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runVM(cmd, deps, vmName)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to back up, as configured")
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}

	return cmd
}
```

to:

```go
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
)

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(defaultVMCmdDeps())
}

func newRunCmdWithDeps(deps runDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runVM itself returns --
			// flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runVM(cmd, deps, vmName)
		},
	}
	addRequiredVMFlag(cmd, &vmName, "name of the VM to back up, as configured")

	return cmd
}
```

(Note the dropped `"github.com/xortim/snapback/internal/vm"` import -- nothing left in `run.go` names `vm.Controller` directly once `runDeps` is an alias defined in `deps.go`.)

- [ ] **Step 4: Simplify `cleanup.go`**

In `internal/cli/cleanup.go`, change the top of the file, from `package cli` through the end of `newCleanupCmdWithDeps`:

```go
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

// cleanupDeps groups cleanup's external dependencies so tests can
// substitute a fake config loader and vm.Controller instead of touching
// the real filesystem or requiring a Fusion install.
type cleanupDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

func newCleanupCmd() *cobra.Command {
	return newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	})
}

func newCleanupCmdWithDeps(deps cleanupDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove orphaned snapback snapshots",
		Long:  "Find and remove any snapshot on the named VM matching the snapback-<timestamp> naming pattern -- leftovers from a run that died between the snapshot and delete-snapshot choreography steps.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runCleanup itself
			// returns -- flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runCleanup(cmd, deps, vmName)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to clean up, as configured")
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}

	return cmd
}
```

to:

```go
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
)

func newCleanupCmd() *cobra.Command {
	return newCleanupCmdWithDeps(defaultVMCmdDeps())
}

func newCleanupCmdWithDeps(deps cleanupDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove orphaned snapback snapshots",
		Long:  "Find and remove any snapshot on the named VM matching the snapback-<timestamp> naming pattern -- leftovers from a run that died between the snapshot and delete-snapshot choreography steps.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runCleanup itself
			// returns -- flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runCleanup(cmd, deps, vmName)
		},
	}
	addRequiredVMFlag(cmd, &vmName, "name of the VM to clean up, as configured")

	return cmd
}
```

(Same reasoning: `config` and `vm` are no longer named directly in `cleanup.go`. Leave `runCleanup` itself untouched here -- Task 5 rewrites its body.)

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go build ./... && go test ./internal/cli/... -v`
Expected: PASS -- every existing `TestRunCmd_*` and `TestCleanupCmd_*` test still passes unchanged, since `runDeps{...}`/`cleanupDeps{...}` literals in the test files resolve to the same `vmCmdDeps` struct either way.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/deps.go internal/cli/run.go internal/cli/cleanup.go
git commit -m "refactor(cli): collapse runDeps/cleanupDeps into one vmCmdDeps"
```

## Task 5: `cleanup.go` -- use the lock and the batched delete

**Files:**
- Modify: `internal/cli/cleanup.go` (`runCleanup`)
- Test: `internal/cli/cleanup_internal_test.go`

**Interfaces:**
- Consumes: `backup.AcquireLock`/`backup.ErrLocked` (Task 2), `Controller.DeleteSnapshots` (Task 1).
- `runCleanup`'s signature is unchanged. New behavior: if nothing matches the `snapback-` prefix, it reports "no orphaned snapshots found" without ever touching the lock (unchanged from before). If there's at least one match and the per-VM lock is already held (a `run` or another `cleanup` is active for this VM), it reports that and returns `nil` -- no error, since this isn't a failure, just deferring to whoever holds the lock. Otherwise it acquires the lock, deletes every match in one `DeleteSnapshots` call, and reports each one actually removed.

- [ ] **Step 1: Write the failing tests**

In `internal/cli/cleanup_internal_test.go`, update the two existing tests that will now reach the lock so they supply a `Destination` (a real lock file needs somewhere to live). Change:

```go
func TestCleanupCmd_RemovesOnlySnapbackPrefixedSnapshots(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := fake.Snapshot(vmx, "manual-checkpoint"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
```

to:

```go
func TestCleanupCmd_RemovesOnlySnapbackPrefixedSnapshots(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := fake.Snapshot(vmx, "manual-checkpoint"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
```

And change:

```go
func TestCleanupCmd_DeleteFailure_ReturnsWrappedError(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	fake.DeleteSnapshotErr = errBoom

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
```

to:

```go
func TestCleanupCmd_DeleteFailure_ReturnsWrappedError(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	fake.DeleteSnapshotErr = errBoom

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
```

Then add two new tests at the end of the file:

```go
func TestCleanupCmd_LockHeld_SkipsWithoutErrorAndDoesNotDelete(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: dest, VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil (a held lock is skipped, not an error)", err)
	}
	if !strings.Contains(out.String(), "in progress") {
		t.Errorf("stdout = %q, want a message about a backup in progress", out.String())
	}

	remaining, lerr := fake.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 1 {
		t.Errorf("ListSnapshots() = %v, want the snapshot untouched while the lock was held", remaining)
	}
}

func TestCleanupCmd_MultipleOrphans_DeletesInOneBatchCall(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := fake.Snapshot(vmx, "snapback-20260102T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if strings.Count(out.String(), "removed orphaned snapshot") != 2 {
		t.Errorf("stdout = %q, want both orphans reported removed", out.String())
	}
	remaining, lerr := fake.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 0 {
		t.Errorf("ListSnapshots() = %v, want both orphans removed", remaining)
	}
}
```

`cleanup_internal_test.go` needs `"github.com/xortim/snapback/internal/backup"` added to its import block for `backup.AcquireLock` in the new lock-held test.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/... -run 'TestCleanupCmd_LockHeld|TestCleanupCmd_MultipleOrphans' -v`
Expected: `TestCleanupCmd_LockHeld_SkipsWithoutErrorAndDoesNotDelete` FAILs -- `runCleanup` currently ignores the lock entirely and deletes the snapshot regardless. `TestCleanupCmd_MultipleOrphans_DeletesInOneBatchCall` currently PASSes already (the old per-name loop also reports and removes both) -- it's here to pin that behavior through the rewrite in Step 3.

- [ ] **Step 3: Rewrite `runCleanup`**

In `internal/cli/cleanup.go`, change:

```go
func runCleanup(cmd *cobra.Command, deps cleanupDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	snapshots, err := ctrl.ListSnapshots(vmCfg.VMX)
	if err != nil {
		return fmt.Errorf("list snapshots for %q: %w", vmName, err)
	}

	out := cmd.OutOrStdout()
	var deleteErrs []error
	found := false
	for _, name := range snapshots {
		if !strings.HasPrefix(name, backup.SnapshotPrefix) {
			continue
		}
		found = true
		if err := ctrl.DeleteSnapshot(vmCfg.VMX, name); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("remove orphaned snapshot %q: %w", name, err))
			continue
		}
		if _, err := fmt.Fprintf(out, "removed orphaned snapshot %q from %q\n", name, vmName); err != nil {
			return err
		}
	}

	if len(deleteErrs) > 0 {
		return fmt.Errorf("cleanup %q: %w", vmName, errors.Join(deleteErrs...))
	}

	if !found {
		_, err := fmt.Fprintf(out, "no orphaned snapshots found for %q\n", vmName)
		return err
	}

	return nil
}
```

to:

```go
func runCleanup(cmd *cobra.Command, deps cleanupDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	snapshots, err := ctrl.ListSnapshots(vmCfg.VMX)
	if err != nil {
		return fmt.Errorf("list snapshots for %q: %w", vmName, err)
	}

	var matching []string
	for _, name := range snapshots {
		if strings.HasPrefix(name, backup.SnapshotPrefix) {
			matching = append(matching, name)
		}
	}

	out := cmd.OutOrStdout()
	if len(matching) == 0 {
		_, err := fmt.Fprintf(out, "no orphaned snapshots found for %q\n", vmName)
		return err
	}

	// Only lock once there's actually something to delete: a `run` for
	// this VM holds this same lock for its whole choreography (see
	// backup.AcquireLock's doc comment), so a snapshot can never be
	// deleted out from under it. If the lock is held, defer to whoever
	// holds it rather than fail -- this isn't a cleanup error, just
	// something to retry later.
	lock, err := backup.AcquireLock(cfg.Destination, vmCfg.Name)
	if err != nil {
		if errors.Is(err, backup.ErrLocked) {
			_, ferr := fmt.Fprintf(out, "backup for %q may be in progress (lock held); skipping cleanup\n", vmName)
			return ferr
		}
		return fmt.Errorf("acquire cleanup lock for %q: %w", vmName, err)
	}
	defer func() { _ = lock.Release() }()

	deleted, delErr := ctrl.DeleteSnapshots(vmCfg.VMX, matching)
	for _, name := range deleted {
		if _, err := fmt.Fprintf(out, "removed orphaned snapshot %q from %q\n", name, vmName); err != nil {
			return err
		}
	}
	if delErr != nil {
		return fmt.Errorf("cleanup %q: %w", vmName, delErr)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run 'TestCleanupCmd_LockHeld|TestCleanupCmd_MultipleOrphans' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS -- every existing `TestCleanupCmd_*` test still passes, including `TestCleanupCmd_NoOrphans_ReportsNoneFound` (never reaches the lock, since `matching` is empty), `TestCleanupCmd_RemovesOnlySnapbackPrefixedSnapshots`, and `TestCleanupCmd_DeleteFailure_ReturnsWrappedError` (both updated in Step 1 to supply a `Destination`).

- [ ] **Step 6: Commit**

```bash
git add internal/cli/cleanup.go internal/cli/cleanup_internal_test.go
git commit -m "fix(cli): serialize cleanup against an in-progress run and batch its deletes"
```

---

## Final Verification

- [ ] **Run the full suite one more time end to end, via this repo's own CI targets**

```bash
make lint
make test
make build
```

Expected: all green. At this point every finding from `/code-review 38` is fixed:
1. `run`/`cleanup` snapshot race (Tasks 2, 3, 5) -- an advisory per-VM lock now serializes them.
2. Duplicated `runDeps`/`cleanupDeps` scaffolding (Task 4) -- collapsed into one `vmCmdDeps` plus a shared flag helper.
3. Redundant per-delete UID lookup (Tasks 1, 5) -- `cleanup` now resolves every orphan's UID from a single `vmcli` query.

- [ ] **Push the fix commits to the existing PR branch**

```bash
git push origin feat/cleanup-command
```

Expected: PR #38 picks up the new commits automatically (same branch, no new PR needed).
