# PR #24 Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the five findings from the `/code-review 24` review of PR #24 ("Implement real VMController backed by vmcli", branch `feat/vmcli-controller`) without changing vmcli's observable CLI-invocation shape for the already-passing test cases.

**Architecture:** All changes live in `internal/vm` (the vmcli-backed `Controller` implementation) and `internal/backup` (the choreography that consumes it). The two correctness bugs (orphaned snapshot on cleanup failure, ambiguous snapshot name resolution) are fixed by making `VMCLIController.Snapshot`/`snapshotUID` more conservative and by teaching `internal/backup.Run` to distinguish "no snapshot was created" from "a snapshot might still exist" via a new sentinel error, `vm.ErrOrphanPossible`, checked with `errors.Is`. The manifest-correctness issue is fixed by tightening `mapToolsState`'s no-tools sentinel handling. The two test-quality issues are isolated fixes to `vmcli_integration_test.go`.

**Tech Stack:** Go 1.26.5, standard `testing` package, table-driven/fake-based unit tests already established in `internal/vm/vmcli_internal_test.go` and `internal/backup/choreography_test.go`.

**Spec:** No separate spec doc — this plan implements the five findings from the `/code-review 24` run against PR #24 (branch `feat/vmcli-controller`), reproduced per-task below.

## Global Constraints

- Go 1.26.5 (per CLAUDE.md) — multiple `%w` verbs in `fmt.Errorf` and `slices.Contains` are both available; use them instead of hand-rolled equivalents.
- Work happens directly on branch `feat/vmcli-controller` (already checked out) — PR #24 is open against `main`, so these are additional commits on the existing PR branch, not a new branch.
- Follow the existing test patterns exactly: `fakeRun` + `handler` closures in `internal/vm/vmcli_internal_test.go`, `vm.NewFakeVMController()` + sticky `*Err` fields in `internal/backup/choreography_test.go`. Do not introduce a new mocking approach.
- Run `go test ./internal/vm/... ./internal/backup/... -v` after every task; run `go vet ./...` before the final commit of the plan.
- Do not touch `internal/vm/vmcli_integration_test.go`'s `//go:build integration` tag or the `SNAPBACK_INTEGRATION`/`SNAPBACK_TEST_VMX` gating — those tests are correct as gated, only their body has bugs.

---

## File Structure

- Modify `internal/vm/vmcli.go`:
  - `mapToolsState` — stop treating the literal sentinel `"none"` as `ToolsInstalled`.
  - `snapshotUID` — detect and error on ambiguous (non-unique) display-name matches instead of returning the first one found.
  - New exported `var ErrOrphanPossible error` — signals that a failed `Snapshot()` could not confirm cleanup succeeded.
  - `Snapshot` — wrap `ErrOrphanPossible` when the post-failure lookup errors/is ambiguous, or when the cleanup `Delete` call itself fails.
- Modify `internal/vm/controller.go` — update the `Controller.Snapshot` doc comment to document the `ErrOrphanPossible` exception to the "error means no snapshot exists" contract.
- Modify `internal/vm/vmcli_internal_test.go` — add unit tests for the three behavior changes above.
- Modify `internal/backup/choreography.go` — in `Run`, when `ctrl.Snapshot` fails, tag the `RunError` at `Stage: progress.Snapshotting` (instead of `progress.CheckingTools`) when the error wraps `vm.ErrOrphanPossible`.
- Modify `internal/backup/choreography_test.go` — add a test asserting the `Stage: Snapshotting` tagging for an `ErrOrphanPossible`-wrapped `Snapshot` error.
- Modify `internal/vm/vmcli_integration_test.go`:
  - `sha256File` — hex-encode the digest instead of casting raw bytes to `string`.
  - Remove the hand-rolled `contains` helper; replace both call sites with `slices.Contains`.

No new files.

---

### Task 1: Fix `mapToolsState`'s no-tools sentinel handling

