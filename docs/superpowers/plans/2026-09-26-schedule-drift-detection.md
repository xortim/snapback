# Schedule Drift Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect and warn (via `snapback status`) when a VM's actual launchd state — on disk, and whether it's actually bootstrapped — disagrees with `config.yaml`, and make `snapback schedule sync` able to fix the one case it currently can't: a plist whose content is already correct but isn't loaded.

**Architecture:** A new `Installer.IsLoaded` method plus a shared `classifyAgent` helper let both `Sync` (mutating) and a new read-only `CheckDrift` (read-only) agree on exactly one definition of "in sync" — `agentInSync` / `agentMissing` / `agentDiffers` / `agentNotLoaded`. `Sync` gains a self-heal branch for `agentNotLoaded` (re-bootstrap without a content change); `CheckDrift` reports all four VM-level states plus a fifth, `Stale` (an on-disk label with no matching VM), for `status` to print as non-fatal warnings/notes.

**Tech Stack:** Go 1.26.5, `github.com/xortim/snapback`; no new dependencies — `LaunchctlInstaller.IsLoaded` shells to the same `launchctl` binary every other real `Installer` method already uses.

**Spec:** `docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md` (ADR-006)

## Global Constraints

- Go 1.26.5, module `github.com/xortim/snapback`. No new external dependencies.
- Every task's commit must pass `make lint` and `make test` before moving to the next task (this repo's own CI targets — see `Makefile`).
- Branch `feat/schedule-drift-detection` already exists and is checked out; work happens there, not on `main`.
- Commit messages: conventional-commit format with the touched package as scope (e.g. `feat(launchd): ...`, `test(cli): ...`), not the package name as a bare prefix.
- Every commit ends with:
  ```
  Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
  ```
- Exact user-facing copy (status warnings, `schedule sync` output) must match the strings given in each task below verbatim — other tasks' tests assert on them by substring.
- `LaunchctlInstaller`'s new method gets no unit test beyond what the existing pattern already establishes: real `launchctl`-shelling methods (`Bootstrap`/`Bootout`) are covered only by the `SNAPBACK_INTEGRATION=1`-gated suite, never by a mocked-`exec.Command` unit test — `IsLoaded` follows the same split.

---

### Task 1: `Installer.IsLoaded` — interface, real implementation, fake implementation

Adding a method to the `Installer` interface means every implementer must gain it in the same commit or the package won't compile — `LaunchctlInstaller` and `FakeInstaller` both change here together.

**Files:**
- Modify: `internal/launchd/installer.go:16-50` (interface), `internal/launchd/installer.go` (add `IsLoaded` method near `Bootout`)
- Modify: `internal/launchd/fake.go` (loaded-state tracking, `IsLoaded` method)
- Create: `internal/launchd/fake_test.go` (direct unit tests for the fake's new loaded-state behavior — nothing currently unit-tests `FakeInstaller` in isolation; everything else exercises it only incidentally through `Sync`)
- Modify: `internal/launchd/launchctl_integration_test.go` (one new gated case)

**Interfaces:**
- Produces: `Installer.IsLoaded(label string) (bool, error)`; `LaunchctlInstaller.IsLoaded`; `FakeInstaller.IsLoaded`, `FakeInstaller.IsLoadedErr`, `FakeInstaller.IsLoadedCalls []string`.
- Consumes: nothing new from other tasks (this is the foundation task).

- [ ] **Step 1: Write the failing fake-behavior tests**

Create `internal/launchd/fake_test.go`:

```go
package launchd

import "testing"

func TestFakeInstaller_IsLoaded_DefaultsToFalse(t *testing.T) {
	inst := NewFakeInstaller()
	loaded, err := inst.IsLoaded("com.tim.snapback.dev")
	if err != nil {
		t.Fatalf("IsLoaded() error = %v, want nil", err)
	}
	if loaded {
		t.Error("IsLoaded() = true for a label never bootstrapped, want false")
	}
}

func TestFakeInstaller_IsLoaded_TrueAfterBootstrap(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	loaded, err := inst.IsLoaded(agent.Label)
	if err != nil {
		t.Fatalf("IsLoaded() error = %v", err)
	}
	if !loaded {
		t.Error("IsLoaded() = false after Bootstrap, want true")
	}
}

func TestFakeInstaller_IsLoaded_FalseAfterBootout(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if err := inst.Bootout(agent.Label); err != nil {
		t.Fatalf("Bootout() error = %v", err)
	}
	loaded, err := inst.IsLoaded(agent.Label)
	if err != nil {
		t.Fatalf("IsLoaded() error = %v", err)
	}
	if loaded {
		t.Error("IsLoaded() = true after Bootout, want false")
	}
}

func TestFakeInstaller_IsLoaded_PropagatesConfiguredError(t *testing.T) {
	inst := NewFakeInstaller()
	boom := &fakeErr{"simulated launchctl print failure"}
	inst.IsLoadedErr = boom
	if _, err := inst.IsLoaded("com.tim.snapback.dev"); err != boom {
		t.Errorf("IsLoaded() error = %v, want %v", err, boom)
	}
	if len(inst.IsLoadedCalls) != 1 || inst.IsLoadedCalls[0] != "com.tim.snapback.dev" {
		t.Errorf("IsLoadedCalls = %v, want the label recorded even on error", inst.IsLoadedCalls)
	}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestFakeInstaller_IsLoaded -v`
Expected: FAIL — `inst.IsLoaded undefined (type *FakeInstaller has no field or method IsLoaded)`

- [ ] **Step 3: Add `IsLoaded` to the `Installer` interface**

In `internal/launchd/installer.go`, add after the `Read` method's doc comment/signature (end of the interface block, currently closing at line 50):

```go
	// IsLoaded reports whether label is currently bootstrapped into the
	// GUI launchd domain, independent of whether a plist for it exists
	// on disk -- List reports disk state, IsLoaded reports load state,
	// and a label can be true for one and false for the other in either
	// direction (see ADR-006,
	// docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
	IsLoaded(label string) (bool, error)
```

- [ ] **Step 4: Implement `LaunchctlInstaller.IsLoaded`**

In `internal/launchd/installer.go`, add after `Bootout` (after line 119):

```go
func (l *LaunchctlInstaller) IsLoaded(label string) (bool, error) {
	err := runLaunchctl("print", guiDomain()+"/"+label)
	if err != nil {
		if isNotLoadedError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 5: Implement `FakeInstaller`'s loaded-state tracking**

Replace all of `internal/launchd/fake.go` with:

```go
package launchd

import (
	"bytes"
	"sort"
	"strings"
)

// FakeInstaller is an in-memory Installer for unit tests, mirroring
// vm.FakeVMController's role: Sync's choreography is written and tested
// against this, never against LaunchctlInstaller directly.
type FakeInstaller struct {
	WriteErr     error
	BootstrapErr error
	BootoutErr   error
	RemoveErr    error
	ListErr      error
	ReadErr      error
	IsLoadedErr  error

	// BootstrapFailAt, if non-zero, restricts BootstrapErr to only the
	// call'th call to Bootstrap (1-indexed) -- every other call succeeds.
	// Lets a test exercise Sync's error handling partway through a loop
	// of multiple VMs (BootstrapErr alone is sticky and would fail every
	// call once set, never letting an earlier VM in the same Sync
	// succeed).
	BootstrapFailAt int

	// WriteCalls/BootstrapCalls/BootoutCalls/RemoveCalls/ReadCalls/
	// IsLoadedCalls record every call to that method, in order, so tests
	// can assert not just that something happened but exactly what and
	// how many times. Each is appended to before its method's configured
	// *Err (if any) is returned, matching
	// vm.FakeVMController.CheckDiskConsistency's record-then-return
	// convention -- so a failed call still shows up here for a test to
	// assert against.
	WriteCalls     []string // labels passed to Write
	BootstrapCalls []string // plistPaths passed to Bootstrap
	BootoutCalls   []string // labels passed to Bootout
	RemoveCalls    []string // labels passed to Remove
	ReadCalls      []string // labels passed to Read
	IsLoadedCalls  []string // labels passed to IsLoaded

	// Calls is a single ordered log across every method above (e.g.
	// "write:<label>", "bootstrap:<path>", "bootout:<label>",
	// "remove:<label>", "read:<label>", "isloaded:<label>"), for tests
	// that need to assert relative ordering between different methods --
	// the per-method slices above can't show, for example, that a
	// Bootout happened before a Bootstrap.
	Calls []string

	plists map[string][]byte // label -> last-written plist content
	loaded map[string]bool   // label -> currently bootstrapped, per Bootstrap/Bootout
}

// NewFakeInstaller returns a FakeInstaller with nothing installed.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{plists: make(map[string][]byte), loaded: make(map[string]bool)}
}

