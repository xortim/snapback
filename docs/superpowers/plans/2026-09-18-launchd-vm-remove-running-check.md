# Launchd vm-remove Running-Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the `vm remove` half of issue #96 — `launchd.Sync`'s removal loop must running-check a VM before tearing down its LaunchAgent even when that VM has been removed from config entirely, not just when its schedule was cleared to `""`.

**Architecture:** `vm remove <name>` is the one call site that still knows the removed VM's real `config.VM.Name` at the moment it's dropped from config — `runVMRemove` already has it as its own `name` parameter. Thread that name through the existing `persistConfigAndSync` → `syncSchedules` → `launchd.Sync` call chain as a new variadic `removedNames` parameter, and have `Sync`'s existing `nameByLabel` map (already used to running-check the "schedule cleared to \"\"" case) absorb it too. No new concepts — this reuses the exact mechanism #90 already built for the sibling case, extended to cover the one gap #90 explicitly deferred.

**Tech Stack:** Go 1.26.5, standard `testing` package, existing `launchd.FakeInstaller` / `backup.AcquireLock` test fixtures.

**Spec:** https://github.com/xortim/snapback/issues/96 (no separate ADR — this is a narrow, scoped bug fix against existing scheduling code landed under ADR-005, `docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md`)

## Global Constraints

- Module path `github.com/xortim/snapback`, Go 1.26.5.
- Run `make lint` and `make test` before every commit that finishes a task — these are CI's own targets (see CLAUDE.md's Commands section); do not substitute ad-hoc `go vet`/`go build`.
- The new `removedNames ...string` parameters must be backward compatible: `schedule sync`, `init`, and `vm add` — none of which have a removed name to supply — must keep compiling and behaving identically with zero variadic arguments.
- Do not touch `init`'s own drop-a-VM path (tracked separately by #88, already fixed) or attempt to solve the "hand-edited config.yaml then `schedule sync`" case — issue #96 explicitly scopes that one out as unrecoverable (no real `Name` survives a bare sanitized label), and this plan preserves that as a documented, intentional gap.

---

## File Structure

- `internal/launchd/sync.go` — `Sync`'s signature and `nameByLabel` construction gain the new `removedNames` input; doc comments on `Sync` and `RunningChecker` updated to describe the narrowed (not closed) gap.
- `internal/launchd/sync_test.go` — one existing test renamed/reworded to clarify it now covers only the "no name supplied" case; two new tests cover the "name supplied" case (running → skip; not running → remove).
- `internal/cli/schedule.go` — `persistConfigAndSync` and `syncSchedules` both gain a trailing `removedNames ...string` parameter, forwarded down to `launchd.Sync`.
- `internal/cli/vm.go` — `runVMRemove` passes its own `name` as the removed-name argument.
- `internal/cli/vm_internal_test.go` — two new CLI-level tests prove the wiring end-to-end: a held lock skips the removal, and an unlocked VM's LaunchAgent is actually removed (proving `isRunning` was consulted, not merely bypassed).

---

### Task 1: `launchd.Sync` accepts and honors removed VM names

**Files:**
- Modify: `internal/launchd/sync.go`
- Test: `internal/launchd/sync_test.go`

**Interfaces:**
- Produces: `Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker, removedNames ...string) (SyncResult, error)` — the `removedNames` parameter is new; everything else is unchanged. Callers with nothing to pass (the common case) simply omit it.

- [ ] **Step 1: Write the failing tests**

