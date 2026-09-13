# PR #87 Review Blockers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the three blocking findings from `/code-review 87`'s review
of PR #87 (launchd scheduling) — a config-load break for every existing
install, a scheduled-run kill via `launchd.Sync`'s bootout, and a silent
schedule-loss path in `init --force` — each as its own commit directly
on `feat/launchd-scheduling-design` (this branch already *is* PR #87).

**Architecture:** Three independent, non-overlapping fixes:

1. `config.Load` gains a load-time migration step that translates a
   pre-ADR-005 cron-string `Schedule` (as written by main's still-shipping
   `internal/tui/init_schedule.go` wizard) into the closed `daily`/
   `weekly`/`monthly` enum, so an existing user's `config.yaml` keeps
   loading. `config.Validate` itself is untouched — it keeps rejecting
   raw cron strings, which is correct for anything that builds a
   `config.Config` fresh (the current wizard, `vm add`).
2. `launchd.Sync` takes a new `RunningChecker` callback and consults it
   before tearing down an *existing* LaunchAgent whose plist content
   changed, skipping (not erroring) the update for a VM whose backup is
   currently in progress — built on the existing per-VM `backup.Lock`
   (`internal/backup/lock.go`), not a new locking mechanism. The skipped
   VM's on-disk plist and loaded job are both left untouched, so the
   next `Sync` call detects the same diff and retries automatically.
3. `promptSchedules` (`internal/tui/init.go`) falls back to matching a
   prior VM by `Name` when `VMX` doesn't match (covers a bundle *moved*
   between search directories, where the folder name — and thus `Name`
   — is unchanged but the path isn't), and prints a warning naming any
   previously-scheduled VM that matches neither `VMX` nor `Name` this
   run (covers a true rename, which can't be auto-matched) so the user
   notices before confirming instead of silently losing the schedule.

**Tech Stack:** Go 1.26.5, no new dependencies. `golang.org/x/sys/unix`
(already a dependency, via `internal/backup/lock.go`'s `flock(2)` use).

**Spec:** These are targeted bug fixes from `/code-review 87`'s review of
PR #87, not new features. Background:
`docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md` (ADR-005)
describes the shipped scheduling feature these fixes protect. Each task
below is filed as its own GitHub issue with full failure-scenario detail
— read the issue before starting that task, this plan does not repeat it:

- Task 1 fixes [#89](https://github.com/xortim/snapback/issues/89)
- Task 2 fixes [#90](https://github.com/xortim/snapback/issues/90)
- Task 3 fixes [#91](https://github.com/xortim/snapback/issues/91)

## Global Constraints

- Match existing code style exactly: doc comments above every
  non-trivial function explaining *why*, not *what* (every file touched
  below already does this — follow the surrounding comments' voice).
- No new dependencies.
- `go test ./...` and `make lint` must both pass before any task is
  considered done.
- Conventional commits, package name as scope (e.g. `fix(config): ...`),
  per this repo's commit convention — not the package name as a bare
  prefix.
- Each task is exactly one commit. Do not squash the three together.

---

## Task 1: Migrate legacy cron schedules at load time (fixes #89)

**Files:**
- Create: `internal/config/migrate.go`
- Create: `internal/config/migrate_internal_test.go`
- Modify: `internal/config/config.go:61-67` (the VM tilde-expansion loop
  in `Load`)
- Test: `internal/config/config_test.go` (new `TestLoad_*` cases)

**Interfaces:**
- Produces: `migrateLegacySchedule(schedule string) string` (unexported,
  package `config`) — Task 1 only; not consumed elsewhere in this plan.

- [ ] **Step 1: Write the failing unit tests for the pure migration function**

Create `internal/config/migrate_internal_test.go`:

```go
package config

import "testing"

func TestMigrateLegacySchedule_PassesThroughValidEnumValues(t *testing.T) {
	for _, sched := range []string{"", "daily", "weekly", "monthly"} {
		if got := migrateLegacySchedule(sched); got != sched {
			t.Errorf("migrateLegacySchedule(%q) = %q, want unchanged", sched, got)
		}
	}
}

func TestMigrateLegacySchedule_NightlyCronBecomesDaily(t *testing.T) {
	// "0 2 * * *" is cronNightly from the pre-ADR-005 wizard
	// (internal/tui/init_schedule.go on main): every day, no
	// day-of-month or day-of-week restriction.
	if got := migrateLegacySchedule("0 2 * * *"); got != "daily" {
		t.Errorf("migrateLegacySchedule(nightly cron) = %q, want %q", got, "daily")
	}
}

func TestMigrateLegacySchedule_WeeklyCronBecomesWeekly(t *testing.T) {
	// "0 2 * * 0" is cronWeekly from the pre-ADR-005 wizard: fixed
	// day-of-week (Sunday), no day-of-month restriction.
	if got := migrateLegacySchedule("0 2 * * 0"); got != "weekly" {
		t.Errorf("migrateLegacySchedule(weekly cron) = %q, want %q", got, "weekly")
	}
}

func TestMigrateLegacySchedule_CustomCronWithDayOfMonthBecomesMonthly(t *testing.T) {
	// A user-typed "custom" cron (the wizard's third preset) pinning a
	// day-of-month, e.g. "run on the 1st": day-of-month takes priority
	// over day-of-week in the heuristic since it's the more specific
	// constraint.
	if got := migrateLegacySchedule("0 3 1 * *"); got != "monthly" {
		t.Errorf("migrateLegacySchedule(day-of-month cron) = %q, want %q", got, "monthly")
	}
}

func TestMigrateLegacySchedule_CustomCronWithDayOfWeekBecomesWeekly(t *testing.T) {
	if got := migrateLegacySchedule("30 4 * * 3"); got != "weekly" {
		t.Errorf("migrateLegacySchedule(day-of-week cron) = %q, want %q", got, "weekly")
	}
}

func TestMigrateLegacySchedule_UnparseableGarbageIsLeftUnchanged(t *testing.T) {
	// Not 5 fields -- Validate must still reject this as a genuinely
	// malformed schedule, not have it silently coerced to something.
	if got := migrateLegacySchedule("bogus"); got != "bogus" {
		t.Errorf("migrateLegacySchedule(garbage) = %q, want it left unchanged for Validate to reject", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run TestMigrateLegacySchedule -v`
Expected: FAIL — `undefined: migrateLegacySchedule`

- [ ] **Step 3: Implement migrateLegacySchedule**

Create `internal/config/migrate.go`:

```go
package config

import "strings"

// migrateLegacySchedule translates a config.VM.Schedule value written
// before ADR-005 (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md)
// narrowed the field to a closed "", "daily", "weekly", "monthly" enum.
// Before that change, main's `init` wizard (internal/tui/init_schedule.go's
// history) wrote raw 5-field cron expressions for its nightly/weekly/
// custom presets -- e.g. "0 2 * * *" for "nightly". Without this, an
// existing config.yaml written by that wizard fails config.Validate on
// every load after upgrading to a build with the closed enum, breaking
// every command (run/list/status/vm add/vm remove/schedule sync) for
// any user who already ran init.
//
// Already-valid enum values pass through unchanged. A value that isn't
// a recognized enum value and doesn't parse as a 5-field cron expression
// is left untouched, so Validate still rejects a genuinely malformed
// schedule instead of this function silently manufacturing a value for
// it.
//
// The cron -> enum mapping is a heuristic, not a lossless translation
// (cron can express schedules the enum can't, like "every Tuesday at
// 3am"): day-of-month takes priority over day-of-week when both are
// pinned, and a cron with neither pinned (any plain daily cron,
// including the wizard's old "nightly" preset) becomes "daily". This
// matches the closest available enum bucket for every pattern the old
// wizard's three presets (nightly/weekly/custom) could produce.
func migrateLegacySchedule(schedule string) string {
	switch schedule {
	case "", "daily", "weekly", "monthly":
		return schedule
	}

	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return schedule
	}
	dayOfMonth, dayOfWeek := fields[2], fields[4]
	switch {
	case dayOfMonth != "*":
		return "monthly"
	case dayOfWeek != "*":
		return "weekly"
	default:
		return "daily"
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/... -run TestMigrateLegacySchedule -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Wire the migration into Load, with a failing integration test first**

Add to `internal/config/config_test.go` (after `TestLoad_DefaultsMissingCompressionToZstd`):

```go
func TestLoad_MigratesLegacyCronNightlyScheduleToDaily(t *testing.T) {
	path := writeTempConfig(t, `
destination: /Volumes/Backups/snapback
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
vms:
  - name: dev
    vmx: /vms/dev.vmwarevm/dev.vmx
    schedule: "0 2 * * *"
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v, want the legacy nightly cron schedule migrated instead of rejected", err)
	}
	if cfg.VMs[0].Schedule != "daily" {
		t.Errorf("VMs[0].Schedule = %q, want %q (migrated from legacy cron)", cfg.VMs[0].Schedule, "daily")
	}
}

func TestLoad_MigratesLegacyCronWeeklyScheduleToWeekly(t *testing.T) {
	path := writeTempConfig(t, `
destination: /Volumes/Backups/snapback
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
vms:
  - name: dev
    vmx: /vms/dev.vmwarevm/dev.vmx
    schedule: "0 2 * * 0"
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v, want the legacy weekly cron schedule migrated instead of rejected", err)
	}
	if cfg.VMs[0].Schedule != "weekly" {
		t.Errorf("VMs[0].Schedule = %q, want %q (migrated from legacy cron)", cfg.VMs[0].Schedule, "weekly")
	}
}

func TestLoad_RejectsGenuinelyInvalidSchedule(t *testing.T) {
	path := writeTempConfig(t, `
destination: /Volumes/Backups/snapback
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
vms:
  - name: dev
    vmx: /vms/dev.vmwarevm/dev.vmx
    schedule: bogus
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load returned nil error for an unparseable schedule, want it still rejected")
	}
	if !strings.Contains(err.Error(), "schedule") {
		t.Errorf("Load error = %q, want it to mention \"schedule\"", err.Error())
	}
}
```

Run: `go test ./internal/config/... -run TestLoad_Migrates -v`
Expected: FAIL — schedule still `"0 2 * * *"`/`"0 2 * * 0"`, `Validate`
rejects it, `Load` returns an error.

- [ ] **Step 6: Apply the migration in Load**

In `internal/config/config.go`, the VM loop currently reads:

```go
	for i, vm := range cfg.VMs {
		expandedVMX, err := ExpandTilde(vm.VMX)
		if err != nil {
			return nil, fmt.Errorf("parse %s: expand vms[%d].vmx: %w", path, i, err)
		}
		cfg.VMs[i].VMX = expandedVMX
	}
```

Change it to:

```go
	for i, vm := range cfg.VMs {
		expandedVMX, err := ExpandTilde(vm.VMX)
		if err != nil {
			return nil, fmt.Errorf("parse %s: expand vms[%d].vmx: %w", path, i, err)
		}
		cfg.VMs[i].VMX = expandedVMX
		// Applied before Validate below so a config.yaml written by the
		// pre-ADR-005 wizard's cron-string schedules still loads -- see
		// migrateLegacySchedule's doc comment.
		cfg.VMs[i].Schedule = migrateLegacySchedule(cfg.VMs[i].Schedule)
	}
```

- [ ] **Step 7: Run the full config package test suite**

Run: `go test ./internal/config/... -v`
Expected: PASS — all tests, including the 3 new `TestLoad_*` cases and
the 6 `TestMigrateLegacySchedule_*` cases, plus every pre-existing test
(in particular `TestValidate_RejectsUnknownSchedule` in
`validate_test.go`, which calls `config.Validate` directly and must
still reject `"0 2 * * *"` — this task does not touch `Validate`).

- [ ] **Step 8: Run lint**

Run: `make lint`
Expected: no findings

- [ ] **Step 9: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_internal_test.go internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
fix(config): migrate legacy cron schedules on load

config.Validate's closed daily/weekly/monthly enum (ADR-005) rejects
the raw cron strings main's init wizard wrote before this branch
narrowed the Schedule field, breaking every command for any user who
already ran init. config.Load now translates a legacy cron schedule to
the closest enum value before Validate runs, so an existing config.yaml
keeps loading.

Fixes #89.
EOF
)"
```

---

## Task 2: Skip Sync's bootout for a VM currently mid-backup (fixes #90)

**Files:**
- Modify: `internal/backup/lock.go` (add `IsRunning`)
- Test: `internal/backup/lock_test.go` (new `TestIsRunning_*` cases)
- Modify: `internal/launchd/sync.go` (add `RunningChecker`, thread
  through `Sync`, guard the update-path bootout, add `SyncResult.Skipped`)
- Test: `internal/launchd/sync_test.go` (update all `Sync(...)` calls,
  add new skip/retry/error-propagation tests)
- Modify: `internal/cli/schedule.go` (thread `destination` through
  `syncSchedules`, build the `RunningChecker`, print `Skipped`)
- Test: `internal/cli/schedule_internal_test.go` (update the one direct
  `launchd.Sync(...)` call)
- Modify: `internal/cli/vm.go:136,180` (pass `cfg.Destination` to
  `syncSchedules`)
- Modify: `internal/cli/init.go:196` (pass `cfg.Destination` to
  `syncSchedules`)

**Interfaces:**
- Consumes: `backup.AcquireLock(destination, vmName string) (*backup.Lock, error)`,
  `backup.ErrLocked`, `(*backup.Lock).Release() error` (all pre-existing,
  `internal/backup/lock.go`).
- Produces: `backup.IsRunning(destination, vmName string) (bool, error)`
  — consumed by `internal/cli/schedule.go`'s `syncSchedules`.
- Produces: `launchd.RunningChecker` (`type RunningChecker func(vmName string) (bool, error)`)
  and the new `Sync` signature `Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker) (SyncResult, error)`
  — consumed by `internal/cli/schedule.go`.
- Produces: `SyncResult.Skipped []string` — consumed by
  `internal/cli/schedule.go`'s `printSyncResult`.

- [ ] **Step 1: Write the failing test for backup.IsRunning**

Add to `internal/backup/lock_test.go`:

```go
func TestIsRunning_FalseWhenNoLockHeld(t *testing.T) {
	dest := t.TempDir()
	running, err := backup.IsRunning(dest, "myvm")
	if err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}
	if running {
		t.Error("IsRunning() = true, want false -- nothing holds the lock")
	}
}

func TestIsRunning_TrueWhileAnotherProcessHoldsTheLock(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	running, err := backup.IsRunning(dest, "myvm")
	if err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}
	if !running {
		t.Error("IsRunning() = false, want true -- the lock is held")
	}
}

func TestIsRunning_DoesNotItselfHoldTheLockAfterReturning(t *testing.T) {
	dest := t.TempDir()
	if _, err := backup.IsRunning(dest, "myvm"); err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}

	// If IsRunning leaked its own probe lock, this second AcquireLock
	// would fail with ErrLocked.
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after IsRunning() error = %v, want nil -- IsRunning must release its probe lock", err)
	}
	_ = lock.Release()
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backup/... -run TestIsRunning -v`
Expected: FAIL — `undefined: backup.IsRunning`

- [ ] **Step 3: Implement IsRunning**

Add to `internal/backup/lock.go` (after `AcquireLock`):

```go
// IsRunning reports whether a backup or cleanup is currently in
// progress for vmName under destination -- i.e. whether AcquireLock
// would fail with ErrLocked right now. Used by launchd.Sync
// (RunningChecker) to avoid tearing down a VM's LaunchAgent while its
// scheduled run is still executing: Bootout unloads the job, sending it
// SIGTERM/SIGKILL outside this package's own choreography, which is the
// orphaned-snapshot incident class CLAUDE.md documents as a real,
// confirmed failure mode.
func IsRunning(destination, vmName string) (bool, error) {
	lock, err := AcquireLock(destination, vmName)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return true, nil
		}
		return false, err
	}
	return false, lock.Release()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backup/... -run TestIsRunning -v`
Expected: PASS

- [ ] **Step 5: Write the failing tests for Sync's running-check guard**

`Sync`'s signature is changing, so every existing call in
`internal/launchd/sync_test.go` needs a 4th argument. Add this helper
near the top of the file (after `nthCall`):

```go
// neverRunning is a RunningChecker stub for tests that aren't exercising
// the running-check guard itself -- always reports "not running" so
// Sync's existing bootout/bootstrap behavior is unaffected.
func neverRunning(string) (bool, error) { return false, nil }
```

Then update every existing call from `Sync(inst, vms, "/bin/snapback")`
to `Sync(inst, vms, "/bin/snapback", neverRunning)` (9 call sites in
this file: `TestSync_InstallsNewlyScheduledVM`,
`TestSync_UnscheduledVM_NeverWritten`, `TestSync_AlreadyInSync_IsANoOp`
(both calls), `TestSync_ScheduleChanged_UpdatesAndRebootstraps` (both
calls), `TestSync_ScheduleCleared_RemovesPlist` (both calls),
`TestSync_VMNoLongerInList_RemovesPlist` (both calls),
`TestSync_CollidingNames_ErrorsBeforeWritingAnything`,
`TestSync_MixedWorkload_InstallsOneAndRemovesAnother` (both calls),
`TestSync_ErrorMidLoop_PreservesPartialResult`).

Then add these new tests at the end of the file:

```go
func TestSync_UpdatePath_SkipsWhenBackupCurrentlyRunning(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	callsAfterInstall := len(inst.Calls)

	vms[0].Schedule = "weekly"
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
	if len(result.Updated) != 0 {
		t.Errorf("result.Updated = %v, want none -- must not apply while running", result.Updated)
	}
	if got := inst.Calls[callsAfterInstall:]; len(got) != 0 {
		t.Errorf("Calls after the skipped Sync = %v, want none -- Write/Bootout/Bootstrap must not run while the backup is in progress", got)
	}

	// Once the backup finishes, the deferred update must still apply --
	// proves the skip doesn't get permanently stuck because Write was
	// never called to mark it as already-applied.
	result, err = Sync(inst, vms, "/bin/snapback", neverRunning)
	if err != nil {
		t.Fatalf("third Sync() error = %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "dev" {
		t.Errorf("third Sync() result.Updated = %v, want [\"dev\"] once no longer running", result.Updated)
	}
}

func TestSync_FreshInstall_NeverConsultsRunningCheck(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	called := false
	isRunning := func(string) (bool, error) {
		called = true
		return false, nil
	}
	if _, err := Sync(inst, vms, "/bin/snapback", isRunning); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if called {
		t.Error("isRunning was called for a fresh install -- nothing can be running under a label that was never bootstrapped")
	}
}

func TestSync_RunningCheckError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms[0].Schedule = "weekly"
	boom := errors.New("lock check boom")
	_, err := Sync(inst, vms, "/bin/snapback", func(string) (bool, error) { return false, boom })
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("Sync() error = %v, want it to wrap %v", err, boom)
	}
}
```

- [ ] **Step 6: Run to verify the new tests fail and old ones don't compile**

Run: `go test ./internal/launchd/... -v`
Expected: build failure — `not enough arguments in call to Sync`.

- [ ] **Step 7: Implement the RunningChecker guard in Sync**

In `internal/launchd/sync.go`, update `SyncResult`:

```go
// SyncResult records what Sync changed, for callers to report to the
// user (internal/cli's `schedule sync`, `vm add`, `vm remove`, `init`
// all print this the same way).
type SyncResult struct {
	Installed []string // VM names newly given a LaunchAgent
	Updated   []string // VM names whose LaunchAgent was rewritten and re-bootstrapped
	Removed   []string // labels booted out and deleted
	// Skipped lists VM names whose LaunchAgent needed an update (its
	// plist content changed) but were left alone because a backup was
	// in progress for that VM at the time -- see RunningChecker. Neither
	// the on-disk plist nor the loaded job were touched, so the next
	// Sync call will detect the same diff and retry.
	Skipped []string
}

// IsEmpty reports whether Sync found nothing to do.
func (r SyncResult) IsEmpty() bool {
	return len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Removed) == 0 && len(r.Skipped) == 0
}

// RunningChecker reports whether vmName currently has a backup in
// progress (see backup.IsRunning). Sync consults this before tearing
// down an *existing* LaunchAgent whose content changed -- Bootout
// unloads the running job, which would otherwise kill a scheduled
// backup mid-choreography (see CLAUDE.md's "Known gotchas" for the
// orphaned-snapshot incident this guards against). A freshly-scheduled
// VM with no existing LaunchAgent has nothing running under launchd to
// race, so this is only consulted on the update path, never the install
// path.
type RunningChecker func(vmName string) (bool, error)
```

Then update `Sync`'s signature and the scheduled-VM loop:

```go
func Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker) (SyncResult, error) {
	if err := DetectCollisions(vms); err != nil {
		return SyncResult{}, err
	}

	var scheduled []Agent
	desiredLabels := make(map[string]bool)
	for _, v := range vms {
		if v.Schedule == "" {
			continue
		}
		agent, err := buildAgent(v, binaryPath)
		if err != nil {
			return SyncResult{}, err
		}
		scheduled = append(scheduled, agent)
		desiredLabels[agent.Label] = true
	}

	existingLabels, err := installer.List()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list installed schedules: %w", err)
	}
	existing := make(map[string]bool, len(existingLabels))
	for _, l := range existingLabels {
		existing[l] = true
	}

	var result SyncResult
	for _, agent := range scheduled {
		install := !existing[agent.Label]
		if !install {
			// Checked before Write (not after): Write persists the new
			// plist content unconditionally, and that content is what
			// "changed" below is diffed against next time. Checking
			// first means a skip leaves the on-disk plist untouched, so
			// the next Sync call still sees the real diff and retries --
			// checking after Write would make the retry never fire,
			// since the second call would find changed == false.
			running, err := isRunning(agent.VMName)
			if err != nil {
				return result, fmt.Errorf("check running state for %q: %w", agent.VMName, err)
			}
			if running {
				result.Skipped = append(result.Skipped, agent.VMName)
				continue
			}
		}

		plistPath, changed, err := installer.Write(agent)
		if err != nil {
			return result, fmt.Errorf("write plist for %q: %w", agent.VMName, err)
		}
		if !install && !changed {
			continue
		}
		// Bootout before Bootstrap on *both* paths, not just the update
		// path. Bootstrap fails against an already-loaded label, and
		// "install" here only means "no plist on disk" (that's all
		// Installer.List can see) -- a job can still be loaded in
		// launchd's session with its plist deleted by hand, which would
		// otherwise make an unrelated command like `vm add` fail after it
		// had already written config.yaml. Bootout is idempotent for a
		// label that isn't loaded (see isNotLoadedError), so the extra
		// call is free on the normal install path.
		if err := installer.Bootout(agent.Label); err != nil {
			return result, fmt.Errorf("bootout stale %q: %w", agent.VMName, err)
		}
		if err := installer.Bootstrap(plistPath); err != nil {
			return result, fmt.Errorf("bootstrap %q: %w", agent.VMName, err)
		}
		if install {
			result.Installed = append(result.Installed, agent.VMName)
		} else {
			result.Updated = append(result.Updated, agent.VMName)
		}
	}

	for _, label := range existingLabels {
		if desiredLabels[label] {
			continue
		}
		if err := installer.Bootout(label); err != nil {
			return result, fmt.Errorf("bootout %q: %w", label, err)
		}
		if err := installer.Remove(label); err != nil {
			return result, fmt.Errorf("remove plist %q: %w", label, err)
		}
		result.Removed = append(result.Removed, label)
	}

	return result, nil
}
```

- [ ] **Step 8: Run the launchd package tests**

Run: `go test ./internal/launchd/... -v`
Expected: PASS — all pre-existing tests (now passing `neverRunning`)
plus the 3 new tests from Step 5.

- [ ] **Step 9: Update the one direct Sync call in internal/cli**

In `internal/cli/schedule_internal_test.go`, `TestScheduleSyncCmd_Removed_PrintsVMNameNotRawLabel`
seeds state with a direct `launchd.Sync` call:

```go
	if _, err := launchd.Sync(inst, []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}, "/bin/snapback"); err != nil {
```

Change to:

```go
	if _, err := launchd.Sync(inst, []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
```

- [ ] **Step 10: Thread destination through syncSchedules in internal/cli/schedule.go**

Add the import and update both functions:

```go
import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
)
```

```go
func runScheduleSync(cmd *cobra.Command, deps scheduleDeps) error {
	cfg, _, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}
	return syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.Destination, cfg.VMs)
}