**Files:**
- Modify: `internal/vm/vmcli.go:99-108` (`mapToolsState`)
- Test: `internal/vm/vmcli_internal_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no signature change — `mapToolsState(t toolsQueryResult) ToolsState` behavior only.

**Finding:** `mapToolsState` treats any non-empty `installType` as `ToolsInstalled`. The no-tools-installed JSON shape from real vmcli is explicitly unconfirmed (per the function's own comment). vmrun's equivalent field is known to report the literal string `"none"` for a no-tools guest — if vmcli does the same, `mapToolsState` would wrongly return `ToolsInstalled`, and that gets written verbatim into `manifest.json`'s `tools_state` field (`internal/backup/manifest.go:25`, populated from `internal/backup/choreography.go:237`).

- [ ] **Step 1: Write the failing test**

Add to `internal/vm/vmcli_internal_test.go`, near the other `CheckToolsState` tests (after `TestVMCLIController_CheckToolsState_NoInstallTypeMapsToToolsUnknown`, line 72):

```go
func TestVMCLIController_CheckToolsState_NoneInstallTypeMapsToToolsUnknown(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"running":false,"runningStatus":"notRunning","installType":"none"}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.CheckToolsState(testVMX)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != ToolsUnknown {
		t.Errorf("CheckToolsState() = %q, want %q (\"none\" is a known no-tools sentinel, not an installed value)", got, ToolsUnknown)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/vm/... -run TestVMCLIController_CheckToolsState_NoneInstallTypeMapsToToolsUnknown -v`
Expected: FAIL — `got` is `ToolsInstalled`, not `ToolsUnknown`.

- [ ] **Step 3: Fix `mapToolsState`**

Replace `internal/vm/vmcli.go:99-108`:

```go
func mapToolsState(t toolsQueryResult) ToolsState {
	switch {
	case t.Running && t.RunningStatus == "running":
		return ToolsRunning
	case t.InstallType != "" && !strings.EqualFold(t.InstallType, "none"):
		return ToolsInstalled
	default:
		return ToolsUnknown
	}
}
```

`strings` is already imported in this file (used by `vmcliError`), no import changes needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/vm/... -v`
Expected: PASS — all `internal/vm` tests, including the new one and the three existing `CheckToolsState` tests (unaffected: `"ovt"` and `""` still map to `ToolsInstalled`/`ToolsUnknown` as before).

- [ ] **Step 5: Commit**

```bash
git add internal/vm/vmcli.go internal/vm/vmcli_internal_test.go
git commit -m "fix(vm): treat vmcli installType \"none\" as ToolsUnknown, not ToolsInstalled"
```

---

### Task 2: Fix `vmcli_integration_test.go` checksum encoding and duplicate `contains` helper

**Files:**
- Modify: `internal/vm/vmcli_integration_test.go`

**Interfaces:**
- Consumes: `slices.Contains` (stdlib, already used at `internal/backup/choreography.go:149`).
- Produces: no exported change — this file only builds under `-tags=integration` and isn't invoked by other tasks.

**Finding (checksum):** `sha256File` casts the raw 32-byte SHA-256 digest directly to a `string` instead of hex-encoding it. When `TestIntegration_FrozenBundleReadableDuringSnapshot` fails, `t.Errorf` prints `before`/`after` as unprintable binary data, destroying the diagnostic exactly when it's needed.

**Finding (duplication):** The file hand-rolls a `contains(items []string, want string) bool` helper duplicating `slices.Contains`, which the codebase already uses (`internal/backup/choreography.go:149`).

- [ ] **Step 1: Fix `sha256File` to hex-encode**

Add `"encoding/hex"` to the import block (`internal/vm/vmcli_integration_test.go:13-21`):

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xortim/snapback/internal/vm"
)
```

(This also adds `"slices"`, needed by Step 2.)

Replace `sha256File` (`internal/vm/vmcli_integration_test.go:125-136`):

```go
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

- [ ] **Step 2: Remove the duplicate `contains` helper and use `slices.Contains`**

Delete the `contains` function (`internal/vm/vmcli_integration_test.go:138-145`):

```go
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
```

Replace its two call sites:

At `internal/vm/vmcli_integration_test.go:68`:
```go
	if !slices.Contains(snapshots, name) {
```