func (f *FakeInstaller) Write(agent Agent) (string, bool, error) {
	f.WriteCalls = append(f.WriteCalls, agent.Label)
	f.Calls = append(f.Calls, "write:"+agent.Label)
	if f.WriteErr != nil {
		return "", false, f.WriteErr
	}
	data, err := renderPlist(agent)
	if err != nil {
		return "", false, err
	}
	existing, ok := f.plists[agent.Label]
	changed := !ok || !bytes.Equal(existing, data)
	f.plists[agent.Label] = data
	return "/fake/LaunchAgents/" + agent.Label + ".plist", changed, nil
}

// fakePlistPathPrefix/fakePlistPathSuffix bracket the synthetic path
// Write returns above -- Bootstrap only receives that path (mirroring
// the real Installer, whose Bootstrap likewise takes a path, not a
// label), so it recovers the label by trimming them back off, purely
// for this fake's own loaded-state bookkeeping.
const (
	fakePlistPathPrefix = "/fake/LaunchAgents/"
	fakePlistPathSuffix = ".plist"
)

func labelFromFakePlistPath(plistPath string) string {
	return strings.TrimSuffix(strings.TrimPrefix(plistPath, fakePlistPathPrefix), fakePlistPathSuffix)
}

func (f *FakeInstaller) Bootstrap(plistPath string) error {
	f.BootstrapCalls = append(f.BootstrapCalls, plistPath)
	f.Calls = append(f.Calls, "bootstrap:"+plistPath)
	if f.BootstrapErr != nil && (f.BootstrapFailAt == 0 || len(f.BootstrapCalls) == f.BootstrapFailAt) {
		return f.BootstrapErr
	}
	f.loaded[labelFromFakePlistPath(plistPath)] = true
	return nil
}

func (f *FakeInstaller) Bootout(label string) error {
	f.BootoutCalls = append(f.BootoutCalls, label)
	f.Calls = append(f.Calls, "bootout:"+label)
	if f.BootoutErr != nil {
		return f.BootoutErr
	}
	f.loaded[label] = false
	return nil
}

func (f *FakeInstaller) Remove(label string) error {
	f.RemoveCalls = append(f.RemoveCalls, label)
	f.Calls = append(f.Calls, "remove:"+label)
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	delete(f.plists, label)
	delete(f.loaded, label)
	return nil
}

func (f *FakeInstaller) Read(label string) ([]byte, bool, error) {
	f.ReadCalls = append(f.ReadCalls, label)
	f.Calls = append(f.Calls, "read:"+label)
	if f.ReadErr != nil {
		return nil, false, f.ReadErr
	}
	data, ok := f.plists[label]
	return data, ok, nil
}

func (f *FakeInstaller) List() ([]string, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	labels := make([]string, 0, len(f.plists))
	for l := range f.plists {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels, nil
}

func (f *FakeInstaller) IsLoaded(label string) (bool, error) {
	f.IsLoadedCalls = append(f.IsLoadedCalls, label)
	f.Calls = append(f.Calls, "isloaded:"+label)
	if f.IsLoadedErr != nil {
		return false, f.IsLoadedErr
	}
	return f.loaded[label], nil
}
```

- [ ] **Step 6: Run to verify it passes**

Run: `go test ./internal/launchd/... -v`
Expected: PASS for every `TestFakeInstaller_*` test. The full package's other tests (`sync_test.go`, `installer_test.go`, etc.) still compile and pass unchanged — this step only added methods, it didn't change `Sync`'s behavior yet.

- [ ] **Step 7: Add the gated integration case**

In `internal/launchd/launchctl_integration_test.go`, add after `TestIntegration_BootstrapThenBootout`:

```go
func TestIntegration_IsLoaded_ReflectsBootstrapAndBootout(t *testing.T) {
	requireIntegration(t)
	inst := &launchd.LaunchctlInstaller{Dir: t.TempDir()}
	agent := launchd.Agent{
		Label:      "com.tim.snapback.integration-isloaded",
		VMName:     "integration-isloaded",
		BinaryPath: "/bin/echo",
		LogPath:    t.TempDir() + "/integration-isloaded.log",
	}

	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if loaded, err := inst.IsLoaded(agent.Label); err != nil || loaded {
		t.Fatalf("IsLoaded() before Bootstrap = (%v, %v), want (false, nil)", loaded, err)
	}

	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Bootout(agent.Label); err != nil {
			t.Errorf("cleanup Bootout() error = %v", err)
		}
	})

	if loaded, err := inst.IsLoaded(agent.Label); err != nil || !loaded {
		t.Errorf("IsLoaded() after Bootstrap = (%v, %v), want (true, nil)", loaded, err)
	}
	if err := inst.Bootout(agent.Label); err != nil {
		t.Fatalf("Bootout() error = %v", err)
	}
	if loaded, err := inst.IsLoaded(agent.Label); err != nil || loaded {
		t.Errorf("IsLoaded() after Bootout = (%v, %v), want (false, nil)", loaded, err)
	}
}
```

- [ ] **Step 8: Run full suite and lint**

Run: `make test && make lint`
Expected: PASS (the new integration test skips itself without `SNAPBACK_INTEGRATION=1`, same as its neighbors).

- [ ] **Step 9: Commit**

```bash
git add internal/launchd/installer.go internal/launchd/fake.go internal/launchd/fake_test.go internal/launchd/launchctl_integration_test.go
git commit -m "$(cat <<'EOF'
feat(launchd): add Installer.IsLoaded for loaded-state queries