// syncSchedules connects to launchd, resolves the running binary's path,
// runs launchd.Sync, and prints the result -- shared by `schedule sync`
// and the auto-sync call sites in `vm add`/`vm remove`/`init` so there's
// exactly one place that does this, not four. destination is cfg's
// backup destination, forwarded to backup.IsRunning so Sync can tell
// whether a VM's scheduled run is currently in progress before tearing
// down its LaunchAgent.
func syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), destination string, vms []config.VM) error {
	installer, err := newInstaller()
	if err != nil {
		return fmt.Errorf("connect to launchd: %w", err)
	}
	binaryPath, err := executable()
	if err != nil {
		return fmt.Errorf("resolve snapback binary path: %w", err)
	}

	isRunning := func(vmName string) (bool, error) {
		return backup.IsRunning(destination, vmName)
	}

	result, err := launchd.Sync(installer, vms, binaryPath, isRunning)
	if err != nil {
		return fmt.Errorf("sync launchd schedules: %w", err)
	}
	return printSyncResult(cmd.OutOrStdout(), result)
}
```

And add a `Skipped` loop to `printSyncResult`, after the `Updated` loop
and before the `Removed` loop:

```go
	for _, name := range result.Skipped {
		if _, err := fmt.Fprintf(out, "skipped: %s (backup in progress, will retry on next sync)\n", name); err != nil {
			return err
		}
	}
