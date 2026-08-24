# ADR-002: snapback CLI UX — Progress Reporting and Interactive Commands

**Status:** Proposed
**Date:** 2026-08-23
**Deciders:** Tim

## Scope

**In scope:**

- A `Reporter`/`Event` abstraction that decouples backup/restore choreography from
  how progress is rendered, so choreography stays unit-testable against the fake
  `VMController` exactly as CLAUDE.md already commits to.
- Interactive, bubbletea-based UX for `run`, `init`, and `restore` — the three
  commands with either a long-running pipeline or a decision that benefits from
  a guided flow.
- A non-interactive fallback renderer for `run`, since `run --all` fires from
  `launchd` with no attached TTY.
- Styling for `status` and `list` — no event loop needed, since neither command
  has anything to animate.
- A semantic color palette used consistently across all of the above.

**Out of scope (this ADR):**

- A persistent dashboard/monitor mode (`snapback` with no subcommand staying
  open) — ruled out as duplicating what the existing xbar plugin already covers
  for a single-user tool (see `docs/design.md` Q&A on "why xbar instead of a
  real menu bar app").
- Any change to `status --xbar` output — it stays exactly the plain xbar-format
  text already specified in `docs/design.md#xbar-plugin-output-format`.
- Non-interactive/scriptable flags for `init` or `restore` (e.g. `--yes`,
  `--non-interactive`) — both are inherently interactive by design; scripted
  variants are a later addition if ever needed, not part of phase 1.

## Context

`docs/design.md` fixes the backup choreography (checkTools → snapshot → confirm
→ sync → copy → merge → compress → checksum → prune → notify) and the command
surface, but says nothing about how any of that is presented to a human at the
terminal. The current scaffolding (`internal/cli/root.go`) is four stub
commands with no output behavior at all.

Deciding this now, before phase 1's choreography code is written, matters
because the natural implementation mistake is to have choreography functions
`fmt.Println` directly — which would need to be ripped out later to make room
for a TUI, and would tangle the exact logic CLAUDE.md wants tested against a
fake `VMController` with terminal-rendering concerns.

## Approaches Considered

1. **Charm stack (bubbletea + lipgloss + bubbles + huh), event-driven, with a
   plain-text fallback.** *(Chosen.)* Choreography emits typed events over a
   `Reporter` interface; a bubbletea renderer and a plain-line renderer both
   consume the same event stream, selected by a single TTY check.
2. **Minimal (lipgloss + ANSI spinners, no bubbletea).** Rejected — `init`'s
   VM-picker and `restore`'s archive-picker need real arrow-key list
   interaction, which means hand-rolling raw-mode input handling that
   `bubbles/list` already solves. This approach only wins by avoiding a
   dependency that buys real functionality here.
3. **Persistent dashboard app** (k9s/lazygit-style). Rejected as out of scope
   above — this is a single-user tool with launchd for scheduling and xbar for
   glanceable status; a persistent TUI duplicates both.

## Architecture

### Progress events

A new `internal/progress` package defines the vocabulary choreography and
renderers share:

```go
package progress

type Stage int

const (
    CheckingTools Stage = iota
    Snapshotting
    Copying
    Merging
    Compressing
    Checksumming
    Pruning
    Notifying
    Done
)

type Event struct {
    Stage   Stage
    Message string
    Percent float64 // set for Copying/Compressing, where a byte count is known
    Err     error
}

type Reporter interface {
    Report(Event)
}
```

Choreography functions (`Run`, `Restore` in whatever package ends up owning
the pipeline) take a `Reporter` alongside the `VMController`, calling
`Report()` at each pipeline step. Choreography code never imports
`bubbletea`, `lipgloss`, or `huh` — it only depends on the `Reporter`
interface, which lives in `internal/progress` with no rendering imports of its
own.

Two `Reporter` implementations exist:

- `tui.Reporter` — wraps a `*tea.Program`, forwards each `Event` via
  `Program.Send`.
- `plain.Reporter` — writes one timestamped line per event to the given
  `io.Writer` (stdout for interactive non-TTY runs, whatever `launchd`
  redirects stdout to for scheduled runs).

Command wiring (`internal/cli/*.go`) is the only place that decides which
`Reporter` to construct, via `term.IsTerminal(int(os.Stdout.Fd()))`
(`golang.org/x/term`). Neither `Reporter` implementation nor the choreography
package makes that decision itself.

### Cancellation

The choreography goroutine takes a `context.Context`. On SIGINT, cancellation
propagates through whatever `VMController` call is in flight, and the exit
path reports which `Stage` was active at cancellation time. If that stage is
`Snapshotting` or later (i.e., a `snapback-<timestamp>` snapshot may exist on
the source VM), both renderers print a pointer to `snapback cleanup` — this is
the same orphaned-snapshot failure mode `docs/design.md` already documents for
a hard crash, not a new one.

## Visual Design

### Color palette — semantic status

Chosen over a "Charm Classic" purple/magenta theme and a monochrome-plus-one-accent
theme (both explored and set aside) because color here does more than decorate
the active step — it surfaces the guest-quiesce guarantee (`tools_state` from
`docs/design.md`) at a glance instead of leaving it buried in the manifest:

| Color              | Meaning                                              |
| ------------------ | ----------------------------------------------------- |
| Green (`#04B575`)  | Step complete, fully verified (e.g. tools were `running`, quiesced) |
| Yellow (`#e3b341`) | Step complete but degraded (e.g. crash-consistent snapshot — tools not running) |
| Blue (`#58a6ff`)   | Step currently in progress                            |
| Red (`#f85149`)    | Error                                                  |
| Gray (dim)         | Step not yet reached                                  |

Applied consistently across `run`'s checklist, `status`'s table/card views, and
`restore`'s manifest preview — the same four colors mean the same four things
everywhere in the CLI.

### `run` — checklist + progress bar

TTY: a per-stage checklist (✓/⚠/✗ per the palette above) that fills in as
`Event`s arrive, with a progress bar during `Copying` (the one stage with a
known byte count via `Percent`) and an elapsed timer. On error, completed
steps stay visibly checked so the failure is legible in context rather than
clearing the screen.

Non-TTY (`launchd`-triggered `run --all`): one timestamped log line per
`Event`, same information, no animation.

### `status` — table overview, card drill-down

`snapback status` (all VMs) is a compact table — one row per VM, scannable at
a glance, scales past a handful of VMs. `snapback status --vm <name>` is a new
flag (extending the existing `run --vm`/`run --all` pattern to `status`) that
drills into a single VM as a card: full consistency sentence, disk-usage bar,
retention policy in prose. The table's footer hints at the drill-down command.
`status --xbar` is unaffected by any of this — separate code path, unchanged
plain-text format.

### `list` — table, unstyled beyond the palette

`list` stays a simple table (ID, timestamp, size, comment) — no interactivity,
no event loop, just column alignment and the same status-color conventions
where a row's consistency is shown.

### `init` — step-by-step wizard via `huh`

Chosen over a single-page form (all fields visible, tab between them) because
each step can validate before the next is asked (e.g. confirm the destination
path is writable before asking about compression), and because
`charmbracelet/huh` — a forms library built on top of bubbletea/bubbles,
already used by tools like `gh` and `soft-serve` — is purpose-built for
exactly this shape. Using it replaces what would otherwise be hand-rolled
step-transition and validation logic.

Flow: discover VMs (`vmrun list`) → select which to manage → destination path
→ compression choice → retention numbers → per-VM schedule (presets:
nightly/weekly/custom cron) → review screen → write `config.yaml`. `init` has
no non-TTY fallback — it's inherently interactive, consistent with
`docs/design.md`'s description of it as "interactive config bootstrap."

### `restore` — split view with live preview

Chosen over a plain list followed by a separate confirm screen because
comparing a few candidate archives before committing is a real use case here
(picking the backup from "before the kernel upgrade" among several nightly
ones), and a live preview pane (manifest detail: size, consistency, comment,
SHA-256) answers that without extra keystrokes per candidate. Regardless of
which archive is picked, the final safety gate is unchanged from the earlier
design conversation: typing the VM name to confirm before extraction, since
`docs/design.md` treats "never overwrites source" as a hard guarantee, not a
preference.

## Testing

- Choreography unit tests (fake `VMController`, per CLAUDE.md) inject a
  slice-collecting fake `Reporter` and assert on emitted `Event` order and
  content — a strict improvement over the alternative of scraping stdout, and
  it stays entirely free of any TUI import.
- `tui.Reporter` and the bubbletea `Model`s for `init`'s wizard and
  `restore`'s picker are tested with Charm's `teatest` package, independent of
  `VMController`/choreography tests.
- `plain.Reporter` gets its own small unit tests (`Event` → expected line
  string).
- No change to the integration suite (`SNAPBACK_INTEGRATION=1`) beyond what
  `docs/design.md` already plans.

## New Dependencies

`github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`,
`github.com/charmbracelet/bubbles`, `github.com/charmbracelet/huh`,
`golang.org/x/term` (TTY detection — bubbletea pulls it transitively, but
command wiring calls it directly for the `Reporter` selection).

## Risks & Open Questions

- **Terminal width assumptions:** the checklist, table, and split-preview
  layouts all assume a reasonably wide terminal. None of the mockups here were
  tested against a narrow (e.g. 80-column) window — worth checking against
  real terminal widths during phase 1 implementation, not assumed safe.
- **`huh` adds a fourth Charm-ecosystem dependency** on top of the three
  already justified — reasonable given it directly replaces hand-rolled wizard
  logic, but worth confirming during implementation that its validation hooks
  actually cover the destination-path-must-be-writable check `init`'s flow
  depends on, rather than assuming it without checking.