Installer.List only ever reported disk state -- a plist that's on disk
with correct content but not actually bootstrapped (manual `launchctl
bootout`, or an interrupted sync) read as "installed" with no way to
tell it apart from a genuinely loaded job. IsLoaded closes that gap on
both LaunchctlInstaller (shells to `launchctl print`) and FakeInstaller
(tracks loaded state through its own Bootstrap/Bootout).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Shared `classifyAgent` helper

**Files:**
- Create: `internal/launchd/classify.go`
- Create: `internal/launchd/classify_test.go`

**Interfaces:**
- Consumes: `Installer` (Task 1, including `IsLoaded`), `Agent`, `renderPlist(Agent) ([]byte, error)`, `buildAgent(config.VM, string) (Agent, error)` — all pre-existing except `IsLoaded`.
- Produces: `agentState` (`agentInSync`, `agentMissing`, `agentDiffers`, `agentNotLoaded`), `classifyAgent(installer Installer, agent Agent) (agentState, error)` — consumed by Task 3 (`Sync`) and Task 4 (`CheckDrift`).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/classify_test.go`:

```go
package launchd

import (
	"errors"
	"testing"
)

func TestClassifyAgent_NoPlistOnDisk_ReturnsAgentMissing(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentMissing {
		t.Errorf("classifyAgent() = %v, want agentMissing", state)
	}
}

func TestClassifyAgent_ContentDiffers_ReturnsAgentDiffers(t *testing.T) {
	inst := NewFakeInstaller()
	installed := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/old/bin/snapback", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(installed); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	desired := installed
	desired.BinaryPath = "/new/bin/snapback"
	state, err := classifyAgent(inst, desired)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentDiffers {
		t.Errorf("classifyAgent() = %v, want agentDiffers", state)
	}
	// A content mismatch is drift regardless of load state -- classifyAgent
	// must not even need to ask.
	if len(inst.IsLoadedCalls) != 0 {
		t.Errorf("IsLoadedCalls = %v, want none consulted when content already differs", inst.IsLoadedCalls)
	}
}

func TestClassifyAgent_ContentMatchesButNotLoaded_ReturnsAgentNotLoaded(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	// Deliberately never Bootstrap'd -- FakeInstaller defaults to "not
	// loaded" for any label it hasn't seen a Bootstrap call for.

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentNotLoaded {
		t.Errorf("classifyAgent() = %v, want agentNotLoaded", state)
	}
}

func TestClassifyAgent_ContentMatchesAndLoaded_ReturnsAgentInSync(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentInSync {
		t.Errorf("classifyAgent() = %v, want agentInSync", state)
	}
}

func TestClassifyAgent_ReadError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	boom := errors.New("simulated read failure")
	inst.ReadErr = boom
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}

	_, err := classifyAgent(inst, agent)
	if !errors.Is(err, boom) {
		t.Errorf("classifyAgent() error = %v, want it to wrap %v", err, boom)
	}
}

func TestClassifyAgent_IsLoadedError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	boom := errors.New("simulated launchctl print failure")
	inst.IsLoadedErr = boom

	_, err := classifyAgent(inst, agent)
	if !errors.Is(err, boom) {
		t.Errorf("classifyAgent() error = %v, want it to wrap %v", err, boom)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestClassifyAgent -v`
Expected: FAIL — `undefined: classifyAgent` (and `undefined: agentMissing` etc.)

- [ ] **Step 3: Implement `classify.go`**

Create `internal/launchd/classify.go`:

```go
package launchd

import (
	"bytes"
	"fmt"
)

// agentState is classifyAgent's verdict for one desired VM's plist,
// comparing installer's actual on-disk/loaded state against what
// buildAgent would currently render for it. See ADR-006
// (docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
type agentState int

const (
	// agentInSync means the on-disk plist content matches and the label
	// is loaded -- nothing to do.
	agentInSync agentState = iota
	// agentMissing means no plist file exists for this label at all.
	agentMissing
	// agentDiffers means a plist file exists but its content doesn't
	// match what buildAgent would currently render (e.g. config.yaml's
	// schedule was hand-edited since the last sync).
	agentDiffers
	// agentNotLoaded means the on-disk content matches, but the label
	// isn't currently bootstrapped into launchd (e.g. after a manual
	// `launchctl bootout`, or an interrupted prior sync).
	agentNotLoaded
)

// classifyAgent reports how agent's actual state (on disk, and loaded)
// compares to what buildAgent would currently render for it. It's the
// one place both Sync and CheckDrift decide "does this VM's launchd
// state already match config.yaml" -- shared so the two can't disagree.
// IsLoaded is only consulted when content already matches: a plist that
// differs is drift regardless of its load state, so there's no need to
// also probe IsLoaded for it.
func classifyAgent(installer Installer, agent Agent) (agentState, error) {
	existingData, ok, err := installer.Read(agent.Label)
	if err != nil {
		return agentInSync, fmt.Errorf("read existing plist for %q: %w", agent.VMName, err)
	}
	if !ok {
		return agentMissing, nil
	}

	candidate, err := renderPlist(agent)
	if err != nil {
		return agentInSync, fmt.Errorf("render plist for %q: %w", agent.VMName, err)
	}
	if !bytes.Equal(existingData, candidate) {
		return agentDiffers, nil
	}

	loaded, err := installer.IsLoaded(agent.Label)
	if err != nil {
		return agentInSync, fmt.Errorf("check loaded state for %q: %w", agent.VMName, err)
	}
	if !loaded {
		return agentNotLoaded, nil
	}
	return agentInSync, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/launchd/... -v`
Expected: PASS for all `TestClassifyAgent_*` cases and everything else in the package (this step adds a new, not-yet-called function — `Sync` doesn't use it yet).

- [ ] **Step 5: Lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/launchd/classify.go internal/launchd/classify_test.go
git commit -m "$(cat <<'EOF'
feat(launchd): add classifyAgent, the shared drift-classification helper

One function decides agentInSync/agentMissing/agentDiffers/agentNotLoaded
for a VM's plist -- Sync and the upcoming CheckDrift both call this
instead of separately deriving "changed", so the two can't disagree
about what "in sync" means. Not wired into Sync yet.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Wire `classifyAgent` into `Sync`, self-heal `agentNotLoaded`

**Files:**
- Modify: `internal/launchd/sync.go` (`SyncResult.Reloaded`, `IsEmpty`, the per-VM loop)
- Modify: `internal/launchd/sync_test.go` (fix one existing assertion, add two new tests)
- Modify: `internal/cli/schedule.go` (`printSyncResult`'s new `reloaded:` line)
- Modify: `internal/cli/schedule_internal_test.go` (one new test)

**Interfaces:**
- Consumes: `classifyAgent`/`agentState` (Task 2).
- Produces: `SyncResult.Reloaded []string` — consumed by Task 5's `status` wiring only indirectly (not read there; `status` uses `CheckDrift`, not `SyncResult`), but is `schedule sync`'s own user-visible output.

- [ ] **Step 1: Update the one existing test whose assertion this change invalidates**

In `internal/launchd/sync_test.go`, `TestSync_AlreadyInSync_IsANoOp` currently ends with:

```go
	// Nothing at all beyond the Read the change-detection needs -- no
	// Write, since nothing changed.
	if got := inst.Calls[callsAfterFirst:]; len(got) != 1 || got[0] != "read:com.tim.snapback.dev" {
		t.Errorf("second Sync() made calls %v, want only the change-detecting Read", got)
	}
}
```

Replace with:

```go
	// Nothing at all beyond the Read+IsLoaded change-detection needs --
	// no Write, since nothing changed and it's already loaded. IsLoaded
	// is a new consultation as of the agentNotLoaded self-heal branch:
	// an already-in-sync VM still needs its loaded state checked every
	// time, precisely so a *not*-loaded one doesn't silently pass as
	// "no-op" forever.
	if got := inst.Calls[callsAfterFirst:]; len(got) != 2 || got[0] != "read:com.tim.snapback.dev" || got[1] != "isloaded:com.tim.snapback.dev" {
		t.Errorf("second Sync() made calls %v, want only the change-detecting Read followed by the loaded-state check", got)
	}
}
```

- [ ] **Step 2: Add the two new failing tests**

Append to `internal/launchd/sync_test.go`:

```go
func TestSync_NotLoadedButContentMatches_ReBootstraps(t *testing.T) {
	// Simulates a manual `launchctl bootout` (or an interrupted prior
	// sync) leaving a correct plist on disk but not actually loaded --
	// Sync must still notice and re-bootstrap it, not read "content
	// matches" as "nothing to do."
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	label := "com.tim.snapback.dev"
	if err := inst.Bootout(label); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	bootstrapsAfterInstall := len(inst.BootstrapCalls)

	result, err := Sync(inst, vms, "/bin/snapback", neverRunning)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Reloaded) != 1 || result.Reloaded[0] != "dev" {
		t.Errorf("result.Reloaded = %v, want [\"dev\"]", result.Reloaded)
	}
	if len(result.Installed) != 0 || len(result.Updated) != 0 {
		t.Errorf("result.Installed/Updated = %v/%v, want both empty -- content never changed", result.Installed, result.Updated)
	}
	if len(inst.BootstrapCalls) != bootstrapsAfterInstall+1 {
		t.Errorf("BootstrapCalls = %v, want exactly one new call to re-load the job", inst.BootstrapCalls)
	}
	loaded, err := inst.IsLoaded(label)
	if err != nil {
		t.Fatalf("IsLoaded() error = %v", err)
	}
	if !loaded {
		t.Error("IsLoaded() = false after the self-heal Sync, want true")
	}
}