```

- [ ] **Step 11: Update the other two syncSchedules call sites**

In `internal/cli/vm.go`, both call sites:

```go
			if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs); err != nil {
```

become (line 136, inside `runVMAdd`, and line 180, inside `runVMRemove`):

```go
			if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.Destination, cfg.VMs); err != nil {
```

In `internal/cli/init.go:196`:

```go
		if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs); err != nil {
```

becomes:

```go
		if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.Destination, cfg.VMs); err != nil {
```

- [ ] **Step 12: Run the full cli and backup package tests**

Run: `go test ./internal/cli/... ./internal/backup/... ./internal/launchd/... -v`
Expected: PASS — no test in these packages exercises the update path
through the CLI layer with a nonexistent `Destination` like `/dest`
(confirmed by inspection of `schedule_internal_test.go`,
`vm_test.go`, `init_test.go` — every existing case is either a fresh
install, an error path before `Sync` runs, or a removal, none of which
call `isRunning`), so none of them touch the filesystem via the new
`backup.IsRunning` call.

- [ ] **Step 13: Run the full suite and lint**

Run: `go test ./... && make lint`
Expected: PASS, no findings

- [ ] **Step 14: Commit**

```bash
git add internal/backup/lock.go internal/backup/lock_test.go internal/launchd/sync.go internal/launchd/sync_test.go internal/cli/schedule.go internal/cli/schedule_internal_test.go internal/cli/vm.go internal/cli/init.go
git commit -m "$(cat <<'EOF'
fix(launchd): skip Sync's bootout while a scheduled backup is running

Sync unconditionally booted out a VM's launchd job before
re-bootstrapping it whenever that VM's plist content changed, with no
check for whether the job was mid-backup -- bootout sends
SIGTERM/SIGKILL to the running process outside backup's own
choreography, the same orphaned-snapshot incident class CLAUDE.md
already documents. Sync now takes a RunningChecker, built on the
existing per-VM backup.Lock via the new backup.IsRunning, and skips
(not errors) the update for a VM currently mid-backup -- the on-disk
plist and loaded job are both left untouched so the next sync retries
the deferred update automatically.

Fixes #90.
EOF
)"
```

---

## Task 3: Fall back to name match and warn on schedule-loss (fixes #91)

**Files:**
- Modify: `internal/tui/init.go` (`promptSchedules`)
- Test: `internal/tui/init_test.go`

**Interfaces:**
- Consumes: `config.VM{Name, VMX, Schedule string}`, `scheduleChoiceFor(schedule string) string`,
  `resolveSchedule(choice string) string` (all pre-existing, unchanged).
- No new exported interfaces — `promptSchedules`'s signature is
  unchanged: `promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM, prior *config.Config) error`.

- [ ] **Step 1: Fix the existing test whose premise this change corrects**

`TestPromptSchedules_PriorConfig_NoMatchingVMX_DefaultsToNone` in
`internal/tui/init_test.go` currently uses a VM whose `Name` ("dev")
still matches the prior VM's `Name`, only the `VMX` differs -- that's
exactly the "moved, not renamed" case this task fixes, so its name and
expected outcome are both about to become wrong. Replace it with a test
of a *genuine* non-match (both `Name` and `VMX` differ):

```go
func TestPromptSchedules_PriorConfig_NoMatchAtAll_DefaultsToNone(t *testing.T) {
	vms := []config.VM{{Name: "dev2", VMX: "/vms/dev2.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty -- no prior VM shares this name or VMX", vms[0].Schedule)
	}
}
```

- [ ] **Step 2: Write the failing test for the name-match fallback**

Add to `internal/tui/init_test.go`:

```go
func TestPromptSchedules_PriorConfig_VMXChangedButNameMatches_FallsBackToName(t *testing.T) {
	// Same Name, different VMX -- a bundle moved to a different search
	// directory without being renamed (discoverVMs' Name comes from the
	// bundle folder name, which a plain move leaves unchanged).
	vms := []config.VM{{Name: "dev", VMX: "/vms/other-dir/dev.vmwarevm/dev.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx", Schedule: "daily"}}}
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "daily" {
		t.Errorf("Schedule = %q, want %q (seeded via name fallback despite the changed VMX)", vms[0].Schedule, "daily")
	}
}
```

- [ ] **Step 3: Write the failing test for the unmatched-schedule warning**

```go
func TestPromptSchedules_PriorConfig_UnmatchedScheduledVM_PrintsWarning(t *testing.T) {
	// "old" matches neither Name nor VMX of anything in vms -- a true
	// rename (or removal), which can't be auto-matched, so this must be
	// surfaced instead of silently dropped.
	vms := []config.VM{{Name: "new", VMX: "/vms/new.vmwarevm/new.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "old", VMX: "/vms/old.vmwarevm/old.vmx", Schedule: "daily"}}}
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty -- \"new\" has no prior match of its own", vms[0].Schedule)
	}
	if !strings.Contains(out.String(), "old") {
		t.Errorf("output = %q, want a warning naming the unmatched prior VM %q", out.String(), "old")
	}
}

