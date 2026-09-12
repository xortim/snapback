# init --force Schedule Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `snapback init --force` from silently resetting every VM's
schedule to "none" and, via the auto-sync this PR wires in, deleting
their real LaunchAgents.

**Architecture:** `promptSchedules` (`internal/tui/init.go`) always
starts every VM's schedule prompt at `scheduleChoiceNone`, ignoring the
`prior *config.Config` that `promptCoreSettings` already uses to seed
destination/compression/retention/notifications. Thread the same
`prior` through `promptSchedules`, seed each VM's starting choice from
the matching prior VM's `Schedule` (matched by `VMX`, the one field
guaranteed unique per bundle), and update the doc comments that
currently claim schedules are unaffected by `prior`.

**Tech Stack:** Go, `github.com/charmbracelet/huh` (the TUI form
library already in use).

**Spec:** This is a targeted bug fix from PR #87 code review, not a new
feature with its own spec. Background:
`docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md` (ADR-005)
and `CLAUDE.md`'s "Phase 2's scheduling half" section describe the
shipped scheduling feature this fix protects.

## Global Constraints

- Match existing code style exactly: doc comments above every
  non-trivial function explaining *why*, not *what*; table-driven tests
  matching the style already in `internal/tui/init_schedule_test.go` and
  `internal/tui/init_test.go`.
- No new dependencies.
- `go test ./...` (unit, fake-only) and `make lint` must both pass
  before this is done — see `make test` / `make lint` in the Makefile.
- Do not touch `internal/cli/vm.go` (`vm add`/`vm remove`) — they load
  `cfg.VMs` straight from disk and only prompt schedules for *newly
  added* VMs, so they don't have this bug.

---

### Task 1: Seed `init --force`'s per-VM schedule prompts from the prior config

**Files:**
- Modify: `internal/tui/init_schedule.go` — add a reverse mapping from a
  `config.VM.Schedule` value back to a `scheduleChoices` entry.
- Modify: `internal/tui/init.go` — `promptSchedules` (around line
  298-318) gains a `prior *config.Config` parameter and seeds each VM's
  starting choice from it; `RunInitWizard` (around line 390-448) passes
  `prior` through and its doc comment is corrected.
- Modify: `internal/cli/init.go` — the `--force` doc comment (lines
  123-130) currently only names
  destination/compression/retention/notifications; extend it to also
  name schedules, now that this is true.
- Test: `internal/tui/init_schedule_test.go` — new table test for the
  reverse mapping.
- Test: `internal/tui/init_test.go` — update the three existing
  `promptSchedules(...)` call sites to pass `nil`, add three new
  `promptSchedules` prior-seeding tests, add one `RunInitWizard`
  end-to-end test.

**Interfaces:**
- Consumes: `config.VM{Name, VMX, Schedule string}`
  (`internal/config/config.go:27-34`), `config.Config.VMs []config.VM`,
  the existing `scheduleChoiceNone`/`scheduleChoiceDaily`/
  `scheduleChoiceWeekly`/`scheduleChoiceMonthly`/`scheduleChoices`/
  `resolveSchedule` (`internal/tui/init_schedule.go`), `runForm` and
  `huh` usage already in `promptSchedules`.
- Produces: `scheduleChoiceFor(schedule string) string` (new — the
  reverse of `resolveSchedule`); `promptSchedules(ctx context.Context,
  in io.Reader, out io.Writer, accessible bool, vms []config.VM, prior
  *config.Config) error` (changed signature — adds the trailing `prior`
  param). Nothing outside `internal/tui` calls `promptSchedules`
  directly (only `RunInitWizard` does), so this is not a breaking change
  to any other package.

- [ ] **Step 1: Write the failing test for the reverse schedule mapping**

Add to `internal/tui/init_schedule_test.go`, after `TestResolveSchedule`:

