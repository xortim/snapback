# ADR-003: Wire `backup.Run` to `progress.Reporter` and `context.Context`

**Status:** Proposed
**Date:** 2026-08-27
**Deciders:** Tim

## Scope

**In scope:**

- A new `internal/progress` package: `Stage` enum, `Event{Stage, Message,
  Percent, Err}`, `Reporter interface { Report(Event) }` — exactly as
  specified in `docs/superpowers/specs/2026-08-23-cli-ux-design.md`
  ("ADR-002"), with zero new dependencies.
- A `progress.NoOpReporter` — a `Reporter` that discards every `Event`,
  analogous to `io.Discard`, so callers and tests that don't care about
  progress aren't forced to build a collector.
- `backup.Run`'s new signature: `Run(ctx context.Context, ctrl
  vm.Controller, reporter progress.Reporter, opts Options) (*Result,
  error)`, emitting one `Event` per stage transition.
- Coarse-grained `ctx` cancellation: checked between stages, not inside an
  in-flight `vm.Controller` call.
- A typed `RunError{Stage, Err}` so a caller can tell whether a failure
  happened at `Snapshotting` or later (i.e. whether a
  `snapback-<timestamp>` snapshot may be orphaned on the source VM).
- Byte-count `Percent` reporting for the `Copying` and `Compressing`
  stages, via optional callbacks on `copyDir` and `createArchive`.
- Updating all existing `internal/backup` tests to the new `Run` call
  shape.

**Out of scope (this ADR):**

- `tui.Reporter` and `plain.Reporter` — the actual rendering
  implementations ADR-002 describes. Those belong to whichever issue
  wires the CLI's `run` command up to a terminal (#7) or the bubbletea
  TUI layer (#17), and pull in the Charm-stack dependencies ADR-002
  lists. `internal/progress` itself stays free of any rendering import.
- Adding `context.Context` to `vm.Controller`'s methods
  (`CheckToolsState`, `Snapshot`, `ListSnapshots`, `DeleteSnapshot`).
  That interface merged in PR #18 and #11's real (`vmcli`/`vmrun`-backed)
  implementation is designed against it as-is; widening it is a separate,
  larger change than this ADR's stated scope. Concretely, this means a
  `vm.Controller` call already in flight when `ctx` is canceled still
  runs to completion — `Run()` only checks `ctx.Err()` between stages,
  not during one. True mid-syscall cancellation is deferred to whenever
  #11 gives `Controller` a real, cancelable implementation.
- Four smaller findings from the same review that produced this ADR
  (launchd `$PATH` silently downgrading zstd→gzip on scheduled runs, a
  `lookZstd()` TOCTOU, `copyDir`'s mtime/mode fidelity, and same-second
  `archiveID` collisions) — filed as their own issues instead of folded
  in here, to keep this change to one coherent unit (Reporter/Event/ctx
  wiring).
- `Pruning` and `Notifying` — two of `progress.Stage`'s nine values.
  These are retention-policy and notification concerns that span
  multiple VMs' archives (`docs/design.md` step 8), not something a
  single `backup.Run` call does. `Run()` only ever emits the six stages
  listed below; whatever wraps `Run()` with retention pruning and an
  `osascript` notification (presumably #7 or a later retention-policy
  issue) is responsible for `Pruning`/`Notifying` events, if any.

## Context

`internal/backup.Run` (landed in PR #22) implements the choreography from
`docs/design.md` but not the `Reporter`/`context.Context` requirements
`docs/superpowers/specs/2026-08-23-cli-ux-design.md` ("ADR-002") had
already specified the day before PR #22's implementation plan was
written. The gap was invisible to every task-scoped review along the way
because the plan that drove PR #22 cited only `docs/design.md` and never
cross-referenced ADR-002; it surfaced only in PR #22's final whole-branch
review and was filed as issue #21 rather than rushed into that PR's fix
wave.

This ADR is the design for closing that gap: it does not revisit any
decision ADR-002 already made about the `Reporter`/`Event` shape or the
Charm-stack rendering choice — only the parts issue #21 identified as
still needing a design call before implementation (the orphan-snapshot
signal shape, whether `vm.Controller` needs to change, and how byte-level
`Percent` gets computed).

## Architecture

### `internal/progress` package

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

// NoOpReporter discards every Event. Use it where progress reporting
// isn't needed -- e.g. most existing internal/backup tests that assert
// on Result/error, not on emitted events.
type NoOpReporter struct{}

func (NoOpReporter) Report(Event) {}
```

