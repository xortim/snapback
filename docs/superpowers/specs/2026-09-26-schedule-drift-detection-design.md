# ADR-006: Schedule Drift Detection

**Status:** Proposed
**Date:** 2026-09-26
**Deciders:** Tim

Plan: `docs/superpowers/plans/2026-09-26-schedule-drift-detection.md` (to
follow).

## Scope

Closes [#85](https://github.com/xortim/snapback/issues/85), filed as a
follow-up from ADR-005
(`docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md`), whose
own Risks section names the same gap twice: no time-of-day override
aside, ADR-005 shipped `Sync` reading *disk* state (`Installer.List()`
globs `~/Library/LaunchAgents/com.tim.snapback.*.plist`) with no check
against launchd's actually-*loaded* state. A plist that's on disk with
correct content but isn't bootstrapped — after an interrupted sync, or a
manual `launchctl bootout` — reads as "already in sync" indefinitely.

**In scope:**

- `Installer.IsLoaded(label string) (bool, error)` — a new method on the
  existing interface, implemented on both `LaunchctlInstaller` (shells to
  `launchctl print`) and `FakeInstaller` (in-memory).
- A shared classification helper used by both `Sync` and a new read-only
  `CheckDrift`, so the two can't disagree about what "in sync" means.
- `Sync` gains a self-heal branch: a VM whose on-disk plist content
  already matches config but isn't loaded gets re-bootstrapped (no
  `Write`, since content didn't change). `SyncResult` gains a `Reloaded`
  field to report this distinctly from `Updated`.
- `launchd.CheckDrift(installer Installer, vms []config.VM, binaryPath
  string) (DriftReport, error)` — a new, read-only function: never calls
  `Write`/`Bootout`/`Bootstrap`/`Remove`, just reports what's out of
  sync, in four categories.
- `snapback status` calls `CheckDrift` and prints one warning line per
  drift category per VM, non-fatal, mirroring the existing
  `warnUndiscoveredVMs`/`warnDamagedDiskChains` pattern in
  `internal/cli/status.go`.

**Out of scope:**

- Fixing the `os.Executable()`-moves-silently-break-plists risk ADR-005
  also names. `CheckDrift` will *surface* that case (rendered content
  differs because `BinaryPath` differs → reports as `OutOfSync`, same as
  any other content drift), which is a reasonable side effect, but
  distinguishing "schedule actually changed" from "binary moved" is not
  attempted — both are legitimately "config and installed state
  disagree."
- Any new command. Detection surfaces only through `status`; fixing
  drift still goes through the existing `snapback schedule sync`.
- `vm add`/`vm remove`/`init`'s auto-sync call sites need no changes —
  they already call `Sync`, which gains the self-heal behavior for free.

## Design

### `Installer.IsLoaded`

```go
// IsLoaded reports whether label is currently bootstrapped into the
// GUI launchd domain, independent of whether a plist for it exists on
// disk (List reports disk state; IsLoaded reports load state — a label
// can be true for one and false for the other in either direction).
IsLoaded(label string) (bool, error)
```

`LaunchctlInstaller.IsLoaded` runs `launchctl print gui/<uid>/<label>`
and reports `false, nil` for the same not-found exit codes/messages
`isNotLoadedError` already tolerates for `bootout` (exit 3, "No such
process", "Could not find service") — any other non-zero exit is a real
error, not "not loaded." `FakeInstaller` gets a `Loaded map[string]bool`
(a label present and `true` counts as loaded), `IsLoadedErr`, and
`IsLoadedCalls`, following its existing per-method conventions
(`ReadErr`/`ReadCalls`, etc.).

### Shared classification

```go
type agentState int

const (
    agentInSync agentState = iota
    agentMissing   // desired, no plist on disk
    agentDiffers   // on-disk content != what buildAgent would render now
    agentNotLoaded // on-disk content matches, but IsLoaded is false
)

func classifyAgent(installer Installer, agent Agent) (agentState, error)
```

`classifyAgent` is `Sync`'s existing install/changed logic
(`installer.Read` + `renderPlist` + `bytes.Equal`) plus one new branch:
when content matches, call `installer.IsLoaded(agent.Label)` and report
`agentNotLoaded` if false. `Sync`'s per-VM loop and `CheckDrift` both
call this instead of independently deriving "changed."

### `Sync`'s self-heal branch

Today, `Sync` only acts on a VM when `classifyAgent` would return
`agentMissing` or `agentDiffers` — an `agentNotLoaded` VM falls through
untouched (`continue`), since content already matches. This adds a third
branch: `agentNotLoaded` still runs the existing running-check →
`Bootout` (idempotent no-op, since it's not loaded) → `Bootstrap`
sequence, but skips `Write` entirely (content's already correct on
disk). `SyncResult` gains:

```go
// Reloaded lists VM names whose on-disk plist content already matched
// config but had to be re-bootstrapped because it wasn't loaded (e.g.
// after a manual `launchctl bootout`, or an interrupted prior sync).
Reloaded []string
```

`IsEmpty()` and `printSyncResult` both extend to cover it —
`"reloaded: %s (was not bootstrapped)\n"`, after `updated:` and before
`skipped:`, matching the existing install → update → skip → remove
ordering.

The running-check (`RunningChecker`) applies to `agentNotLoaded` the
same as `agentDiffers`: `Bootstrap` alone doesn't collide with an
in-progress manual `run`, but a launchd job whose `StartCalendarInterval`
has already elapsed can fire immediately on load, so the existing
skip-if-running gate is reused unchanged rather than special-cased away.

### `CheckDrift`

```go
// DriftReport categorizes every way a VM's actual launchd state can
// disagree with config.yaml. VM names, not labels, throughout except
// Stale (see below) -- callers report per-VM, not per-plist-file.
type DriftReport struct {
    NotInstalled []string // schedule set, no plist on disk yet
    OutOfSync    []string // on-disk plist content differs from config
    NotLoaded    []string // on-disk content matches, but not bootstrapped
    Stale        []string // labels on disk with no corresponding desired VM
}

func (r DriftReport) IsEmpty() bool

func CheckDrift(installer Installer, vms []config.VM, binaryPath string) (DriftReport, error)
```

Builds the same desired-agent set `Sync` does (`buildAgent` per VM with
non-empty `Schedule`), classifies each against `installer.List()`'s
current contents via `classifyAgent`, and separately walks
`existingLabels` for any label with no corresponding desired VM (schedule
cleared or VM removed by hand in `config.yaml`, never followed by
`schedule sync`/`vm remove`) into `Stale`. `Stale` holds labels rather
than VM names — same constraint `SyncResult.Removed` already has,
documented on `Sync`'s `nameByLabel` (a config edited by hand loses the
real name entirely; `ShortLabel` renders it readably).

`CheckDrift` never calls any mutating `Installer` method — only
`List`, `Read`, `IsLoaded`. `DetectCollisions` is not run here; a
colliding config is `Sync`'s (and `persistConfigAndSync`'s) problem to
reject before it's ever written, not `status`'s to re-diagnose.

### CLI wiring

`internal/cli/status.go` gains `warnScheduleDrift`, called from
`runStatus` alongside `warnUndiscoveredVMs`/`warnDamagedDiskChains` (same
non-fatal, print-to-stderr shape: a `newInstaller`/`executable` failure
prints a `"note: could not check schedule drift: %v\n"` line and returns
nil rather than aborting `status`). `statusDeps` gains `newInstaller
func() (launchd.Installer, error)` and `executable func() (string,
error)`, both defaulting the same way `scheduleDeps` already does
(`defaultNewInstaller`, `os.Executable`).

One line per category per VM:

```
warning: %q's schedule (%s) is configured but no LaunchAgent is installed for it -- run `snapback schedule sync`
warning: %q's installed LaunchAgent doesn't match config.yaml -- run `snapback schedule sync`
warning: %q's LaunchAgent is installed but not loaded -- run `snapback schedule sync`
note: LaunchAgent %q has no matching VM in config.yaml -- run `snapback schedule sync` to remove it
```

`Stale` prints as `note:`, not `warning:` — mirroring
`warnUndiscoveredVMs`'s existing `note:`-for-"legitimate, just
unexpected" convention, since a stale plist is inert (booted-out labels
still fire nothing new) rather than a sign backups aren't happening,
which is what `warning:` is reserved for elsewhere in `status.go`.