Open `internal/launchd/sync_test.go`. Replace the existing `TestSync_VMRemovedFromList_StillRemovesUnconditionally` (it currently asserts `isRunning` is *never* called for a removed VM — that's only still true when the caller has no name to supply) with this reworded version, and add the two new tests directly after it:

```go
func TestSync_VMRemovedFromList_NoNameSupplied_StillRemovesUnconditionally(t *testing.T) {
	// Documents the remaining, accepted gap: a VM entirely gone from vms
	// with no removedNames entry either (e.g. config.yaml hand-edited to
	// delete the VM, then `schedule sync` run) can't be running-checked --
	// only its sanitized label survives, and nothing in that path ever
	// knew its real Name. See RunningChecker's doc comment and issue #96.
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	called := false
	isRunning := func(string) (bool, error) {
		called = true
		return true, nil
	}
	result, err := Sync(inst, nil, "/bin/snapback", isRunning)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if called {
		t.Error("isRunning was called for a VM no longer in vms with no removedNames supplied -- only its sanitized label is available, so this must not be checked (see #96)")
	}
	if len(result.Removed) != 1 {
		t.Errorf("result.Removed = %v, want the plist removed unconditionally", result.Removed)
	}
}

func TestSync_RemovedVMName_HonorsRunningCheckBeforeBootout(t *testing.T) {
	// vm remove passes the removed VM's real name via removedNames --
	// Sync must running-check it exactly like a VM whose schedule was
	// merely cleared to "", instead of falling into the unconditional
	// bootout that applies when no name is available at all (#96).
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	bootoutsAfterInstall := len(inst.BootoutCalls)

	stillRunning := func(name string) (bool, error) {
		if name != "dev" {
			t.Errorf("isRunning called with %q, want %q", name, "dev")
		}
		return true, nil
	}
	result, err := Sync(inst, nil, "/bin/snapback", stillRunning, "dev")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "dev" {
		t.Errorf("result.Skipped = %v, want [\"dev\"] -- removing a VM mid-backup must not kill it", result.Skipped)
	}
	if len(result.Removed) != 0 {
		t.Errorf("result.Removed = %v, want none while running", result.Removed)
	}
	if len(inst.BootoutCalls) != bootoutsAfterInstall || len(inst.RemoveCalls) != 0 {
		t.Errorf("Bootout/Remove called = %v/%v, want no new Bootout and no Remove while the backup is in progress", inst.BootoutCalls, inst.RemoveCalls)
	}

	// Once the backup finishes, the deferred removal must still apply.
	result, err = Sync(inst, nil, "/bin/snapback", neverRunning, "dev")
	if err != nil {
		t.Fatalf("third Sync() error = %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "com.tim.snapback.dev" {
		t.Errorf("third Sync() result.Removed = %v, want the plist removed once no longer running", result.Removed)
	}
}

func TestSync_RemovedVMName_ProceedsWhenNotRunning(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	called := false
	isRunning := func(name string) (bool, error) {
		called = true
		if name != "dev" {
			t.Errorf("isRunning called with %q, want %q", name, "dev")
		}
		return false, nil
	}
	result, err := Sync(inst, nil, "/bin/snapback", isRunning, "dev")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if !called {
		t.Error("isRunning was not called for a removed VM whose name was supplied via removedNames")
	}
	if len(result.Removed) != 1 || result.Removed[0] != "com.tim.snapback.dev" {
		t.Errorf("result.Removed = %v, want the plist removed", result.Removed)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/launchd/... -run TestSync_RemovedVMName -v`
Expected: FAIL — `too many arguments in call to Sync` (the `"dev"` trailing argument doesn't compile against the current 4-parameter signature).

- [ ] **Step 3: Update `Sync`'s signature and `nameByLabel` construction**

In `internal/launchd/sync.go`, change the function signature:

```go
func Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker, removedNames ...string) (SyncResult, error) {
```

And extend the `nameByLabel` construction right below the `DetectCollisions` check:

```go
	nameByLabel := make(map[string]string, len(vms)+len(removedNames))
	for _, v := range vms {
		nameByLabel[labelPrefix+sanitizeLabel(v.Name)] = v.Name
	}
	for _, name := range removedNames {
		nameByLabel[labelPrefix+sanitizeLabel(name)] = name
	}
```

- [ ] **Step 4: Update the doc comments describing the gap**

Replace `RunningChecker`'s "Scope limit" paragraph (the last paragraph of its doc comment) with:

```go
// Scope limit: the loop below covers every VM still present in vms,
// whether it's newly scheduled, updated, or had its Schedule cleared to
// "" (which routes it into the removal loop further down, since it's no
// longer in desiredLabels) -- the real config.VM.Name is available for
// all of those. A VM removed from vms entirely (`vm remove`) is also
// covered, but only because `vm remove` passes its own name through
// Sync's removedNames parameter explicitly -- the removal loop has no
// way to recover a real Name from a bare sanitized label on its own.
// The still-uncovered case is a VM entry deleted by hand directly in
// config.yaml, followed by `schedule sync`: nothing calling Sync in
// that path ever knew the deleted VM's real Name, so there's no name to
// pass as removedNames, and that VM's label falls back to the
// unconditional-bootout case below (issue #96).
```

Add this new paragraph to `Sync`'s own doc comment, right before the "If reconciliation fails partway through" paragraph:

```go
// removedNames names any VM(s) no longer present in vms at all (e.g.
// `vm remove <name>`) whose in-progress-backup state should still be
// checked before their LaunchAgent is torn down, the same as any VM
// still in vms -- see RunningChecker's Scope limit above for why this
// can't be recovered from vms itself. Most callers (schedule sync,
// init, vm add) have nothing to pass here and omit it entirely.
//
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/launchd/... -v`
Expected: PASS — every test in the package, including the three touched/added above and all pre-existing ones (confirms the variadic addition didn't break any 4-argument call site).

- [ ] **Step 6: Lint**

Run: `make lint`
Expected: no findings.

- [ ] **Step 7: Commit**

```bash
git add internal/launchd/sync.go internal/launchd/sync_test.go
git commit -m "$(cat <<'EOF'
fix(launchd): honor removed VM names in Sync's running-check

Sync's removal loop already running-checked a VM whose schedule was
cleared to "" but left in config (#90); a VM removed from config
entirely fell into an unconditional bootout instead, since only its
sanitized label survived. Sync now accepts an optional removedNames
list and folds it into the same nameByLabel map the "schedule cleared"
case already uses, so a caller that still has the real name (vm
remove, next) can supply it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Thread `removedNames` through `persistConfigAndSync`/`syncSchedules`

**Files:**
- Modify: `internal/cli/schedule.go`

**Interfaces:**
- Consumes: `launchd.Sync(installer, vms, binaryPath, isRunning, removedNames...)` from Task 1.
- Produces: `persistConfigAndSync(cmd, marshal, writeFile, newInstaller, executable, configPath, cfg, syncDestination string, removedNames ...string) error` and `syncSchedules(cmd, newInstaller, executable, destination string, vms []config.VM, removedNames ...string) error` — both existing names, now with a trailing variadic parameter. Task 3's `runVMRemove` call relies on this exact parameter position (last).

- [ ] **Step 1: Update `syncSchedules`**

In `internal/cli/schedule.go`, change:

```go
func syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), destination string, vms []config.VM) error {
```

to:

```go
func syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), destination string, vms []config.VM, removedNames ...string) error {
```

and change the `launchd.Sync` call inside it from:

```go
	result, err := launchd.Sync(installer, vms, binaryPath, isRunning)
```

to:

```go
	result, err := launchd.Sync(installer, vms, binaryPath, isRunning, removedNames...)
```

- [ ] **Step 2: Update `persistConfigAndSync`**

Change:

```go
func persistConfigAndSync(cmd *cobra.Command, marshal func(*config.Config) ([]byte, error), writeFile func(string, []byte) error, newInstaller func() (launchd.Installer, error), executable func() (string, error), configPath string, cfg *config.Config, syncDestination string) error {
```

to:

```go
func persistConfigAndSync(cmd *cobra.Command, marshal func(*config.Config) ([]byte, error), writeFile func(string, []byte) error, newInstaller func() (launchd.Installer, error), executable func() (string, error), configPath string, cfg *config.Config, syncDestination string, removedNames ...string) error {
```

and change the `syncSchedules` call inside it from:

```go
		if err := syncSchedules(cmd, newInstaller, executable, syncDestination, cfg.VMs); err != nil {
```

to:

```go
		if err := syncSchedules(cmd, newInstaller, executable, syncDestination, cfg.VMs, removedNames...); err != nil {
```

- [ ] **Step 3: Update `syncSchedules`'s doc comment**

Its doc comment currently ends with "...forwarded to `backup.IsRunning` so Sync can tell whether a VM's scheduled run is currently in progress before tearing down its LaunchAgent." Append a new sentence:

```go
// removedNames is forwarded to launchd.Sync unchanged -- see its doc
// comment for who has one to pass (currently only vm remove).
```

- [ ] **Step 4: Verify existing call sites still compile**

Run: `go build ./...`
Expected: succeeds. `runScheduleSync`, `runVMAdd`, and `runInit` all call `persistConfigAndSync`/`syncSchedules` with the pre-existing argument count (variadic parameter defaults to empty) — none of them need edits in this task.

- [ ] **Step 5: Run the existing schedule/init/vm test suites to confirm no regression**

Run: `go test ./internal/cli/... -run 'TestScheduleSync|TestInitCmd|TestVMAddCmd|TestVMRemoveCmd' -v`
Expected: PASS — identical behavior to before this task, since every existing call site passes zero `removedNames`.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/schedule.go
git commit -m "$(cat <<'EOF'
fix(cli): thread removedNames through persistConfigAndSync/syncSchedules

Plumbing-only change: both helpers gain a trailing variadic
removedNames, forwarded to launchd.Sync unchanged. Every existing call
site (schedule sync, init, vm add) has nothing to pass and is
unaffected; vm remove (next) is the only caller that will use it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `vm remove` supplies its own name; end-to-end coverage

**Files:**
- Modify: `internal/cli/vm.go`
- Test: `internal/cli/vm_internal_test.go`

**Interfaces:**
- Consumes: `persistConfigAndSync(..., syncDestination string, removedNames ...string) error` from Task 2.

- [ ] **Step 1: Write the failing tests**

Add these two tests to `internal/cli/vm_internal_test.go` (needs a new import: `"github.com/xortim/snapback/internal/backup"`, alongside the existing `config`/`launchd`/`tui` imports):

```go
func TestVMRemoveCmd_SkipsLaunchdBootoutWhenBackupRunning(t *testing.T) {
	dest := t.TempDir()
	inst := launchd.NewFakeInstaller()
	seedVMs := []config.VM{{Name: "remove-me", VMX: "/vms/remove.vmx", Schedule: "daily"}}
	if _, err := launchd.Sync(inst, seedVMs, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	lock, err := backup.AcquireLock(dest, "remove-me")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	var written []byte
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: dest,
				VMs:         []config.VM{{Name: "keep-me", VMX: "/vms/keep.vmx"}, {Name: "remove-me", VMX: "/vms/remove.vmx", Schedule: "daily"}},
			}, nil
		},
		marshal: config.Marshal,
		writeFile: func(_ string, data []byte) error {
			written = data
			return nil
		},
		newInstaller: func() (launchd.Installer, error) { return inst, nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "remove-me", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(written), "remove-me") {
		t.Errorf("written config = %q, want \"remove-me\" gone even though its LaunchAgent removal was skipped", written)
	}
	if !strings.Contains(out.String(), "skipped: remove-me") {
		t.Errorf("stdout = %q, want \"skipped: remove-me\" -- removing a VM mid-backup must not kill its in-flight run (#96)", out.String())
	}
	label := "com.tim.snapback.remove-me"
	for _, l := range inst.RemoveCalls {
		if l == label {
			t.Errorf("RemoveCalls = %v, want %q's LaunchAgent left loaded while its backup is running", inst.RemoveCalls, label)
		}
	}
}

func TestVMRemoveCmd_RemovesLaunchdScheduleWhenNotRunning(t *testing.T) {
	dest := t.TempDir()
	inst := launchd.NewFakeInstaller()
	seedVMs := []config.VM{{Name: "remove-me", VMX: "/vms/remove.vmx", Schedule: "daily"}}
	if _, err := launchd.Sync(inst, seedVMs, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: dest,
				VMs:         []config.VM{{Name: "remove-me", VMX: "/vms/remove.vmx", Schedule: "daily"}},
			}, nil
		},
		marshal:      config.Marshal,
		writeFile:    func(string, []byte) error { return nil },
		newInstaller: func() (launchd.Installer, error) { return inst, nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "remove-me", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "removed: remove-me") {
		t.Errorf("stdout = %q, want \"removed: remove-me\" -- proves isRunning was actually consulted (and found nothing) for the removed VM's real name, not just skipped by omission", out.String())
	}
	label := "com.tim.snapback.remove-me"
	found := false
	for _, l := range inst.RemoveCalls {
		if l == label {
			found = true
		}
	}
	if !found {
		t.Errorf("RemoveCalls = %v, want %q removed once confirmed not running", inst.RemoveCalls, label)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run 'TestVMRemoveCmd_SkipsLaunchdBootoutWhenBackupRunning|TestVMRemoveCmd_RemovesLaunchdScheduleWhenNotRunning' -v`
Expected: FAIL on `TestVMRemoveCmd_SkipsLaunchdBootoutWhenBackupRunning` — the held lock is never consulted yet (`runVMRemove` hasn't been changed), so `remove-me`'s LaunchAgent is torn down unconditionally and `"skipped: remove-me"` never appears in stdout. (`TestVMRemoveCmd_RemovesLaunchdScheduleWhenNotRunning` may already pass by coincidence since nothing is running either way — that's fine, it's there to pin the happy path once Step 3 lands.)

- [ ] **Step 3: Update `runVMRemove`**

In `internal/cli/vm.go`, change:

```go
	if err := persistConfigAndSync(cmd, deps.marshal, deps.writeFile, deps.newInstaller, deps.executable, configPath, cfg, cfg.Destination); err != nil {
		return err
	}
```

(the one inside `runVMRemove`, not `runVMAdd`'s identical-looking call a few lines above it) to:

```go
	if err := persistConfigAndSync(cmd, deps.marshal, deps.writeFile, deps.newInstaller, deps.executable, configPath, cfg, cfg.Destination, name); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -run 'TestVMRemoveCmd|TestVMAddCmd|TestScheduleSync|TestInitCmd' -v`
Expected: PASS across the board — the two new tests pass, and every pre-existing `vm add`/`vm remove`/`schedule sync`/`init` test is unaffected (they never supply a running VM at the removed VM's name).

- [ ] **Step 5: Full test suite and lint**

Run: `make test`
Run: `make lint`
Expected: both clean.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/vm.go internal/cli/vm_internal_test.go
git commit -m "$(cat <<'EOF'
fix(cli): vm remove passes its own name to Sync's running-check

Closes #96 (the vm-remove half; the "schedule cleared to \"\"" half
was already fixed by #90). runVMRemove already has the removed VM's
real Name as its own name parameter -- passing it through
persistConfigAndSync/syncSchedules to launchd.Sync means clearing a
VM via `vm remove <name>` mid-backup now skips tearing down its
LaunchAgent instead of killing the in-flight run, the same protection
`vm add`/init updates and schedule clears already had.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Final Verification

- [ ] **Step 1: Run the full suite one more time**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 2: Run lint one more time**

Run: `make lint`
Expected: clean.

- [ ] **Step 3: Manually confirm #96's own reproduction scenario is now covered**

Re-read issue #96's failure scenario: "`vm remove <name>` mid-backup ... kill[s] the in-flight process". `TestVMRemoveCmd_SkipsLaunchdBootoutWhenBackupRunning` (Task 3) reproduces exactly that scenario against a real held `backup.Lock` and asserts the LaunchAgent survives. Confirm it's in the diff and passing.

- [ ] **Step 4: Push and open a PR**

```bash
git push -u origin fix/launchd-vm-remove-running-check
gh pr create --title "fix(launchd,cli): vm remove honors running-check before tearing down its LaunchAgent" --body "$(cat <<'EOF'
## Summary
- Closes #96 (the `vm remove` half — the "schedule cleared to `\"\"`" half was already fixed by #90).
- `launchd.Sync` now accepts an optional `removedNames` list so a caller that still knows a removed VM's real name (currently only `vm remove`) can have it running-checked before its LaunchAgent is booted out, instead of falling into the unconditional-bootout path that applies when no name survives at all (hand-edited config.yaml + `schedule sync` — an accepted, documented remaining gap).

## Test plan
- [x] `go test ./internal/launchd/... -v`
- [x] `go test ./internal/cli/... -v`
- [x] `make lint`
- [x] `make test`
EOF
)"
```

Report the PR URL back once opened.

## Self-Review Notes

- **Spec coverage:** #96's own two named halves — (schedule cleared, still in config) and (removed from config entirely) — are respectively already-fixed and fixed-by-this-plan. Its "Possible direction (b)" (thread the removed VM's name through explicitly from `runVMRemove`) is exactly what Task 3 does.
- **Placeholder scan:** none — every step has literal, complete code.
- **Type consistency:** `removedNames ...string` is spelled identically across `Sync` (Task 1), `syncSchedules`/`persistConfigAndSync` (Task 2), and its call from `runVMRemove` (Task 3, passing the single string `name`, which Go implicitly wraps as `removedNames = ["name"]` via the variadic call `persistConfigAndSync(..., name)`).