```go
func TestScheduleChoiceFor(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		want     string
	}{
		{"empty resolves to none", "", scheduleChoiceNone},
		{"daily passes through", "daily", scheduleChoiceDaily},
		{"weekly passes through", "weekly", scheduleChoiceWeekly},
		{"monthly passes through", "monthly", scheduleChoiceMonthly},
	}
	for _, tt := range tests {
		if got := scheduleChoiceFor(tt.schedule); got != tt.want {
			t.Errorf("%s: scheduleChoiceFor(%q) = %q, want %q", tt.name, tt.schedule, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

Run: `go test ./internal/tui/... -run TestScheduleChoiceFor -v`
Expected: FAIL — `undefined: scheduleChoiceFor`

- [ ] **Step 3: Implement `scheduleChoiceFor`**

In `internal/tui/init_schedule.go`, add below `resolveSchedule`:

```go
// scheduleChoiceFor turns a config.VM.Schedule value back into the
// matching scheduleChoices entry -- the reverse of resolveSchedule.
// Used to seed promptSchedules' per-VM default from a prior config's
// existing schedule instead of always starting at "none": every choice
// except "none" already doubles as its own Schedule value (see
// resolveSchedule's doc comment), so the only special case is the
// unscheduled "" value, which maps back to "none".
func scheduleChoiceFor(schedule string) string {
	if schedule == "" {
		return scheduleChoiceNone
	}
	return schedule
}
```

- [ ] **Step 4: Run the test again to confirm it passes**

Run: `go test ./internal/tui/... -run TestScheduleChoiceFor -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init_schedule.go internal/tui/init_schedule_test.go
git commit -m "feat(tui): add reverse schedule-choice mapping"
```

- [ ] **Step 6: Write the failing tests for `promptSchedules` prior-seeding**

First, update the three existing `promptSchedules(...)` calls in
`internal/tui/init_test.go` (lines 389, 403, 420) to pass `nil` as the
new trailing argument, e.g.:

```go
	if err := promptSchedules(context.Background(), in, &out, true, vms, nil); err != nil {
```

(apply the same `, nil` addition to the other two call sites at lines
403 and 420).

Then add these three new tests directly after
`TestPromptSchedules_MultipleVMs_AskedInOrder` (after line 432):

```go
func TestPromptSchedules_PriorConfig_SeedsExistingScheduleAsDefault(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}
	// Blank accepts whatever's pre-filled -- if prior weren't wired
	// through, this blank would resolve to "none" instead.
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "daily" {
		t.Errorf("Schedule = %q, want %q (seeded from prior config)", vms[0].Schedule, "daily")
	}
}

func TestPromptSchedules_PriorConfig_NoMatchingVMX_DefaultsToNone(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev-renamed.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}
	in := strings.NewReader("\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty -- no prior VM shares this VMX", vms[0].Schedule)
	}
}

func TestPromptSchedules_PriorConfig_CanStillBeChangedExplicitly(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	prior := &config.Config{VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}
	// "1" explicitly selects scheduleChoices[0] ("none") despite the
	// seeded "daily" default -- the seed only changes what a blank
	// answer accepts, not what's selectable.
	in := strings.NewReader("1\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms, prior); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty -- explicit choice should override the seeded default", vms[0].Schedule)
	}
}
```

- [ ] **Step 7: Write the failing end-to-end `RunInitWizard` test**

Add to `internal/tui/init_test.go`, directly after
`TestRunInitWizard_EndToEnd_DiscoveredVMWithDefaults` (after line 495):

```go
func TestRunInitWizard_Force_PreservesExistingVMSchedule(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	prior := &config.Config{
		VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx", Schedule: "daily"}},
	}
	// Same input sequence as TestRunInitWizard_EndToEnd_DiscoveredVMWithDefaults
	// -- prior doesn't add or remove any prompts, it only changes what a
	// blank answer resolves to.
	// VM select: "0" (confirm default selection), "n" (decline manual).
	// Core settings: 6 blanks, all defaults.
	// Schedule (1 VM): blank -- must keep "daily" from prior, not reset
	// to "none".
	// Review: blank (accept default "write? [Y/n]" = yes).
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n")
	var out bytes.Buffer

	cfg, err := RunInitWizard(context.Background(), in, &out, true, candidates, prior)
	if err != nil {
		t.Fatalf("RunInitWizard() error = %v", err)
	}
	if len(cfg.VMs) != 1 || cfg.VMs[0].Schedule != "daily" {
		t.Fatalf("cfg.VMs = %+v, want the existing VM's \"daily\" schedule preserved from prior", cfg.VMs)
	}
}
```

- [ ] **Step 8: Run all the new tests to confirm they fail**

Run: `go test ./internal/tui/... -run 'TestPromptSchedules|TestRunInitWizard_Force_PreservesExistingVMSchedule' -v`
Expected: FAIL — the three `TestPromptSchedules_*` tests fail to
compile (too many arguments in call to `promptSchedules`), and once
those three are temporarily commented out to isolate it,
`TestRunInitWizard_Force_PreservesExistingVMSchedule` fails on the
assertion (`cfg.VMs[0].Schedule` is `""`, not `"daily"`) since
`RunInitWizard` doesn't pass `prior` to `promptSchedules` yet. In
practice, since all four tests are new/changed in the same commit, it's
enough to see the compile failure here and move straight to
implementing Step 9 — the compile failure already proves none of this
code exists yet.

- [ ] **Step 9: Change `promptSchedules`'s signature and seed from prior**

In `internal/tui/init.go`, replace the existing `promptSchedules`
function (lines 298-318) with:

```go
// promptSchedules asks a schedule preset for each VM in vms, in order,
// mutating vms[i].Schedule in place. When prior is non-nil (an existing
// config.yaml found under `init --force`, per #52), each VM's starting
// choice is seeded from the matching prior VM's existing Schedule --
// matched by VMX, the one field guaranteed unique per bundle (see
// selectVMs) -- instead of always starting at "none". Without this,
// init --force silently proposed erasing every VM's schedule on each
// rerun; now that runInit auto-syncs LaunchAgents right after writing
// config (internal/cli/init.go), that would go on to actually delete
// working scheduled backups, not just reset a config field. A VM with
// no match in prior (renamed, or newly selected this run) still starts
// at "none", same as a fresh init.
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