At `internal/vm/vmcli_integration_test.go:80`:
```go
	if slices.Contains(snapshots, name) {
```

- [ ] **Step 3: Verify the file still builds under the integration tag**

This file is gated by `//go:build integration` and requires a real Fusion install + scratch VM to actually run, so it cannot be executed here. Verify it compiles instead:

Run: `go vet -tags=integration ./internal/vm/...`
Expected: no output (clean).

Also run the full non-integration suite to make sure nothing else broke:

Run: `go test ./internal/vm/... -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/vm/vmcli_integration_test.go
git commit -m "fix(vm): hex-encode integration test checksums, dedupe contains helper via slices.Contains"
```

---

### Task 3: Make `snapshotUID` reject ambiguous display names

**Files:**
- Modify: `internal/vm/vmcli.go:143-157` (`snapshotUID`)
- Test: `internal/vm/vmcli_internal_test.go`

**Interfaces:**
- Consumes: `querySnapshots(vmxPath string) ([]snapshotEntry, error)` (`internal/vm/vmcli.go:131`, unchanged).
- Produces: `snapshotUID(vmxPath, name string) (uid int64, found bool, err error)` — same signature, but now returns a non-nil `err` (with `found=false`, `uid=0`) when more than one snapshot shares `name`, instead of silently returning the first match. Task 4 depends on this new error path being distinguishable from "found" and from a genuine query error only by `err != nil` — it does not need to tell them apart, so no further signature change is needed.

**Finding:** vmcli/VMware snapshot display names are not guaranteed unique. `snapshotUID`'s linear first-match scan means `DeleteSnapshot` (and the cleanup path in `Snapshot`, fixed in Task 4) can silently target the wrong snapshot — e.g. an unretired orphan from a previous run sharing a same-second timestamp name with a new one.

- [ ] **Step 1: Write the failing test**

Add to `internal/vm/vmcli_internal_test.go`, after `TestVMCLIController_DeleteSnapshot_NotFoundReturnsErrorWithoutCallingDelete` (line 244):

```go
func TestVMCLIController_DeleteSnapshot_AmbiguousNameReturnsErrorWithoutDeleting(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		if args[1] == "Snapshot" && args[2] == "query" {
			return []byte(`{"currentUID":5,"helperUID":0,"snapshots":[
				{"displayName":"snapback-1","parentUID":0,"uid":3},
				{"displayName":"snapback-1","parentUID":3,"uid":5}
			]}`), nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.DeleteSnapshot(testVMX, "snapback-1")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("DeleteSnapshot() error = %v, want it to report the ambiguous name", err)
	}
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			t.Errorf("run() called Delete %v, want no delete when the target name is ambiguous", call)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/vm/... -run TestVMCLIController_DeleteSnapshot_AmbiguousNameReturnsErrorWithoutDeleting -v`
Expected: FAIL — current code returns the first match (`uid: 3`) with `err == nil`, so `DeleteSnapshot` succeeds instead of erroring.

- [ ] **Step 3: Fix `snapshotUID`**

Replace `internal/vm/vmcli.go:143-157`:

```go
// snapshotUID looks up name's uid via querySnapshots. vmcli's Snapshot
// Delete takes a uid, not a name (unlike vmrun) -- every delete goes
// through this lookup first. Display names are not guaranteed unique
// (e.g. an unretired orphan from a previous run sharing a same-second
// timestamp name with a new one), so more than one match is reported as
// an error rather than silently picking the first one.
func (c *VMCLIController) snapshotUID(vmxPath, name string) (uid int64, found bool, err error) {
	snapshots, err := c.querySnapshots(vmxPath)
	if err != nil {
		return 0, false, err
	}
	var matchUID int64
	matches := 0
	for _, s := range snapshots {
		if s.DisplayName == name {
			matchUID = s.UID
			matches++
		}
	}
	switch matches {
	case 0:
		return 0, false, nil
	case 1:
		return matchUID, true, nil
	default:
		return 0, false, fmt.Errorf("snapshot name %q is ambiguous: %d snapshots on %q share this display name", name, matches, vmxPath)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/vm/... -v`