func TestSync_NotLoaded_SkipsWhenBackupCurrentlyRunning(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	label := "com.tim.snapback.dev"
	if err := inst.Bootout(label); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	bootstrapsAfterInstall := len(inst.BootstrapCalls)

	stillRunning := func(name string) (bool, error) {
		if name != "dev" {
			t.Errorf("isRunning called with %q, want %q", name, "dev")
		}
		return true, nil
	}
	result, err := Sync(inst, vms, "/bin/snapback", stillRunning)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "dev" {
		t.Errorf("result.Skipped = %v, want [\"dev\"]", result.Skipped)
	}
	if len(result.Reloaded) != 0 {
		t.Errorf("result.Reloaded = %v, want none while running", result.Reloaded)
	}
	if len(inst.BootstrapCalls) != bootstrapsAfterInstall {
		t.Errorf("BootstrapCalls = %v, want no new call while the backup is in progress", inst.BootstrapCalls)
	}

	result, err = Sync(inst, vms, "/bin/snapback", neverRunning)
	if err != nil {
		t.Fatalf("third Sync() error = %v", err)
	}
	if len(result.Reloaded) != 1 || result.Reloaded[0] != "dev" {
		t.Errorf("third Sync() result.Reloaded = %v, want [\"dev\"] once no longer running", result.Reloaded)
	}
}
```

- [ ] **Step 3: Run to verify both new tests fail, and the updated one fails too**

Run: `go test ./internal/launchd/... -run 'TestSync_NotLoaded|TestSync_AlreadyInSync_IsANoOp' -v`
Expected: FAIL — `result.Reloaded undefined` (compile error) for the two new tests; the updated assertion in `TestSync_AlreadyInSync_IsANoOp` also fails once it compiles (still only 1 call today, not 2).

- [ ] **Step 4: Add `SyncResult.Reloaded` and update `IsEmpty`**

In `internal/launchd/sync.go`, in the `SyncResult` struct (lines 13-25), add after `Updated`:

```go
	// Reloaded lists VM names whose on-disk plist content already
	// matched config but had to be re-bootstrapped because it wasn't
	// loaded (e.g. after a manual `launchctl bootout`, or an
	// interrupted prior sync). See ADR-006.
	Reloaded []string
```

Update `IsEmpty` (lines 28-30):

```go
func (r SyncResult) IsEmpty() bool {
	return len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Reloaded) == 0 && len(r.Skipped) == 0 && len(r.Removed) == 0
}
```

- [ ] **Step 5: Replace the per-VM loop to use `classifyAgent`**

In `internal/launchd/sync.go`, replace the entire block from `existingLabels, err := installer.List()` (original line 143) through the end of the per-VM `for _, agent := range scheduled` loop (original line 216) with:

```go
	existingLabels, err := installer.List()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list installed schedules: %w", err)
	}

	var result SyncResult
	for _, agent := range scheduled {
		state, err := classifyAgent(installer, agent)
		if err != nil {
			return result, err
		}
		if state == agentInSync {
			continue
		}

		running, err := isRunning(agent.VMName)
		if err != nil {
			return result, fmt.Errorf("check running state for %q: %w", agent.VMName, err)
		}
		if running {
			result.Skipped = append(result.Skipped, agent.VMName)
			continue
		}

		// Write is called on every branch, including agentNotLoaded
		// (content already correct): its own idempotency (changed is
		// only true when content actually differs) means this never
		// re-writes the file needlessly, and it's the only way any
		// branch learns plistPath to pass to Bootstrap below.
		plistPath, _, err := installer.Write(agent)
		if err != nil {
			return result, fmt.Errorf("write plist for %q: %w", agent.VMName, err)
		}
		// Bootout before Bootstrap on every path, not just update: List()
		// only sees disk, so a job whose plist was hand-deleted (or
		// whose content matches but isn't loaded) can still be loaded in
		// launchd's session, and Bootstrap fails against an
		// already-loaded label. Bootout is idempotent for a label that
		// isn't loaded, so the extra call costs nothing when it wasn't
		// needed.
		if err := installer.Bootout(agent.Label); err != nil {
			return result, fmt.Errorf("bootout stale %q: %w", agent.VMName, err)
		}
		if err := installer.Bootstrap(plistPath); err != nil {
			return result, fmt.Errorf("bootstrap %q: %w", agent.VMName, err)
		}
		switch state {
		case agentMissing:
			result.Installed = append(result.Installed, agent.VMName)
		case agentDiffers:
			result.Updated = append(result.Updated, agent.VMName)
		case agentNotLoaded:
			result.Reloaded = append(result.Reloaded, agent.VMName)
		}
	}