`status --vm <name>` (the single-VM card view) does not call
`warnScheduleDrift` — same scope cut `runStatusForVM`'s doc comment
already applies to other summary-only checks, and the per-VM view has no
natural place to print a fourth kind of note without cluttering the
card.

## Testing

- `internal/launchd`: `FakeInstaller.IsLoaded` unit tests (default
  unloaded, explicit `Loaded` entries, `IsLoadedErr`, call recording).
- `classifyAgent` unit tests: all four states, plus `Read`/`IsLoaded`
  error propagation.
- `Sync` unit tests: new case for `agentNotLoaded` (Bootout+Bootstrap
  called, `Write` not called, VM lands in `Reloaded` not `Updated`), and
  a RunningChecker-skip case for the same state (lands in `Skipped`, not
  `Reloaded`).
- `CheckDrift` unit tests: each of the four categories individually, a
  combination case (one VM per category in one config), the empty/clean
  case, and `List`/`Read`/`IsLoaded` failures surfacing as errors.
- `LaunchctlInstaller.IsLoaded`: new case in the existing
  `SNAPBACK_INTEGRATION=1`-gated suite
  (`launchctl_integration_test.go`) — bootstrap a disposable label,
  assert `true`; bootout it, assert `false`.
- `internal/cli/status_internal_test.go`: one test per warning line
  (`NotInstalled`/`OutOfSync`/`NotLoaded`/`Stale`), a combination case,
  and a fully-in-sync config producing no drift output at all.
- `internal/cli/schedule_internal_test.go`: `printSyncResult`'s new
  `reloaded:` line.

## Risks & Open Questions

- **`os.Executable()`-moves-silently-break-plists** (named in ADR-005's
  Risks) will now surface as `OutOfSync` the first time `status` runs
  after the binary moves, even though the *schedule* itself never
  changed. Accepted: still a true statement ("installed state doesn't
  match what config.yaml would currently produce"), and running
  `schedule sync` re-renders and fixes it regardless of which field
  actually changed. Not solved differently here.
- **TOCTOU between `CheckDrift` and any subsequent `schedule sync`**:
  `status` reporting drift and a human acting on it are two separate
  invocations: `launchctl` state can change in between (e.g. someone
  else's manual `bootout`). Not a new problem — `Sync` itself has the
  same gap against concurrent changes — and not solved here.
- **`launchctl print`'s exact output/exit codes** are asserted defensively
  the same way `isNotLoadedError` already is (see its own doc comment):
  no live macOS/launchd in CI to confirm the exact wording, so the
  integration suite (gated, manual) is where this gets confirmed against
  a real system, same as the existing `bootout` tolerance.