Expected: PASS — new test passes, and existing `TestVMCLIController_DeleteSnapshot_LooksUpUIDAndDeletes` / `TestVMCLIController_DeleteSnapshot_NotFoundReturnsErrorWithoutCallingDelete` / both `Snapshot_Failure*` tests still pass (all use unique display names, so `matches` is 0 or 1).

- [ ] **Step 5: Commit**

```bash
git add internal/vm/vmcli.go internal/vm/vmcli_internal_test.go
git commit -m "fix(vm): reject ambiguous snapshot display names instead of picking the first match"
```

---

### Task 4: Add `vm.ErrOrphanPossible` and wire it into `Snapshot`'s cleanup path

**Files:**
- Modify: `internal/vm/vmcli.go:159-173` (`Snapshot`), plus a new package-level `var`
- Test: `internal/vm/vmcli_internal_test.go`

**Interfaces:**
- Consumes: `snapshotUID` from Task 3 (now returns `err != nil` on ambiguity, not just on genuine query failure).
- Produces: `var ErrOrphanPossible error` (exported from package `vm`) — Task 5 checks this with `errors.Is(err, vm.ErrOrphanPossible)`. `Snapshot(vmxPath, name string) error` keeps its existing signature; on a Take failure it now returns an error wrapping `ErrOrphanPossible` whenever cleanup could not be confirmed (ambiguous or errored lookup, or a failed Delete), and the plain `takeErr` (unwrapped, unchanged from today) whenever cleanup is confirmed unnecessary (no matching snapshot found) or confirmed successful (Delete succeeded).

**Finding:** The cleanup `Snapshot Delete` issued after a failed `Snapshot Take` discards its own error (`_, _, _ = c.run(...)`). If cleanup also fails, `Snapshot()` still returns only `takeErr`, indistinguishable from a clean failure. `internal/backup/choreography.go` relies on the `Controller.Snapshot` contract that an error means "no snapshot exists" — so a real orphaned snapshot is left with no warning to the user.

- [ ] **Step 1: Write the failing tests**

Add to `internal/vm/vmcli_internal_test.go`, after `TestVMCLIController_Snapshot_FailureWithPartialSnapshot_CleansUpBeforeReturningError` (line 159):

```go
func TestVMCLIController_Snapshot_FailureWithCleanupDeleteFailure_ReturnsErrOrphanPossible(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "Take":
			return nil, []byte("vmcli: snapshot failed partway"), errors.New("exit status 255")
		case args[1] == "Snapshot" && args[2] == "query":
			return []byte(`{"currentUID":7,"helperUID":0,"snapshots":[{"displayName":"snapback-1","parentUID":0,"uid":7}]}`), nil, nil
		case args[1] == "Snapshot" && args[2] == "Delete":
			return nil, []byte("vmcli: delete failed"), errors.New("exit status 255")
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.Snapshot(testVMX, "snapback-1")
	if !errors.Is(err, ErrOrphanPossible) {
		t.Errorf("Snapshot() error = %v, want errors.Is(err, ErrOrphanPossible) = true", err)
	}
	if err == nil || !strings.Contains(err.Error(), "snapshot failed partway") {
		t.Errorf("Snapshot() error = %v, want it to still mention the original Take failure", err)
	}
}

func TestVMCLIController_Snapshot_FailureWithAmbiguousCleanupLookup_ReturnsErrOrphanPossible(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "Take":
			return nil, []byte("vmcli: snapshot failed partway"), errors.New("exit status 255")
		case args[1] == "Snapshot" && args[2] == "query":
			return []byte(`{"currentUID":7,"helperUID":0,"snapshots":[
				{"displayName":"snapback-1","parentUID":0,"uid":3},
				{"displayName":"snapback-1","parentUID":3,"uid":7}
			]}`), nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.Snapshot(testVMX, "snapback-1")
	if !errors.Is(err, ErrOrphanPossible) {
		t.Errorf("Snapshot() error = %v, want errors.Is(err, ErrOrphanPossible) = true", err)
	}
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			t.Errorf("run() called Delete %v, want no delete attempt when the cleanup lookup is ambiguous", call)
		}
	}
}
```