Then update `RunInitWizard`'s single call site (currently line 421):

```go
	if err := promptSchedules(ctx, in, out, accessible, vms, prior); err != nil {
		return nil, err
	}
```

And correct `RunInitWizard`'s doc comment (lines 404-406), which
currently reads:

```go
// prior is forwarded to promptCoreSettings -- see its doc comment. VM
// selection and schedules are unaffected by prior; only core settings
// seed from an existing config.
```

Replace it with:

```go
// prior is forwarded to promptCoreSettings and promptSchedules -- see
// their doc comments. VM selection itself is unaffected by prior (a
// fresh init and init --force offer the same discovered candidates);
// core settings and each selected VM's schedule seed their defaults
// from an existing config when prior is non-nil.
```

- [ ] **Step 10: Run all four new/changed tests to confirm they now pass**

Run: `go test ./internal/tui/... -run 'TestPromptSchedules|TestRunInitWizard_Force_PreservesExistingVMSchedule' -v`
Expected: PASS — all three `TestPromptSchedules_PriorConfig_*` tests
plus `TestRunInitWizard_Force_PreservesExistingVMSchedule`.

- [ ] **Step 11: Update the `--force` doc comment in `internal/cli/init.go`**

In `internal/cli/init.go`, the comment above the `if force && exists`
block (lines 123-130) currently reads:

```go
	// --force over an established config: seed the wizard's defaults from
	// what's already there (see internal/tui/init.go's promptCoreSettings
	// doc comment) instead of silently proposing to reset
	// destination/compression/retention/notifications back to factory
	// defaults. A failure to load the existing config doesn't abort
	// init -- --force re-running over a config that's gone stale or
	// unparseable is itself a legitimate reason to run init, so this
	// falls back to the hardcoded defaults instead of blocking that.
```

Replace it with:

```go
	// --force over an established config: seed the wizard's defaults from
	// what's already there (see internal/tui/init.go's promptCoreSettings
	// and promptSchedules doc comments) instead of silently proposing to
	// reset destination/compression/retention/notifications/schedules
	// back to factory defaults or "none" -- runInit auto-syncs
	// LaunchAgents right after writing config below, so a reset schedule
	// here doesn't just change a config field, it deletes a working
	// LaunchAgent. A failure to load the existing config doesn't abort
	// init -- --force re-running over a config that's gone stale or
	// unparseable is itself a legitimate reason to run init, so this
	// falls back to the hardcoded defaults instead of blocking that.
```

- [ ] **Step 12: Run the full unit test suite**

Run: `make test`
Expected: PASS, all packages, including the new and modified tests in
`internal/tui`.

- [ ] **Step 13: Run lint**

Run: `make lint`
Expected: No findings. If `golangci-lint fmt --diff` reports a
formatting diff, run `make fmt-fix` and re-run `make lint`.

- [ ] **Step 14: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go internal/cli/init.go
git commit -m "fix(tui): preserve VM schedules across init --force

init --force always reseeded every VM's schedule prompt from \"none\",
ignoring the prior config. That was harmless on its own, but this
branch also wired runInit to auto-sync LaunchAgents right after writing
config, so a routine --force run for an unrelated setting would
silently delete every VM's real, working scheduled backup. Seed each
VM's schedule prompt from the matching prior VM (by VMX) instead, so a
blank answer keeps what's already there and only an explicit change
clears it."
```

---

## Self-Review Notes

- **Spec coverage:** The one behavior this plan must fix — "`init
  --force` must not silently reset/delete an existing VM's schedule" —
  is covered by Task 1 end to end: the reverse-mapping helper (Step 3),
  the seeding logic in `promptSchedules` (Step 9), the wiring through
  `RunInitWizard` (Step 9), and both unit-level (Step 6) and
  end-to-end (Step 7) tests proving it.
- **Placeholder scan:** No TBD/TODO markers; every step has literal
  code or an exact command.
- **Type consistency:** `promptSchedules`'s new signature
  (`..., vms []config.VM, prior *config.Config`) is used identically at
  its only call site in `RunInitWizard` (Step 9) and in every test call
  (Steps 6, 7). `scheduleChoiceFor(schedule string) string` matches its
  only caller in `promptSchedules`.