`Stage`, `Event`, and `Reporter` are copied verbatim from ADR-002.
`NoOpReporter` is new in this ADR. This package imports nothing beyond
the standard library and has no dependency on `internal/backup` or
`internal/vm` — `backup` depends on `progress`, never the reverse.

### `backup.Run`'s new signature and stage mapping

```go
func Run(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts Options) (*Result, error)
```

`Run()` performs 6 of `progress.Stage`'s 9 values, in this order, calling
`reporter.Report(progress.Event{Stage: ..., Message: ...})` at the start
of each:

1. `CheckingTools` — pre-flight validation and `ctrl.CheckToolsState`.
2. `Snapshotting` — `ctrl.Snapshot` and the `ctrl.ListSnapshots` confirmation.
3. `Copying` — `hostSync`, `readGuestOS`, and the staging `copyDir` (with
   `Percent` — see below).
4. `Merging` — `ctrl.DeleteSnapshot`.
5. `Compressing` — `createArchive` (with `Percent` — see below).
6. `Checksumming` — `os.Stat`/`sha256File`/`writeManifest`.

A final `Event{Stage: Done}` is reported immediately before `Run()`
returns its success result. `Pruning` and `Notifying` are never emitted
by `Run()` (see Scope, above).

### Cancellation

`Run()` checks `ctx.Err()` at the top of the function and immediately
before each of the 6 stage transitions above. If `ctx` is canceled, `Run()`
returns immediately, wrapping `context.Cause(ctx)` in a `RunError` (see
below) tagged with whichever stage was about to start. A `vm.Controller`
call already in flight when cancellation arrives is not interrupted — it
runs to completion, and the next stage-boundary check catches the
cancellation afterward. This is a deliberate, documented limitation (see
Scope) rather than a partial implementation of true mid-call
cancellation.

### Orphan-snapshot signal: `RunError`

```go
type RunError struct {
    Stage progress.Stage // stage active when the failure occurred
    Err   error
}

func (e *RunError) Error() string { return e.Err.Error() }
func (e *RunError) Unwrap() error { return e.Err }
```

Every error `Run()` returns — pre-flight validation, a `ctrl` error,
cancellation, a copy/archive/manifest failure — is wrapped as
`&RunError{Stage: <stage active at the failure>, Err: err}`. Pre-flight
validation failures (bad `VMName`, missing/directory `VMXPath`, etc.) get
`Stage: CheckingTools`, since no snapshot exists yet. A caller determines
whether a snapshot may be orphaned with:

```go
var runErr *backup.RunError
if errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting {
    // print the snapback cleanup pointer ADR-002 requires
}
```

`RunError.Unwrap()` means existing `errors.Is(err, errBoom)`-style
assertions in `choreography_test.go` keep working unchanged — this
replaces the current per-return-site `fmt.Errorf("...: %w", err)`
wrapping with one consistent typed wrapper around the same underlying
errors, not a change to what the underlying errors say.

### Byte-progress for `Copying` and `Compressing`

Both stages read through the same VM bundle data — `Copying` reads
`bundleDir` (the live `.vmwarevm`) writing to `stagingRoot`; `Compressing`
reads `stagingRoot` (what was just staged) writing the archive — so one
size measurement serves both stages' percent calculation.

A new helper in `internal/backup`:

```go
// dirSize returns the sum of all regular file sizes under root.
func dirSize(root string) (int64, error)
```

`Run()` calls `dirSize(bundleDir)` once, right after `readGuestOS`,
storing the result as `totalBytes`. (If `dirSize` fails, `Run()` returns
a `RunError{Stage: Copying, Err: ...}` — progress measurement failing is
treated as a real error, not silently skipped, since a wrong-but-present
`Percent` would be worse than an explicit failure here.)