func TestPromptSchedules_PriorConfig_AllMatched_NoWarning(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if strings.Contains(out.String(), "warning") {
		t.Errorf("output = %q, want no warning -- the only prior VM was matched", out.String())
	}
}
```

- [ ] **Step 4: Run to verify the new/changed tests fail**

Run: `go test ./internal/tui/... -run TestPromptSchedules -v`
Expected: `TestPromptSchedules_PriorConfig_VMXChangedButNameMatches_FallsBackToName`
and `TestPromptSchedules_PriorConfig_UnmatchedScheduledVM_PrintsWarning`
FAIL (schedule stays `""` instead of `"daily"`; no warning text present).
`TestPromptSchedules_PriorConfig_NoMatchAtAll_DefaultsToNone` and
`TestPromptSchedules_PriorConfig_AllMatched_NoWarning` should already
PASS against the old code (they're here to lock in behavior the fix
must not change).

- [ ] **Step 5: Implement the name fallback and warning in promptSchedules**

In `internal/tui/init.go`, replace `promptSchedules`'s current body:

```go
func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM, prior *config.Config) error {
	priorByVMX := make(map[string]string, len(vms))
	if prior != nil {
		for _, p := range prior.VMs {
			priorByVMX[p.VMX] = p.Schedule
		}
	}

	for i := range vms {
		choice := scheduleChoiceNone
		if schedule, ok := priorByVMX[vms[i].VMX]; ok {
			choice = scheduleChoiceFor(schedule)
		}

		err := runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Schedule for %s", vms[i].Name)).
					Options(huh.NewOptions(scheduleChoices...)...).
					Value(&choice),
			),
		)
		if err != nil {
			return err
		}
		vms[i].Schedule = resolveSchedule(choice)
	}
	return nil
}
```

with:

```go
func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM, prior *config.Config) error {
	priorByVMX := make(map[string]string, len(vms))
	priorByName := make(map[string]string, len(vms))
	if prior != nil {
		for _, p := range prior.VMs {
			priorByVMX[p.VMX] = p.Schedule
			priorByName[p.Name] = p.Schedule
		}
		if err := warnUnmatchedPriorSchedules(out, prior.VMs, vms); err != nil {
			return err
		}
	}

	for i := range vms {
		choice := scheduleChoiceNone
		if schedule, ok := priorByVMX[vms[i].VMX]; ok {
			choice = scheduleChoiceFor(schedule)
		} else if schedule, ok := priorByName[vms[i].Name]; ok {
			// VMX changed but the bundle's folder name (and thus Name,
			// per discoverVMs) didn't -- a bundle moved between search
			// directories, not renamed. Falls back here rather than
			// defaulting to "none" and letting the mandatory post-write
			// auto-sync (internal/cli/init.go) delete a real,
			// still-correct LaunchAgent.
			choice = scheduleChoiceFor(schedule)
		}

		err := runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Schedule for %s", vms[i].Name)).
					Options(huh.NewOptions(scheduleChoices...)...).
					Value(&choice),
			),
		)
		if err != nil {
			return err
		}
		vms[i].Schedule = resolveSchedule(choice)
	}
	return nil
}