```

This also removes the now-unused `existing` map (previously built from `existingLabels` solely to compute `install := !existing[agent.Label]`, which `classifyAgent`'s `agentMissing` state now covers directly) and the `bytes` import if nothing else in the file uses it — check with `goimports`/`make lint` in Step 7.

- [ ] **Step 6: Run to verify everything passes**

Run: `go test ./internal/launchd/... -v`
Expected: PASS for the entire package, including every pre-existing `Sync` test (their behavior for `agentMissing`/`agentDiffers` is identical to the old install/changed logic) and both new `TestSync_NotLoaded*` tests.

- [ ] **Step 7: Lint**

Run: `make lint`
Expected: PASS (fixes any now-unused import `gofmt`/`golangci-lint fmt` flags — likely `bytes` if nothing else in `sync.go` still uses it).

- [ ] **Step 8: Add `printSyncResult`'s `reloaded:` line**

In `internal/cli/schedule.go`, in `printSyncResult`, add after the `result.Updated` loop and before the `result.Skipped` loop:

```go
	for _, name := range result.Reloaded {
		if _, err := fmt.Fprintf(out, "reloaded: %s (was not bootstrapped)\n", name); err != nil {
			return err
		}
	}
```

- [ ] **Step 9: Write the failing CLI test**

Append to `internal/cli/schedule_internal_test.go`:

```go
func TestScheduleSyncCmd_NotLoadedButContentMatches_ReportsReloaded(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	inst := launchd.NewFakeInstaller()
	if _, err := launchd.Sync(inst, vms, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.dev"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}

	deps := scheduleDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest", VMs: vms}, nil },
		newInstaller: func() (launchd.Installer, error) { return inst, nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "reloaded: dev") {
		t.Errorf("stdout = %q, want \"reloaded: dev\"", out.String())
	}
}
```

- [ ] **Step 10: Run to verify it passes**

Run: `go test ./internal/cli/... -run TestScheduleSyncCmd -v`
Expected: PASS

- [ ] **Step 11: Full verification**

Run: `make test && make lint`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/launchd/sync.go internal/launchd/sync_test.go internal/cli/schedule.go internal/cli/schedule_internal_test.go
git commit -m "$(cat <<'EOF'
feat(launchd): self-heal a not-loaded plist in Sync via classifyAgent

Sync previously only acted when a plist's on-disk content changed --
one whose content already matched config but wasn't actually
bootstrapped (after a manual `launchctl bootout`, or an interrupted
prior sync) fell through untouched forever, even across repeated
`schedule sync` runs. Sync now re-bootstraps that case too, reported
under a new SyncResult.Reloaded distinct from Installed/Updated.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Read-only `CheckDrift`

**Files:**
- Create: `internal/launchd/drift.go`
- Create: `internal/launchd/drift_test.go`

**Interfaces:**
- Consumes: `classifyAgent`/`agentState` (Task 2), `buildAgent`, `Installer`.
- Produces: `DriftReport{NotInstalled, OutOfSync, NotLoaded, Stale []string}`, `DriftReport.IsEmpty() bool`, `CheckDrift(installer Installer, vms []config.VM, binaryPath string) (DriftReport, error)` — consumed by Task 5's `status` wiring.

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/drift_test.go`:

```go
package launchd

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestCheckDrift_CleanConfig_ReportsNoDrift(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if !report.IsEmpty() {
		t.Errorf("report = %+v, want empty for a freshly-synced config", report)
	}
}

func TestCheckDrift_ScheduledButNotInstalled(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotInstalled) != 1 || report.NotInstalled[0] != "dev" {
		t.Errorf("report.NotInstalled = %v, want [\"dev\"]", report.NotInstalled)
	}
	if len(report.OutOfSync) != 0 || len(report.NotLoaded) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only NotInstalled populated", report)
	}
}

func TestCheckDrift_ContentDiffers_ReportsOutOfSync(t *testing.T) {
	inst := NewFakeInstaller()
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/old/bin/snapback"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	report, err := CheckDrift(inst, vms, "/new/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.OutOfSync) != 1 || report.OutOfSync[0] != "dev" {
		t.Errorf("report.OutOfSync = %v, want [\"dev\"]", report.OutOfSync)
	}
	if len(report.NotInstalled) != 0 || len(report.NotLoaded) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only OutOfSync populated", report)
	}
}

func TestCheckDrift_NotLoaded(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.dev"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotLoaded) != 1 || report.NotLoaded[0] != "dev" {
		t.Errorf("report.NotLoaded = %v, want [\"dev\"]", report.NotLoaded)
	}
	if len(report.NotInstalled) != 0 || len(report.OutOfSync) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only NotLoaded populated", report)
	}
}

func TestCheckDrift_OrphanedLabel_ReportsStale(t *testing.T) {
	inst := NewFakeInstaller()
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.orphan", VMName: "orphan"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}

	report, err := CheckDrift(inst, nil, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.Stale) != 1 || report.Stale[0] != "com.tim.snapback.orphan" {
		t.Errorf("report.Stale = %v, want [\"com.tim.snapback.orphan\"]", report.Stale)
	}
}

func TestCheckDrift_MixedWorkload_OneOfEachCategory(t *testing.T) {
	inst := NewFakeInstaller()
	// "loaded": fully in sync.
	// "notinstalled": desired, nothing on disk.
	// "differs": on disk, wrong content.
	// "notloaded": on disk, right content, manually booted out.
	// "orphan": on disk, no longer desired.
	vms := []config.VM{
		{Name: "loaded", VMX: "/vms/loaded.vmx", Schedule: "daily"},
		{Name: "notinstalled", VMX: "/vms/notinstalled.vmx", Schedule: "daily"},
		{Name: "differs", VMX: "/vms/differs.vmx", Schedule: "daily"},
		{Name: "notloaded", VMX: "/vms/notloaded.vmx", Schedule: "daily"},
	}
	seedVMs := []config.VM{vms[0], vms[2], vms[3]}
	if _, err := Sync(inst, seedVMs, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.differs", VMName: "differs", BinaryPath: "/changed"}); err != nil {
		t.Fatalf("re-Write() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.notloaded"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.orphan", VMName: "orphan"}); err != nil {
		t.Fatalf("seed orphan Write() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotInstalled) != 1 || report.NotInstalled[0] != "notinstalled" {
		t.Errorf("report.NotInstalled = %v, want [\"notinstalled\"]", report.NotInstalled)
	}
	if len(report.OutOfSync) != 1 || report.OutOfSync[0] != "differs" {
		t.Errorf("report.OutOfSync = %v, want [\"differs\"]", report.OutOfSync)
	}
	if len(report.NotLoaded) != 1 || report.NotLoaded[0] != "notloaded" {
		t.Errorf("report.NotLoaded = %v, want [\"notloaded\"]", report.NotLoaded)
	}
	if len(report.Stale) != 1 || report.Stale[0] != "com.tim.snapback.orphan" {
		t.Errorf("report.Stale = %v, want [\"com.tim.snapback.orphan\"]", report.Stale)
	}
}

func TestCheckDrift_NeverCallsMutatingMethods(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.dev"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	writesBefore, bootstrapsBefore, bootoutsBefore, removesBefore :=
		len(inst.WriteCalls), len(inst.BootstrapCalls), len(inst.BootoutCalls), len(inst.RemoveCalls)

	if _, err := CheckDrift(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(inst.WriteCalls) != writesBefore || len(inst.BootstrapCalls) != bootstrapsBefore ||
		len(inst.BootoutCalls) != bootoutsBefore || len(inst.RemoveCalls) != removesBefore {
		t.Errorf("CheckDrift made a mutating call -- Write/Bootstrap/Bootout/Remove counts changed from %d/%d/%d/%d",
			writesBefore, bootstrapsBefore, bootoutsBefore, removesBefore)
	}
}

func TestCheckDrift_ListError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	boom := errors.New("simulated list failure")
	inst.ListErr = boom

	_, err := CheckDrift(inst, nil, "/bin/snapback")
	if !errors.Is(err, boom) {
		t.Errorf("CheckDrift() error = %v, want it to wrap %v", err, boom)
	}
}

func TestCheckDrift_ReadError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	inst.ReadErr = errors.New("simulated read failure")
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	if _, err := CheckDrift(inst, vms, "/bin/snapback"); err == nil {
		t.Error("CheckDrift() error = nil, want the Read failure surfaced")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestCheckDrift -v`
Expected: FAIL — `undefined: CheckDrift`

- [ ] **Step 3: Implement `drift.go`**

Create `internal/launchd/drift.go`:

```go
package launchd

import (
	"fmt"

	"github.com/xortim/snapback/internal/config"
)

// DriftReport categorizes every way a VM's actual launchd state can
// disagree with config.yaml -- see CheckDrift. VM names throughout,
// except Stale, which holds raw labels: a stale label with no matching
// desired VM has no recoverable config.VM.Name to report instead (the
// same constraint SyncResult.Removed already has -- see Sync's
// nameByLabel doc comment).
type DriftReport struct {
	NotInstalled []string // schedule set, no plist on disk yet
	OutOfSync    []string // on-disk plist content differs from config
	NotLoaded    []string // on-disk content matches, but isn't bootstrapped
	Stale        []string // on-disk label with no corresponding desired VM
}

// IsEmpty reports whether CheckDrift found nothing to report.
func (r DriftReport) IsEmpty() bool {
	return len(r.NotInstalled) == 0 && len(r.OutOfSync) == 0 && len(r.NotLoaded) == 0 && len(r.Stale) == 0
}

// CheckDrift reports how installer's on-disk/loaded state disagrees
// with vms, without changing anything -- the read-only counterpart to
// Sync, built for `snapback status` to warn from (see ADR-006,
// docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
// Only List, Read, and IsLoaded are ever called;
// Write/Bootstrap/Bootout/Remove never are. DetectCollisions is
// deliberately not run here -- a colliding config is Sync's (and
// persistConfigAndSync's) problem to reject before it's ever written,
// not status's to re-diagnose.
func CheckDrift(installer Installer, vms []config.VM, binaryPath string) (DriftReport, error) {
	var report DriftReport

	desiredLabels := make(map[string]bool)
	for _, v := range vms {
		if v.Schedule == "" {
			continue
		}
		agent, err := buildAgent(v, binaryPath)
		if err != nil {
			return DriftReport{}, err
		}
		desiredLabels[agent.Label] = true

		state, err := classifyAgent(installer, agent)
		if err != nil {
			return DriftReport{}, err
		}
		switch state {
		case agentMissing:
			report.NotInstalled = append(report.NotInstalled, v.Name)
		case agentDiffers:
			report.OutOfSync = append(report.OutOfSync, v.Name)
		case agentNotLoaded:
			report.NotLoaded = append(report.NotLoaded, v.Name)
		}
	}

	existingLabels, err := installer.List()
	if err != nil {
		return DriftReport{}, fmt.Errorf("list installed schedules: %w", err)
	}
	for _, label := range existingLabels {
		if !desiredLabels[label] {
			report.Stale = append(report.Stale, label)
		}
	}

	return report, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/launchd/... -v`
Expected: PASS for all `TestCheckDrift_*` cases and the full package.

- [ ] **Step 5: Lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/launchd/drift.go internal/launchd/drift_test.go
git commit -m "$(cat <<'EOF'
feat(launchd): add read-only CheckDrift for status to warn from

Read-only counterpart to Sync, sharing classifyAgent so the two can't
disagree: reports NotInstalled/OutOfSync/NotLoaded per desired VM plus
Stale for any on-disk label with no matching VM, never calling
Write/Bootstrap/Bootout/Remove. Not wired into any CLI command yet.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Wire `CheckDrift` into `snapback status`

**Files:**
- Modify: `internal/cli/status.go` (`statusDeps`, `newStatusCmd`, `runStatus`, new `warnScheduleDrift`)
- Modify: `internal/cli/status_internal_test.go` (add `newInstaller`/`executable` to 8 existing `Summary_*` tests; add 6 new drift tests)

**Interfaces:**
- Consumes: `launchd.CheckDrift`, `launchd.DriftReport`, `launchd.Installer`, `launchd.ShortLabel` (Task 4 and pre-existing), `defaultNewInstaller` (pre-existing, `internal/cli/schedule.go`).
- Produces: `statusDeps.newInstaller`, `statusDeps.executable`, `warnScheduleDrift(cmd *cobra.Command, deps statusDeps, vms []config.VM) error`.

- [ ] **Step 1: Add the new `statusDeps` fields and defaults (no behavior change yet)**

In `internal/cli/status.go`, update imports (currently `fmt`, `strings`, `text/tabwriter`, `time`, `unicode/utf8`, `lipgloss`, `cobra`, `backup`, `config`, `style`, `vm`) to add `"os"` and `"github.com/xortim/snapback/internal/launchd"`:

```go
import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
	"github.com/xortim/snapback/internal/style"
	"github.com/xortim/snapback/internal/vm"
)
```

Update `statusDeps` (lines 22-28):

```go
type statusDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	listArchives  func(destination string) ([]backup.Archive, error)
	searchDirs    func() []string
	discoverVMs   func(searchDirs []string) ([]discoveredVM, error)
	newController func() (vm.Controller, error)
	newInstaller  func() (launchd.Installer, error)
	executable    func() (string, error)
}
```

Update `newStatusCmd` (lines 30-39):

```go
func newStatusCmd() *cobra.Command {
	base := defaultVMCmdDeps()
	return newStatusCmdWithDeps(statusDeps{
		loadConfig:    config.Load,
		listArchives:  backup.ListArchives,
		searchDirs:    defaultVMSearchDirs,
		discoverVMs:   discoverVMs,
		newController: base.newController,
		newInstaller:  defaultNewInstaller,
		executable:    os.Executable,
	})
}
```

(`defaultNewInstaller` already exists in `internal/cli/schedule.go`, same package — no new symbol needed.)

- [ ] **Step 2: Run to verify the package still compiles and existing tests fail only where expected**

Run: `go test ./internal/cli/... -run TestStatusCmd -v`
Expected: FAIL — every `TestStatusCmd_Summary_*` test that reaches `runStatusSummary`'s branch will nil-pointer-panic once Step 4 below wires in a call to `deps.newInstaller()`. Right now (before Step 4), nothing calls the new fields yet, so this step should still PASS — it's here to confirm the baseline is green before proceeding. If anything fails here, stop and investigate before continuing.

- [ ] **Step 3: Update the 8 existing `Summary_*` tests to supply the new deps fields**

In `internal/cli/status_internal_test.go`, add these two lines to each of the following tests' `statusDeps{...}` literal, immediately after the `newController:` field:

```go
			newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
			executable:   func() (string, error) { return "/bin/snapback", nil },
```

(Match existing indentation/alignment in each literal — some use `newController: runningController,` on one line, others a multi-line `func() (vm.Controller, error) { ... }`; add the two new fields right after whichever form is already there.)

Apply to:
1. `TestStatusCmd_Summary_OneRowPerConfiguredVM`
2. `TestStatusCmd_Summary_NotesDiscoveredVMNotInConfig`
3. `TestStatusCmd_Summary_NoNoteWhenAllDiscoveredAreConfigured`
4. `TestStatusCmd_Summary_DiscoveryErrorIsNotedNotFatal`
5. `TestStatusCmd_Summary_WarnsWhenDiskChainNeedsRepair`
6. `TestStatusCmd_Summary_NoWarningWhenDiskHealthy`
7. `TestStatusCmd_Summary_NoWarningForRunningVM`
8. `TestStatusCmd_Summary_DiskCheckFactoryErrorIsNotedNotFatal`

Add the import to the test file's import block:

```go
	"github.com/xortim/snapback/internal/launchd"
```

`TestStatusCmd_VMFlag_*` tests are untouched — `status --vm <name>` never calls `warnScheduleDrift` (see Step 5's doc comment).

- [ ] **Step 4: Write the 6 new failing drift tests**

Append to `internal/cli/status_internal_test.go`:

```go
func TestStatusCmd_Summary_WarnsScheduleNotInstalled(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmx", Schedule: "daily"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "myvm") || !strings.Contains(errOut.String(), "schedule sync") {
		t.Errorf("stderr = %q, want a warning naming %q and pointing at `snapback schedule sync`", errOut.String(), "myvm")
	}
}

func TestStatusCmd_Summary_WarnsScheduleOutOfSync(t *testing.T) {
	inst := launchd.NewFakeInstaller()
	if _, _, err := inst.Write(launchd.Agent{Label: "com.tim.snapback.myvm", VMName: "myvm", BinaryPath: "/old/bin/snapback"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmx", Schedule: "daily"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return inst, nil },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "myvm") || !strings.Contains(errOut.String(), "doesn't match config.yaml") {
		t.Errorf("stderr = %q, want an out-of-sync warning naming %q", errOut.String(), "myvm")
	}
}

func TestStatusCmd_Summary_WarnsScheduleNotLoaded(t *testing.T) {
	vms := []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmx", Schedule: "daily"}}
	inst := launchd.NewFakeInstaller()
	if _, err := launchd.Sync(inst, vms, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.myvm"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	root := newTestRootForStatus(t, statusDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: "/dest", VMs: vms}, nil },
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return inst, nil },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "myvm") || !strings.Contains(errOut.String(), "not loaded") {
		t.Errorf("stderr = %q, want a not-loaded warning naming %q", errOut.String(), "myvm")
	}
}

func TestStatusCmd_Summary_NotesStaleSchedule(t *testing.T) {
	inst := launchd.NewFakeInstaller()
	if _, _, err := inst.Write(launchd.Agent{Label: "com.tim.snapback.orphan", VMName: "orphan"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmx"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return inst, nil },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "note:") || !strings.Contains(errOut.String(), "orphan") {
		t.Errorf("stderr = %q, want a note: line naming the stale label %q", errOut.String(), "orphan")
	}
	if strings.Contains(errOut.String(), "warning:") {
		t.Errorf("stderr = %q, want a stale schedule reported as note:, not warning:", errOut.String())
	}
}

func TestStatusCmd_Summary_NoScheduleDriftNoteWhenAllInSync(t *testing.T) {
	vms := []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmx", Schedule: "daily"}}
	inst := launchd.NewFakeInstaller()
	if _, err := launchd.Sync(inst, vms, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	root := newTestRootForStatus(t, statusDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: "/dest", VMs: vms}, nil },
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return inst, nil },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if strings.Contains(errOut.String(), "schedule sync") {
		t.Errorf("stderr = %q, want no schedule-drift line when config and launchd already agree", errOut.String())
	}
}

func TestStatusCmd_Summary_ScheduleDriftFactoryErrorIsNotedNotFatal(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
		newInstaller:  func() (launchd.Installer, error) { return nil, errBoom },
		executable:    func() (string, error) { return "/bin/snapback", nil },
	})
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil -- a schedule-drift factory failure must not break status's core job", err)
	}
	if !strings.Contains(errOut.String(), errBoom.Error()) {
		t.Errorf("stderr = %q, want it to note the factory failure", errOut.String())
	}
	if !strings.Contains(out.String(), "myvm") {
		t.Errorf("stdout = %q, want the summary table still printed", out.String())
	}
}
```

- [ ] **Step 5: Run to verify the new tests fail (and the 8 updated ones still pass)**

Run: `go test ./internal/cli/... -run TestStatusCmd -v`
Expected: the 6 new `*ScheduleNotInstalled*`/`*ScheduleOutOfSync*`/`*ScheduleNotLoaded*`/`*StaleSchedule*`/`*NoScheduleDriftNote*`/`*ScheduleDriftFactoryError*` tests FAIL (no `warning:`/`note:` text appears yet — `runStatus` doesn't call anything new). All other `TestStatusCmd_*` tests, including the 8 just updated, still PASS.

- [ ] **Step 6: Implement `warnScheduleDrift` and wire it into `runStatus`**

In `internal/cli/status.go`, add after `warnDamagedDiskChains` (after its closing brace):

```go
// warnScheduleDrift checks vms against launchd's actual installed/loaded
// state via launchd.CheckDrift, printing one line to stderr per drift
// category per VM -- non-fatal, mirroring warnUndiscoveredVMs/
// warnDamagedDiskChains. See ADR-006
// (docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
// A newInstaller/executable/CheckDrift failure is reported the same
// non-fatal way rather than aborting status's core job of reporting
// backup state. Not called for `status --vm <name>` -- same
// summary-only scope cut runStatusForVM's own doc comment already
// applies to other checks; the per-VM card has no natural place for a
// fourth kind of note without cluttering it.
func warnScheduleDrift(cmd *cobra.Command, deps statusDeps, vms []config.VM) error {
	installer, err := deps.newInstaller()
	if err != nil {
		_, ferr := fmt.Fprintf(cmd.ErrOrStderr(), "note: could not check schedule drift: %v\n", err)
		return ferr
	}
	binaryPath, err := deps.executable()
	if err != nil {
		_, ferr := fmt.Fprintf(cmd.ErrOrStderr(), "note: could not check schedule drift: %v\n", err)
		return ferr
	}
	report, err := launchd.CheckDrift(installer, vms, binaryPath)
	if err != nil {
		_, ferr := fmt.Fprintf(cmd.ErrOrStderr(), "note: could not check schedule drift: %v\n", err)
		return ferr
	}

	out := cmd.ErrOrStderr()
	for _, name := range report.NotInstalled {
		if _, err := fmt.Fprintf(out, "warning: %q's schedule is configured but no LaunchAgent is installed for it -- run `snapback schedule sync`\n", name); err != nil {
			return err
		}
	}
	for _, name := range report.OutOfSync {
		if _, err := fmt.Fprintf(out, "warning: %q's installed LaunchAgent doesn't match config.yaml -- run `snapback schedule sync`\n", name); err != nil {
			return err
		}
	}
	for _, name := range report.NotLoaded {
		if _, err := fmt.Fprintf(out, "warning: %q's LaunchAgent is installed but not loaded -- run `snapback schedule sync`\n", name); err != nil {
			return err
		}
	}
	for _, label := range report.Stale {
		if _, err := fmt.Fprintf(out, "note: LaunchAgent %q has no matching VM in config.yaml -- run `snapback schedule sync` to remove it\n", launchd.ShortLabel(label)); err != nil {
			return err
		}
	}
	return nil
}
```

In `runStatus`, replace:

```go
	if err := runStatusSummary(cmd, cfg.VMs, archives); err != nil {
		return err
	}
	return warnDamagedDiskChains(cmd, deps, cfg.VMs)
}
```

with:

```go
	if err := runStatusSummary(cmd, cfg.VMs, archives); err != nil {
		return err
	}
	if err := warnDamagedDiskChains(cmd, deps, cfg.VMs); err != nil {
		return err
	}
	return warnScheduleDrift(cmd, deps, cfg.VMs)
}
```

- [ ] **Step 7: Run to verify everything passes**

Run: `go test ./internal/cli/... -v`
Expected: PASS for the entire package, including all 6 new tests and all previously-passing ones.

- [ ] **Step 8: Full verification**

Run: `make test && make lint && make build`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/cli/status.go internal/cli/status_internal_test.go
git commit -m "$(cat <<'EOF'
feat(cli): warn about launchd schedule drift from `snapback status`

status now calls launchd.CheckDrift and prints a non-fatal warning:/
note: line per drift category found (not installed, content differs,
installed but not loaded, or stale), the same way it already warns
about damaged disk chains and undiscovered VMs. Closes #85.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Update CLAUDE.md, final verification

**Files:**
- Modify: `CLAUDE.md` (the launchd scheduling paragraph's "Open follow-up: #85" sentence)

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing consumed by other tasks — this is the final task.

- [ ] **Step 1: Update CLAUDE.md**

In `CLAUDE.md`, find the sentence (in the paragraph describing Phase 2's launchd scheduling half):

```
Open follow-up: #85, drift detection between
`config.yaml` and what's actually installed *and loaded* — `Sync`
classifies install-vs-update from disk contents only, so a plist that's
on disk but not bootstrapped reads as "in sync".
```

Replace with:

```
Drift detection (#85) has since landed too: a new `Installer.IsLoaded`
plus a shared `classifyAgent` helper
(`internal/launchd/classify.go`) let `Sync` and a new read-only
`launchd.CheckDrift` agree on exactly one definition of "in sync" --
`Sync` now self-heals a plist whose content already matches config but
isn't bootstrapped (reported under `SyncResult.Reloaded`), and
`snapback status` calls `CheckDrift` to warn, non-fatally, about any
VM whose configured schedule doesn't match what's actually
installed/loaded, the same way it already warns about damaged disk
chains and undiscovered VMs.
```

- [ ] **Step 2: Full repository verification**

Run: `make all`
Expected: PASS (`clean verify lint test build`, per the `Makefile`'s `all` target)

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: update CLAUDE.md now that #85's drift detection has landed

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

## Self-Review Notes

- **Spec coverage:** `Installer.IsLoaded` (Task 1) → spec §"`Installer.IsLoaded`"; shared classification (Task 2) → spec §"Shared classification"; `Sync` self-heal + `Reloaded` (Task 3) → spec §"`Sync`'s self-heal branch"; `CheckDrift`/`DriftReport` (Task 4) → spec §"`CheckDrift`"; CLI wiring, exact warning copy, `--vm` scope cut (Task 5) → spec §"CLI wiring"; testing plan (all tasks) → spec §"Testing", matched one-for-one against each bullet there.
- **Placeholder scan:** no TBD/TODO; every step carries complete, runnable code, not descriptions of code.
- **Type consistency:** `agentState`/`agentInSync`/`agentMissing`/`agentDiffers`/`agentNotLoaded` (Task 2) used identically in Task 3's `Sync` and Task 4's `CheckDrift`. `DriftReport` field names (`NotInstalled`/`OutOfSync`/`NotLoaded`/`Stale`, Task 4) match Task 5's `warnScheduleDrift` field accesses exactly. `SyncResult.Reloaded` (Task 3) matches `printSyncResult`'s new loop and the schedule CLI test's assertion.
