# ADR-005: launchd Scheduling — Unattended Backups via LaunchAgents

**Status:** Implemented (branch `feat/launchd-scheduling-design`)
**Date:** 2026-09-11
**Deciders:** Tim

Implementation notes are inline below where the shipped code refined this
design; `internal/launchd` is authoritative for anything the two disagree
on. Plan: `docs/superpowers/plans/2026-09-11-launchd-scheduling.md`.

## Scope

**In scope:**

- `config.VM.Schedule` narrows from a free-form string (previously
  documented as raw cron syntax, e.g. `"0 2 * * *"`) to a closed enum:
  `""` (unscheduled), `"daily"`, `"weekly"`, `"monthly"`. `config.Load`
  validates it against exactly those four values.
- `internal/launchd` (new package): builds a `LaunchAgent` plist per
  scheduled VM and drives `launchctl bootstrap`/`bootout` against it,
  behind a small interface so the choreography-style unit tests don't
  shell out — same pattern as `vm.Controller`/`vm.FakeVMController`.
- Plist naming/location: `com.tim.snapback.<sanitized-vm-name>.plist` in
  `~/Library/LaunchAgents/`. VM-name sanitization itself (collision
  detection, the sanitizer function) is filed separately as
  [#84](https://github.com/xortim/snapback/issues/84) — this ADR assumes
  a `sanitizeLabel(name string) string` function exists with that
  contract, but doesn't design it.
- `snapback schedule sync`: reconciles every configured VM's plist
  against `config.yaml` — write+`bootstrap` for a VM with a `schedule`
  whose plist is missing or stale, `bootout`+delete for a VM whose
  plist exists but `schedule` is now empty or the VM itself is gone.
  Idempotent, safe to re-run.
- Auto-sync wiring: `vm add`, `vm remove <name>`, and the `init` wizard
  each call the same sync logic for the VM(s) they touch, as a side
  effect, so the common path needs no extra command.
- `init`/`vm add` wizard changes (`internal/tui/init_schedule.go`,
  `internal/tui/vmadd.go`): schedule choices become
  `none`/`daily`/`weekly`/`monthly` (renamed from
  `none`/`nightly`/`weekly`/`custom`); the always-asked custom-cron field
  and `resolveSchedule`'s cron-string outputs are removed.
- Scheduled-run log files at `~/Library/Logs/snapback/<sanitized-vm-name>.log`
  (plist `StandardOutPath`/`StandardErrorPath`), with simple built-in
  size-based rotation in `run` itself.

**Out of scope (this ADR), tracked as follow-ups:**

- [#84](https://github.com/xortim/snapback/issues/84) — VM-name
  sanitization for plist labels/filenames (collision detection, the
  sanitizer itself).
- [#85](https://github.com/xortim/snapback/issues/85) — detecting and
  warning on drift between `config.yaml`'s `schedule` and what's actually
  installed under `~/Library/LaunchAgents/` (e.g. from a hand-edited
  config that hasn't gone through `vm add`/`vm remove`/`init`/`schedule
  sync` since).
- [#86](https://github.com/xortim/snapback/issues/86) — osascript
  success/failure notifications. `docs/design.md`'s original Phase 2
  definition bundled these with scheduling; split out here to keep this
  change reviewable. `progress.Notifying` remains an unused stage after
  this ADR, same as before it.
- `run --all` — not needed by this design (see Context) and remains
  unimplemented.
- System `newsyslog.d`-based log rotation — would be the more
  conventional macOS approach but requires a one-time `sudo` write under
  `/etc`, a first for this tool; simple in-process rotation was chosen
  instead specifically to avoid that (see Architecture).

## Context

Phase 1 (#1) and Phase 3 (Restore) have both landed
(`docs/superpowers/specs/2026-09-11-restore-design.md`); `docs/design.md`'s
Roadmap marks Phase 2 — Scheduling as the next unstarted phase. Its
original text bundled three things together: launchd plist generation
from `schedule`, `run --all`, and osascript notifications. This ADR
scopes only the first, for two reasons surfaced while brainstorming it:

1. **Notifications are a distinct, separable concern** (already unwired
   at the `progress.Notifying` stage) with no dependency on how
   scheduling itself works — bundling it in only makes this change
   harder to review, not more coherent.
2. **`run --all` turned out to be unnecessary**, once the per-VM
   scheduling model below was chosen. The design initially considered
   here was one plist per *unique schedule string* (so two VMs sharing a
   schedule would share one LaunchAgent), which would have needed either
   `run --all` or a new multi-VM invocation flag to fire both VMs'
   backups from one plist. That was rejected — a repeatable-VM flag
   invents new CLI surface just to support batching, and a shell-wrapper
   plist (`sh -c 'snapback run --vm A && snapback run --vm B'`) has wrong
   failure semantics (one VM failing skips the rest via `&&`) and
   embeds VM names into generated shell strings inside the plist. The
   simpler model — **one plist per VM**, always calling
   `snapback run --vm <name>` — sidesteps all of this: two VMs sharing a
   schedule just means two independent plists firing at the same
   wall-clock time, which launchd handles natively, and it matches the
   existing per-VM advisory lock (`internal/backup.Lock`) where each
   VM's backup is already fully independent of every other VM's.

Config's `schedule` field also simplifies in this ADR. It was previously
documented (`docs/design.md`, the `init` wizard's `resolveSchedule`) as
accepting raw cron syntax, with the wizard offering
`none`/`nightly`/`weekly`/`custom` presets that resolved to cron strings
like `"0 2 * * *"`. Nothing has ever consumed that field — this ADR is
the first thing that does — so there's no migration burden in narrowing
it to a closed `daily`/`weekly`/`monthly` enum matching cron's own
`@daily`/`@weekly`/`@monthly` meta-schedules (fixed midnight-based times,
no configurable time-of-day). This removes an entire class of
translation bugs a general cron-string-to-`StartCalendarInterval`
parser would otherwise need to handle, at the cost of not letting an
operator pick, say, 3am instead of midnight — accepted as the right
trade for a first version; a time override can be added later without
changing the enum's shape (`schedule: weekly` stays valid, an optional
field would just extend it).

## Architecture

### The Day/Weekday `StartCalendarInterval` trap

Apple's `launchd.plist(5)` documents two facts about
`StartCalendarInterval` that directly shape how this ADR generates
plists:

- Missing keys are wildcards ("fire whenever" for that field).
- **If both `Day` and `Weekday` are present in the same dict, they are
  OR'd, not AND'd** — "the job will be started if either one matches the
  current date."

The natural-looking Go representation of a calendar interval is one
struct covering all five fields:

```go
type CalendarInterval struct {
    Minute, Hour, Day, Weekday, Month int
}
```

This is a trap: a plain `int` field's Go zero value is indistinguishable
from an intentionally-specified `0`, and typical plist encoders (and any
hand-rolled `map[string]int` built from this struct without care) will
serialize every field, whether the caller meant to set it or not. Given
`CalendarInterval{Day: 1, Hour: 0, Minute: 0}` for the monthly preset,
`Weekday` isn't touched — but its zero value (`0` = Sunday) still exists
on the struct and would still be serialized as `Weekday: 0` if the
encoding path doesn't specifically omit unset fields. Once the plist
holds *both* `Day: 1` and `Weekday: 0`, the OR rule engages: the job
fires on the 1st of the month **and** every Sunday — roughly four or
five unintended extra runs a month, not once. The same risk applies in
reverse to the weekly preset if `Day` leaks in as an implicit `0`.

**Fix:** there is no shared all-fields struct. Each preset builds its
own `map[string]int` containing *only* the keys it needs:

```go
func calendarInterval(schedule string) map[string]int {
    switch schedule {
    case "daily":
        return map[string]int{"Hour": 0, "Minute": 0}
    case "weekly":
        return map[string]int{"Weekday": 0, "Hour": 0, "Minute": 0} // Sunday
    case "monthly":
        return map[string]int{"Day": 1, "Hour": 0, "Minute": 0} // 1st
    default:
        return nil // "" (unscheduled) — caller never generates a plist
    }
}
```

`Day` and `Weekday` are never both present in the same map, so the OR
rule can't engage for any preset this ADR generates. See Testing for how
this is verified beyond code inspection.

### `internal/launchd` package

```go
package launchd

// Agent describes one VM's scheduled backup job.
type Agent struct {
    Label        string // com.tim.snapback.<sanitized-vm-name>
    VMName       string // config.VM.Name, pre-sanitization
    BinaryPath   string // path to the running snapback binary (os.Executable())
    LogPath      string // ~/Library/Logs/snapback/<sanitized>.log
    Interval     []calendarKey // the schedule, already resolved to plist keys
}

// Installer is the shell-out boundary, mirroring vm.Controller's shape:
// choreography-level code (Sync, below) is written and tested against
// FakeInstaller, never against the real launchctl-backed one directly.
type Installer interface {
    Write(agent Agent) (plistPath string, changed bool, err error) // renders + writes the plist file
    Bootstrap(plistPath string) error                              // launchctl bootstrap gui/<uid> <plistPath>
    Bootout(label string) error                                    // launchctl bootout gui/<uid>/<label>
    Remove(label string) error                                     // deletes the plist, tolerating already-gone
    List() ([]string, error)                                       // labels of every com.tim.snapback.* plist on disk
}

// LaunchctlInstaller is the real implementation (internal/launchd/installer.go).
type LaunchctlInstaller struct{ Dir string } // Dir is settable so integration tests use a scratch dir

// FakeInstaller backs unit tests (internal/launchd/fake.go), recording
// calls in memory — same role vm.FakeVMController plays for internal/backup.
type FakeInstaller struct{ /* ... */ }
```

**Refined during implementation** — the sketch above is the shipped
shape; `internal/launchd/installer.go` and `plist.go` are authoritative.
Three deltas from this ADR's original draft, all found while writing
`Sync`:

- `Installer` gained a fifth method, `List()`, because `Sync` needs to
  discover plists for VMs no longer in the config at all (there's no
  `Agent` to ask about them). This is also the source of the known
  disk-vs-loaded limitation noted in Risks below.
- `Write` returns a `changed bool` so `Sync` can distinguish "wrote a
  new plist" from "content was already identical" and skip a pointless
  bootout/bootstrap cycle. `Remove` takes a `label`, not a `plistPath`,
  so the installer owns path construction on both ends.
- `Agent` carries the already-resolved `Label` and `Interval` rather than
  the raw `Schedule` string; `buildAgent(config.VM, binaryPath)` does the
  sanitization and `calendarInterval` lookup once, so `renderPlist` never
  re-parses a schedule.

Also shipped beyond the sketch: the generated plist sets
`EnvironmentVariables.PATH` to
`/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`.
launchd's default agent PATH excludes both Homebrew prefixes, and
`internal/backup/archive.go` resolves `zstd` via `exec.LookPath` with a
*silent* gzip fallback — without this, a Homebrew-zstd user would get
zstd archives from a manual `run` and gzip from the identical scheduled
one. And `Sync` boots out before bootstrapping on the install path too,
not just the update path: `List()` only sees disk, so a job whose plist
was hand-deleted is still loaded, and `Bootstrap` fails against an
already-loaded label. `Bootout` is idempotent for an unloaded label, so
the extra call costs nothing on the normal path.

`Sync(installer Installer, vms []config.VM) (SyncResult, error)` is the
package's one piece of choreography: for each VM with a non-empty
`Schedule`, ensure its plist exists and matches (write + bootstrap if
missing or changed); for each VM with an empty `Schedule` or no longer
present in `vms` at all but with a plist still on disk, bootout + remove
it. `SyncResult` records what changed (installed/updated/removed VM
names) so `schedule sync`'s CLI output and `vm add`/`vm remove`'s can
report it. `Sync` is what `internal/cli/schedule.go`,
`internal/cli/vm.go` (`vm add`/`vm remove`), and `internal/cli/init.go`
all call — there's exactly one reconciliation implementation, invoked
from four places.

`launchctl bootstrap`/`bootout` target the modern per-user GUI domain
(`gui/<uid>`, `id -u` at runtime) rather than the deprecated
`load`/`unload` subcommands, per current `launchctl(1)` guidance.

### CLI command

```
snapback schedule sync
```

No flags — it always reconciles every VM in the loaded config. Output
lists what changed (`installed: <vm>`, `updated: <vm>`, `removed: <vm>`)
or `nothing to do` if config already matches installed state.
`internal/cli/schedule.go` follows the existing thin-command pattern
(`newScheduleCmd` → `newScheduleSyncCmd`, a `scheduleDeps` struct for
`loadConfig`/`newInstaller`, mirroring `cleanup.go`'s shape as the
closest existing analog — a reconciliation command with no per-VM
argument).

`vm add`, `vm remove <name>`, and `init`'s wizard completion each call
`launchd.Sync` with the post-change VM list after writing `config.yaml`,
not before — sync always reconciles against what was just persisted.

### Wizard changes

`internal/tui/init_schedule.go`'s `scheduleChoices` becomes
`{"none", "daily", "weekly", "monthly"}`. `resolveSchedule` drops its
`custom`/`cronNightly`/`cronWeekly` machinery entirely and becomes a
direct passthrough:

```go
func resolveSchedule(choice string) string {
    if choice == scheduleChoiceNone {
        return ""
    }
    return choice // "daily" | "weekly" | "monthly" already match config.VM.Schedule's enum
}
```

The always-asked custom-cron text field (kept unconditional today
specifically to work around `huh`'s accessible-mode form runner not
respecting `Group.WithHideFunc`, per the existing comment) is deleted
outright rather than reworked — there's no longer a "custom" choice for
it to serve, in either form-runner mode.

### Log rotation

`run`'s entry point, before writing any output, stats its own
deterministic log path (`~/Library/Logs/snapback/<sanitized-vm-name>.log`
— computed the same way regardless of whether this invocation came from
a human or launchd) and rotates in place if it's past a size threshold
(5MB): `.log` → `.log.1` → `.log.2`, deleting anything beyond `.log.2`
(keep 3 generations total). Pure Go (`os.Rename`/`os.Remove`), no `sudo`,
no dependency on `/etc/newsyslog.d` — deliberately, since every other
part of this tool runs unprivileged and this ADR isn't the place to
introduce the first exception.

## Testing

- `internal/launchd`: `calendarInterval` unit tests assert the *exact*
  key set per preset (`map[string]int` equality — daily has no `Day`/
  `Weekday`/`Month` key at all, weekly has no `Day` key, monthly has no
  `Weekday` key), not just "looks right" by inspection. A second test
  simulates a full calendar year (any non-leap and one leap year) date
  by date against each preset's generated dict and asserts an exact
  fire count (365/366 for daily, 52 or 53 for weekly, 12 for monthly) —
  this is the test that would have caught the Day/Weekday OR bug if the
  shared-struct version had shipped instead.
- `launchd.Sync` unit tests against `FakeInstaller`: install for a newly
  scheduled VM, update for a changed schedule, removal for a
  now-unscheduled VM and for a VM no longer in the list, no-op when
  already in sync, and that a VM with `Schedule == ""` never gets a
  `Write`/`Bootstrap` call at all.
- `LaunchctlInstaller` is exercised only in the existing
  `SNAPBACK_INTEGRATION=1`-gated suite (real `launchctl bootstrap`/
  `bootout` against a disposable label), same split as `vmcli`/`vmrun`.
- `internal/cli/schedule_internal_test.go`: `schedule sync`'s
  install/update/remove/no-op output text, via `scheduleDeps` +
  `FakeInstaller`.
- `internal/cli/vm_internal_test.go` / `init_internal_test.go` gain
  assertions that `vm add`/`vm remove`/`init` call sync with the
  post-write VM list (via a fake installer/sync hook), not that they
  duplicate `launchd.Sync`'s own reconciliation-logic tests.
- `internal/tui/init_schedule_test.go`: updated for the four-choice enum;
  the custom-cron-field test cases are deleted along with the field.
- Log rotation: unit test pre-creating an oversized log file, asserting
  the rename chain and that a fourth generation is deleted.

## Risks & Open Questions

- **No time-of-day override**, as noted in Scope/Context — daily is
  always midnight, weekly always Sunday midnight, monthly always the
  1st at midnight. Accepted trade for this version; revisit if a real
  VM's backup window conflicts with midnight (e.g. actually in active
  use then).
- **`os.Executable()` for `BinaryPath`** resolves to wherever the
  currently-running binary lives, which is fine for `schedule sync`/
  `vm add`/`vm remove`/`init` (interactively invoked, binary is wherever
  the user installed it) but means a plist's `ProgramArguments[0]`
  is fixed at sync time — if the binary is later moved (not
  reinstalled in place), scheduled runs break silently until the next
  sync. This is a real gap; it's the same class of problem
  [#85](https://github.com/xortim/snapback/issues/85) (drift detection)
  would help with, so not solved separately here.
- **launchd's own sleep/wake coalescing** ("if multiple intervals
  transpire before the computer is woken, those events will be coalesced
  into one event upon wake") means a laptop asleep through its scheduled
  time gets exactly one catch-up run on wake, not zero and not multiple
  — worth knowing, not a defect to design around.
- **`Sync` reads disk, not launchd's loaded state** (found during
  implementation). `Installer.List()` globs
  `~/Library/LaunchAgents/com.tim.snapback.*.plist`, so a plist that
  exists on disk with correct content but *isn't* actually bootstrapped
  — after an interrupted sync, or a manual `launchctl bootout` — reads
  as "already in sync" indefinitely, and that VM silently stops backing
  up until the next config change forces a rewrite. The opposite
  direction is handled: `Sync` boots out before bootstrapping on both
  the install and update paths, so a loaded job with no plist on disk
  can't make `Bootstrap` fail. Closing the first direction needs a
  `launchctl print`-backed loaded-state check on the `Installer`
  interface; folded into
  [#85](https://github.com/xortim/snapback/issues/85)'s drift-detection
  scope rather than solved here.
- **Sanitization ([#84](https://github.com/xortim/snapback/issues/84))
  is a hard dependency**, not a nice-to-have: without collision
  detection, two VMs whose names sanitize to the same label would
  silently share one `LaunchAgent`, and only one of them would actually
  ever run. This ADR's `Agent`/`Sync` design assumes `sanitizeLabel`
  exists and collisions are rejected before `Sync` ever calls `Write` —
  #84 should land before or alongside this work, not after.