`copyDir` and `createArchive` each gain an optional, nil-safe callback
parameter:

```go
func copyDir(src, dst string, onCopy func(cumulativeBytes int64)) error
func createArchive(srcDir, destPath, requested string, onRead func(cumulativeBytes int64)) (string, error)
```

- `copyDir` invokes `onCopy` after each regular file finishes copying,
  with the running total of bytes copied so far.
- `createArchive`'s underlying `tarTo` wraps its source-file reads in a
  small counting `io.Reader` (or invokes the callback per `io.Copy`
  chunk) so `onRead` fires with the running total of bytes read from
  `srcDir`, independent of which compressor is in use.

`Run()` supplies closures that convert `cumulativeBytes` into
`Percent = float64(cumulativeBytes) / float64(totalBytes)` and call
`reporter.Report(progress.Event{Stage: Copying, Percent: ...})` (or
`Compressing`, for the archive callback). Existing direct callers of
`copyDir`/`createArchive` in `copy_test.go`/`archive_test.go` pass `nil`
for the new parameter and are otherwise unaffected.

## Testing

- A slice-collecting fake `Reporter` added to `choreography_test.go`
  (`type fakeReporter struct{ events []progress.Event }`), asserting
  emitted `Event` order and `Stage` sequence for the happy path.
- A cancellation test: `ctx` created via `context.WithCancel` and
  canceled before `Run()` is called, asserting `Run()` returns a
  `RunError` wrapping the cancellation and that zero `ctrl` methods were
  invoked (same "validation fails before touching ctrl" pattern already
  used throughout the file, e.g. `TestRun_EmptyVMName_ReturnsError`).
- Two `RunError.Stage` tests: one reusing the existing `SnapshotErr`
  fixture (failure before `Snapshotting` completes → caller should NOT
  print the cleanup pointer) and one reusing `DeleteSnapshotErr` (failure
  after `Snapshotting` → caller SHOULD print it).
- `internal/progress` gets a small test file covering `NoOpReporter`
  (trivial — confirms `Report` is a safe no-op call, nothing more; `Stage`
  and `Event` are pure type/const definitions with nothing to unit test
  beyond compiling).
- All ~19 existing `backup.Run(fake, opts)` call sites in
  `choreography_test.go` update to
  `backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)` — Go
  1.26.5 (this module's floor) has `testing.T.Context()` (added in Go
  1.24), so tests use it instead of `context.Background()`.
- `dirSize`, `copyDir`'s `onCopy` callback, and `createArchive`'s
  `onRead` callback each get a direct unit test in their existing test
  files (`copy_test.go`, `archive_test.go`), independent of `Run()`.

## Risks & Open Questions

- **`dirSize`'s extra filesystem walk.** `Run()` already walks `bundleDir`
  once via `copyDir`'s `filepath.WalkDir`; `dirSize` adds a second full
  walk beforehand purely to learn the total upfront (percent is
  meaningless without a denominator). For typical VM bundle sizes
  (multi-GB disk files, few directory entries) this is a cheap stat-only
  walk compared to the byte-for-byte copy that follows, but it's a real
  second traversal — worth confirming during implementation that it
  doesn't measurably slow small/test-fixture cases, and revisiting if
  real-VM testing (per `CLAUDE.md`'s existing open item) shows otherwise.
- **`Percent` during `Compressing` is measured against input bytes read,
  not output bytes written.** This means the progress bar reaches 100%
  when the compressor has *consumed* all input, which may be slightly
  ahead of when the compressed output file is fully flushed to disk
  (compressors buffer). Acceptable for a progress indicator; not a
  correctness concern since `Percent` is advisory, not authoritative.
- **Coarse-grained cancellation leaves a window.** Between a cancellation
  check and the next `vm.Controller` call actually starting, there's no
  race — but once that call has started, canceling `ctx` has zero effect
  until it returns on its own. For today's synchronous fake this window
  is effectively instantaneous; for a future real shell-out
  implementation (a `vmrun`/`vmcli` subprocess) it could mean waiting
  out a slow snapshot operation despite an already-canceled context.
  Documented as an explicit limitation rather than solved here, per
  Scope.