`internal/vm/vmcli_internal_test.go` already imports `"errors"` and `"strings"` (lines 4-6) — no import changes needed there.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/vm/... -run TestVMCLIController_Snapshot_FailureWith -v`
Expected: FAIL — `ErrOrphanPossible` doesn't exist yet (compile error), or once it's stubbed in, `errors.Is` returns false because `Snapshot` never wraps it.

- [ ] **Step 3: Add `ErrOrphanPossible` and rewrite `Snapshot`**

Add `"errors"` to the import block (`internal/vm/vmcli.go:3-11`):

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

Add the sentinel error just above `Snapshot`, replacing the existing doc comment and function at `internal/vm/vmcli.go:159-173`:

```go
// ErrOrphanPossible wraps the error Snapshot returns when a Take failure's
// cleanup could not be confirmed: the post-failure snapshot lookup itself
// failed or was ambiguous, or the cleanup Delete call failed. In every
// other failure case Snapshot's contract (below) still holds -- no
// snapshot was left behind. internal/backup.Run checks for this with
// errors.Is and tags its RunError at Stage: Snapshotting (instead of
// below it) so the caller's orphan warning fires.
var ErrOrphanPossible = errors.New("snapshot cleanup could not be confirmed; a partial snapshot may remain")

// Snapshot satisfies the Controller.Snapshot contract (internal/vm/controller.go):
// it must not leave a snapshot behind when it returns an error. vmcli's
// Take reports a single pass/fail, but if it fails after partially
// creating the snapshot, this checks for and removes it before returning.
// When that check or removal can't be confirmed, the returned error wraps
// ErrOrphanPossible instead of silently claiming success.
func (c *VMCLIController) Snapshot(vmxPath, name string) error {
	_, stderr, err := c.run(vmxPath, "Snapshot", "Take", "-d", "snapback automated snapshot", name)
	if err == nil {
		return nil
	}
	takeErr := vmcliError("snapshot", stderr, err)

	uid, found, qerr := c.snapshotUID(vmxPath, name)
	if qerr != nil {
		return fmt.Errorf("%w: could not verify whether a partial snapshot remains: %v (original failure: %v)", ErrOrphanPossible, qerr, takeErr)
	}
	if !found {
		return takeErr
	}

	if _, delStderr, delErr := c.run(vmxPath, "Snapshot", "Delete", strconv.FormatInt(uid, 10)); delErr != nil {
		cleanupErr := vmcliError("cleanup after failed snapshot", delStderr, delErr)
		return fmt.Errorf("%w: %v (original failure: %v)", ErrOrphanPossible, cleanupErr, takeErr)
	}
	return takeErr
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/vm/... -v`
Expected: PASS — both new tests pass. Also re-check the two pre-existing `Snapshot` tests still pass unchanged:
  - `TestVMCLIController_Snapshot_FailureWithNoPartialSnapshot_ReturnsErrorWithoutDeleting`: `found=false` → returns plain `takeErr`, still not wrapping `ErrOrphanPossible`, still contains "snapshot failed".
  - `TestVMCLIController_Snapshot_FailureWithPartialSnapshot_CleansUpBeforeReturningError`: `found=true`, `delErr=nil` → returns plain `takeErr`, Delete still called with uid `7`.

- [ ] **Step 5: Commit**

```bash
git add internal/vm/vmcli.go internal/vm/vmcli_internal_test.go
git commit -m "fix(vm): surface ErrOrphanPossible when Snapshot cleanup can't be confirmed"
```

---

### Task 5: Tag `RunError.Stage` at `Snapshotting` when cleanup couldn't be confirmed

**Files:**
- Modify: `internal/backup/choreography.go:136-143`
- Modify: `internal/vm/controller.go:26-33` (doc comment only)
- Test: `internal/backup/choreography_test.go`

**Interfaces:**
- Consumes: `vm.ErrOrphanPossible` (Task 4) via `errors.Is(err, vm.ErrOrphanPossible)`.
- Produces: no signature change to `Run` or `RunError` — only which `Stage` value gets used in one existing branch.

**Finding (continued from Task 4):** Even with `ErrOrphanPossible` available, `internal/backup.Run` must actually check for it — otherwise a possibly-orphaned snapshot from Task 4's new failure mode still gets tagged `Stage: CheckingTools` (meaning "nothing to clean up"), and the user still gets no warning.

- [ ] **Step 1: Write the failing test**

Add `"fmt"` to the import block of `internal/backup/choreography_test.go:3-21` (needed to build the wrapped error):

```go
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)
```

Add after `TestRun_SnapshotError_RunErrorStageBelowSnapshotting` (currently ends at line 788):

```go
func TestRun_SnapshotError_OrphanPossible_RunErrorStageAtOrAboveSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = fmt.Errorf("cleanup failed: %w", vm.ErrOrphanPossible)

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
	// Cleanup couldn't be confirmed, so a snapshot may actually remain --
	// Stage must be >= Snapshotting so a caller's orphan check fires and
	// points the user at `snapback cleanup`.
	if runErr.Stage < progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want >= %v (cleanup could not be confirmed)", runErr.Stage, progress.Snapshotting)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup/... -run TestRun_SnapshotError_OrphanPossible_RunErrorStageAtOrAboveSnapshotting -v`