// warnUnmatchedPriorSchedules prints a warning naming every VM in
// priorVMs that had a non-empty Schedule but matches neither the VMX
// nor the Name of any VM in vms this run. Unlike a VMX-only mismatch
// (handled by promptSchedules' name fallback above), a VM matching
// neither field was very likely renamed -- discoverVMs derives Name
// from the bundle's own folder name, so renaming the bundle changes
// both fields at once, and there is no reliable way to auto-match it
// back to its prior entry. Silently defaulting a case like this to
// "none" is exactly how the mandatory post-write auto-sync
// (internal/cli/init.go) ends up deleting a real, working LaunchAgent
// with no warning to the user before they confirm.
func warnUnmatchedPriorSchedules(out io.Writer, priorVMs, vms []config.VM) error {
	presentVMX := make(map[string]bool, len(vms))
	presentName := make(map[string]bool, len(vms))
	for _, v := range vms {
		presentVMX[v.VMX] = true
		presentName[v.Name] = true
	}

	var lost []string
	for _, p := range priorVMs {
		if p.Schedule == "" {
			continue
		}
		if presentVMX[p.VMX] || presentName[p.Name] {
			continue
		}
		lost = append(lost, p.Name)
	}
	if len(lost) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(out, "warning: %d previously-scheduled VM(s) not found under their prior name or path this run -- their schedule was not carried forward: %s\n", len(lost), strings.Join(lost, ", "))
	return err
}
```

- [ ] **Step 6: Run to verify the tests pass**

Run: `go test ./internal/tui/... -run TestPromptSchedules -v`
Expected: PASS — all 8 `TestPromptSchedules_*` tests (5 pre-existing
plus the one replaced and 3 new from this task).

- [ ] **Step 7: Run the full tui package tests**

Run: `go test ./internal/tui/... -v`
Expected: PASS, including `TestRunInitWizard_Force_PreservesExistingVMSchedule`
(unaffected -- that test's VM matches by both `Name` and `VMX`, so it
never touches the new fallback or warning path).

- [ ] **Step 8: Run the full suite and lint**

Run: `go test ./... && make lint`
Expected: PASS, no findings

- [ ] **Step 9: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go
git commit -m "$(cat <<'EOF'
fix(tui): fall back to name match and warn on schedule loss

promptSchedules seeded a VM's prior schedule by VMX match only, so a
bundle moved between search directories (folder name unchanged, path
changed) silently defaulted to "none" -- and the mandatory post-write
auto-sync then deleted its real LaunchAgent. It now falls back to
matching by VM Name when VMX doesn't match, and prints a warning naming
any previously-scheduled VM that matches neither field (a true rename,
which can't be auto-matched) so the user notices before confirming.

Fixes #91.
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** all three issues (#89, #90, #91) have a task each,
  with the exact file/line the review flagged as the entry point.
- **Backward compatibility:** Task 1 deliberately leaves `config.Validate`
  itself unchanged (still rejects raw cron/garbage schedules) --
  migration only happens in `Load`, so `TestValidate_RejectsUnknownSchedule`
  and `ValidateVMs` (used by the wizard/`vm add` when building a fresh
  config) keep rejecting bad input from anything that isn't reading an
  existing `config.yaml`.
- **Task 2 ordering:** the running-check happens *before* `Write`, not
  after -- Step 7's code comment calls this out explicitly, and
  `TestSync_UpdatePath_SkipsWhenBackupCurrentlyRunning`'s third `Sync`
  call is there specifically to prove the deferred update isn't
  permanently stuck.
- **Task 3 scope:** deliberately does not attempt to resolve the
  general "VM dropped from config entirely" case -- that's #88, already
  filed and out of scope here; this task only fixes the narrower
  VMX-changed-but-still-present case its own issue names.