Expected: FAIL — `Run` currently always tags `Stage: progress.CheckingTools` on any `ctrl.Snapshot` error, so `runErr.Stage < progress.Snapshotting`.

- [ ] **Step 3: Wire the check into `Run`**

Add `"errors"` to the import block of `internal/backup/choreography.go:3-14`:

```go
import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)
```

Replace `internal/backup/choreography.go:136-143`:

```go
	reporter.Report(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot " + snapshotName})
	if err := ctrl.Snapshot(opts.VMXPath, snapshotName); err != nil {
		// Snapshot() itself failed. Ordinarily that means no snapshot was
		// ever created, so this is tagged below progress.Snapshotting -- a
		// caller's Stage >= progress.Snapshotting orphan check must not
		// wrongly point at `snapback cleanup` for a snapshot that doesn't
		// exist. But if the implementation couldn't confirm cleanup
		// succeeded (vm.ErrOrphanPossible), a partial snapshot may actually
		// remain, so this is tagged at Snapshotting instead so that check
		// does fire.
		stage := progress.CheckingTools
		if errors.Is(err, vm.ErrOrphanPossible) {
			stage = progress.Snapshotting
		}
		return nil, &RunError{Stage: stage, Err: fmt.Errorf("snapshot: %w", err)}
	}
```

Update the `Controller.Snapshot` doc comment at `internal/vm/controller.go:26-33`:

```go
	// Snapshot must not leave a snapshot behind on the source VM when it
	// returns a non-nil error. If snapshot creation partially succeeds and
	// then fails, the implementation is responsible for removing the
	// partial snapshot before returning the error. internal/backup.Run
	// relies on this: it treats a Snapshot error as proof no snapshot
	// exists, used to decide whether to warn the caller about a possible
	// orphaned snapback-<timestamp> snapshot -- unless that error wraps
	// vm.ErrOrphanPossible (internal/vm/vmcli.go), which means cleanup
	// itself could not be confirmed and a snapshot may actually remain.
	Snapshot(vmxPath, name string) error
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backup/... -v`
Expected: PASS — new test passes, and `TestRun_SnapshotError_RunErrorStageBelowSnapshotting` (plain `errBoom`, doesn't wrap `ErrOrphanPossible`) still asserts `Stage < progress.Snapshotting` and still passes.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/choreography.go internal/backup/choreography_test.go internal/vm/controller.go
git commit -m "fix(backup): tag Snapshot's RunError at Stage: Snapshotting when cleanup can't be confirmed"
```

---

## Final Verification

- [ ] **Run the full unit suite and vet**

```bash
go vet ./...
go test ./... -v
```

Expected: all packages PASS, no vet warnings.

- [ ] **Run the full unit suite via the project's Makefile target (matches CI)**

```bash
make test
```

Expected: PASS, coverage report generated at `coverage.out`.
