# ADR-004: `snapback restore` — Manifest-Driven Restore of a Backup Archive

**Status:** Proposed
**Date:** 2026-09-11
**Deciders:** Tim

## Scope

**In scope:**

- `backup.Restore(ctx, ctrl, reporter, opts) (*RestoreResult, error)` in
  `internal/backup`, alongside the existing `Run` — same package, since
  restore reuses `Manifest`, `Archive`/`ListArchives`, `sha256File`, and
  `readDiskFiles` directly rather than exporting them across a package
  boundary.
- `internal/backup/extract.go` — decompress+untar, the counterpart to
  `archive.go`'s `createArchive`, dispatching on the archive's own
  `Manifest.Compression` field rather than re-sniffing the file.
- Archive lookup: an exact `archive-id` positional argument, or
  `--vm <name> --latest` to resolve the newest archive for that VM
  without looking up the ID first.
- Target resolution: same parent directory as the source VM's `.vmwarevm`
  bundle (derived from `config.yaml`), named
  `<bundle-base> - backup <yyyy-mm-dd>.vmwarevm`, with a numeric suffix
  (` (2)`, ` (3)`, …) appended on collision. `--dest <dir>` overrides the
  inferred location explicitly, and is required when the archive's VM
  name isn't found in the current config (removed via `vm remove`, or
  restoring on a different machine).
- Integrity check, in order: SHA-256 of the archive file against
  `manifest.json`'s `sha256` (before extracting anything), then
  `vm.Controller.CheckDiskConsistency` against the extracted `.vmdk`(s)
  (the same call `Run`'s post-merge check already uses) before the
  restore is reported successful.
- `snapback restore` cobra command (`internal/cli/restore.go`), wired
  into `root.go`, following `run.go`'s existing plain-vs-interactive
  split.
- Generalizing `internal/tui`'s `Model`/`RunInteractive` just enough to
  render both `run` and `restore`'s progress without duplicating the
  bubbletea component: two small interfaces replace the current
  `*backup.Result`/`*backup.RunError` coupling, and the stage list /
  command header become constructor parameters instead of hardcoded.
- Four new `progress.Stage` values: `Verifying`, `Extracting`,
  `CheckingDiskConsistency`, `Placing`.

**Out of scope (this ADR):**

- Registering the restored bundle with Fusion (`vmrun register` or
  equivalent) — `restore` places a normal, openable `.vmwarevm` on disk
  and stops there; opening it in Fusion is a manual step, same as if you
  dragged it in yourself. Nothing today registers *any* VM `snapback`
  touches, so this isn't a regression.
- Guest-level boot verification. `CheckDiskConsistency` confirms the
  restored disk's snapshot chain applies cleanly
  (`vmware-vdiskmanager -e`) — it does not power the VM on and does not
  guarantee the guest OS actually boots. That's a real gap between "the
  archive is intact" and "this VM works," but closing it means launching
  a VM headless as part of a CLI command, which is a much bigger design
  question than this ADR's scope.
- `run --all`, launchd scheduling, and osascript notifications (Phase 2
  — deliberately deferred behind this work, see `docs/design.md`'s
  Roadmap).
- `status --xbar` / the xbar plugin (Phase 4) and `prune`/retention
  enforcement (Phase 5) — unrelated to restore, unchanged by this ADR.
- Locking against concurrent restores of the same VM. `Run`/`cleanup`
  have `backup.Lock` because they mutate the *source* VM's snapshot
  state and a race there can corrupt it. `Restore` never touches the
  source — the only race is two simultaneous restores of the same VM
  picking the same collision-suffixed target name, which at worst means
  one of them retries the suffix loop or (in a pathological
  simultaneous-rename case) one restore's target briefly doesn't exist
  where expected. Accepted as a real but low-severity gap for a
  single-user interactive tool rather than solved with new locking
  infrastructure — see Risks.

## Context

Epic #1 (Phase 1 — Core CLI) closed 2026-09-11 with every named
sub-issue landed, including the optional TUI layer (#17) that was
deliberately prioritized ahead of Phase 2's launchd scheduling — "I can
test the backups by hand for now." That deferral's condition has now
been met, which reopened the question of what's next. The answer isn't
Phase 2: a backup nobody has confirmed is restorable isn't actually a
safety net, and `docs/design.md`'s own `restore` line ("Restore to a new
`.vmwarevm`, suffixed `- backup yyyy-mm-dd`, never overwrites source")
has existed since ADR-001 without an implementation. `docs/design.md`'s
Roadmap section was corrected the same day (commit
`9938884`) to reflect this: Phase 3 (Restore) now explicitly moves ahead
of Phase 2 (Scheduling), and Phases 2–5's status markers were corrected
to match what's actually built versus what the original phase text
implied.

This ADR designs that Phase 3 work. It assumes the archive-on-disk
format `Run` already produces (`docs/design.md`'s backup choreography,
ADR-003's `Result`/`Manifest` shapes) as fixed and given — restore reads
that format, it doesn't change it.

## Architecture

### Archive layout recap

`ListArchives(destination)` (`internal/backup/list.go`) already scans
`<destination>/<archiveID>/` directories for a readable `manifest.json`
and returns `[]Archive{ArchiveID, Manifest}`, newest first. The archive
file itself lives alongside it as `archive.tar.zst` or `archive.tar.gz`
(`Manifest.Compression` says which), and its top-level tar entry is the
`.vmwarevm` bundle directory itself (e.g. `dev-ubuntu.vmwarevm/`) — this
is what `createArchive(stagingRoot, ...)` produced, where `stagingRoot`
contained exactly one child, `stagedBundle`. `Restore` relies on both of
these facts: which compression to reverse, and that extracting the
archive yields a single top-level `.vmwarevm` directory ready to place
directly.

### `Options` / `RestoreResult` / `RestoreError`

```go
// RestoreOptions configures a single restore.
type RestoreOptions struct {
    ArchiveID   string           // required
    Destination string           // parent directory archives live under (cfg.Destination)
    TargetDir   string           // parent directory to place the restored bundle in; if empty, derived from VMXPath
    VMXPath     string           // source VM's vmx path, if known (config lookup); empty if TargetDir is set explicitly
    StagingDir  string           // parent directory for the temporary extraction; os.TempDir() if empty
    Now         func() time.Time // defaults to time.Now if nil; drives the "backup yyyy-mm-dd" suffix
}

// RestoreResult describes a completed restore.
type RestoreResult struct {
    ArchiveID  string
    TargetPath string // the placed .vmwarevm's full path
    Manifest   Manifest
}

// RestoreError mirrors RunError: which stage was active when the
// restore failed.
type RestoreError struct {
    Stage progress.Stage
    Err   error
}

func (e *RestoreError) Error() string        { return e.Err.Error() }
func (e *RestoreError) Unwrap() error        { return e.Err }
func (e *RestoreError) FailedStage() progress.Stage { return e.Stage }
```

`RestoreOptions` takes either `TargetDir` (explicit `--dest`) or
`VMXPath` (known-VM case: the vmx sits at
`<bundle-dir>/<vmname>.vmx`, so `Restore` derives the target parent in
two steps — `filepath.Dir(VMXPath)` gives the bundle dir itself, and
`filepath.Dir` of *that* gives the directory containing it, e.g.
`~/Virtual Machines`, which is where the restored copy is placed
alongside the original) — never both; `internal/cli/restore.go` is
responsible for resolving config lookup vs `--dest` into exactly one of
these before calling `Restore`. Passing neither is a validation error at
the top of `Restore`, tagged `Stage: Verifying` (nothing has been read
yet).

`RunError` gains the same `FailedStage() progress.Stage` method (a pure
addition — existing `.Stage` field access and `errors.As(err, &runErr)`
call sites are unaffected) so both error types satisfy the `tui`
package's generalized failure interface below.

### Pipeline stages

```go
const (
    CheckingTools Stage = iota
    Snapshotting
    Copying
    Merging
    Compressing
    Checksumming
    Pruning
    Notifying
    Verifying
    Extracting
    CheckingDiskConsistency
    Placing
    Done
)
```

New values are appended after the existing `Notifying` (still unused,
reserved for Phase 2) rather than interleaved — nothing depends on the
`Stage` constants' relative numeric order; `tui`'s per-pipeline stage
lists (a `[]progress.Stage` each) are what define display order, matched
by equality, not by int comparison. `Restore` reports, in order:

1. **`Verifying`** — resolve the `Archive` via `ListArchives` +
   `ArchiveID` match, then stream the archive file through SHA-256,
   comparing to `Manifest.SHA256`. `Percent` reported per chunk read
   (same pattern as `Compressing`'s `onRead`), since this is a full pass
   over potentially multi-GB data. Mismatch is a hard failure — nothing
   is extracted.
2. **`Extracting`** — decompress+untar into
   `<StagingDir-or-TempDir>/snapback-restore-<archiveID>/`. `Percent`
   reported per chunk written, mirroring `Copying`'s `onCopy`.
3. **`CheckingDiskConsistency`** — `readDiskFiles` against the extracted
   `.vmx`, then `ctrl.CheckDiskConsistency` per disk. No `Percent` (fast,
   not byte-proportional). Failure here means the *archive itself* is
   bad, not a transfer error already ruled out by step 1 — the staging
   directory is preserved (not cleaned up) for manual inspection, same
   `keepStaging` pattern `Run`'s post-merge check uses, and the error
   message says so explicitly.
4. **`Placing`** — resolve the final target name (see below), then
   `os.Rename` staging → target, falling back to `copyDir` + `os.RemoveAll`
   on a cross-device rename error (`syscall.EXDEV`, surfaced by Go as a
   `*LinkError`). No `Percent` for the common same-volume rename case;
   the cross-device fallback doesn't report one either — it's expected
   to be rare (staging defaults to `os.TempDir()`, which is commonly but
   not always the same volume as `~/Virtual Machines`) and adding a
   third `Percent`-bearing code path for an edge case isn't worth the
   complexity here.

A final `Event{Stage: Done}` precedes a successful return, matching
`Run`.

### Target naming & collision handling

```go
// restoreTargetName returns "<bundle-base> - backup <yyyy-mm-dd>.vmwarevm",
// or that name with " (N)" inserted before the extension if the plain
// name already exists in parent, trying N = 2, 3, ... until a free name
// is found.
func restoreTargetName(parent, bundleBase string, now time.Time) (string, error)
```

`bundleBase` is the archived bundle's directory name with `.vmwarevm`
stripped (recovered from the extracted tar's single top-level entry, not
from config — this makes restore correct even for the `--dest`/unknown-VM
path, where there's no config entry to name it from). The loop is capped
at 100 attempts; exhausting it returns an error rather than looping
forever, since 100 same-day restores of the same VM without cleanup is
almost certainly a bug, not a real use case.

### `extract.go`

```go
// extractArchive decompresses+untars srcPath (as identified by
// compression, "zstd" or "gzip" -- Manifest.Compression, not re-sniffed)
// into destDir, which must not already exist. Mirrors createArchive's
// shape in reverse. If onWrite is non-nil, invoked with the running
// cumulative bytes written across all files.
func extractArchive(srcPath, destDir, compression string, onWrite func(cumulativeBytes int64)) error
```

zstd decompression shells out to the `zstd` binary (`zstd -d -q`,
stdin/stdout piped) exactly as `tarToZstd` shells out to compress —
`Manifest.Compression` already records which was used at backup time, so
there's no fallback-detection logic needed here the way `createArchive`
needs `lookZstd()`; if the manifest says `zstd`, `zstd` is required to
restore that archive (matches `docs/design.md`'s existing zstd-primary,
gzip-fallback stance — a gzip-compressed archive never requires zstd to
restore, but a zstd-compressed one does, same as it did to create).
`extractArchive` validates every tar entry's resolved path stays under
`destDir` (rejecting `..`-escaping or absolute entry names) before
writing — the archive is trusted (it's `snapback`'s own prior output,
checksummed in step 1), but this is a cheap, standard guard against a
corrupted or tampered archive writing outside the staging directory.

### CLI command

```
snapback restore <archive-id>
snapback restore --vm <name> --latest
snapback restore <archive-id> --dest <dir>
```

`internal/cli/restore.go` mirrors `run.go`'s structure: a `restoreDeps`
struct (`loadConfig`, `newController`, `isTerminal`,
`restoreInteractive`), a `newRestoreCmd`/`newRestoreCmdWithDeps` pair,
and a `restoreArchive(cmd, deps, archiveID, vmName, latest, dest string)`
body. Validation before calling `backup.Restore`:

- Exactly one of (`archive-id` positional) or (`--vm` with `--latest`)
  must be given — both or neither is a usage error.
- If `--vm` resolves to a config entry, `VMXPath` is set from it unless
  `--dest` is also given (explicit override wins). If `--vm`/the
  archive's `Manifest.VMName` isn't found in config at all, `--dest` is
  required — error out naming the missing VM and pointing at `--dest` if
  it's absent.

Output on success (plain path): `restore complete: <TargetPath>`,
matching `run`'s `backup complete: <ArchivePath>` line.

### TUI generalization

`tui.Model` currently hardcodes `*backup.Result`, `*backup.RunError`,
and `run`'s 6-stage list. Two new interfaces replace that coupling:

```go
// pipelineResult is implemented by *backup.Result and *backup.RestoreResult.
type pipelineResult interface {
    Summary() string // e.g. "backup complete: <path>" / "restore complete: <path>"
}

// pipelineError is implemented by *backup.RunError and *backup.RestoreError.
type pipelineError interface {
    error
    FailedStage() progress.Stage
}
```

`Model` stores `result pipelineResult` (nil until finished) and matches
failures via `errors.As(m.err, &perr)` against the `pipelineError`
interface type — `errors.As` supports an interface target directly, so
this needs no type switch between the two concrete error types.
`newModel` gains two more parameters: `header string` (the first line
`View()` prints — `"snapback run --vm %s"` / `"snapback restore %s"`,
pre-formatted by the caller) and `barStages []progress.Stage` (which
stages show the byte-progress bar — `{Copying, Compressing}` for run,
`{Verifying, Extracting}` for restore), replacing `View()`'s current
hardcoded `row.stage == progress.Copying || row.stage == progress.Compressing`
check with `slices.Contains(m.barStages, row.stage)`. `RunInteractive`'s
existing exported signature is unchanged (it becomes a thin wrapper
supplying run's header/stages/bar-stages to the generalized internals);
a new `RestoreInteractive(out, label string, cancel context.CancelFunc,
restoreFn func(progress.Reporter) (*backup.RestoreResult, error), ...)
(*backup.RestoreResult, error)` follows the identical shape for restore.
Existing `run` call sites and tests are unaffected by this refactor —
the public API they use doesn't change.

## Testing

- `backup.Restore` unit tests against `vm.FakeVMController`, building
  fixture archives with a real `createArchive` call in test setup
  (round-tripping the same code path `Run` uses, rather than hand-crafting
  tar bytes) plus a hand-written `manifest.json`:
  - happy path: checksum matches, extraction succeeds, fake disk check
    passes, target placed with the expected name.
  - checksum mismatch → `RestoreError{Stage: Verifying}`, nothing
    extracted (staging dir never created).
  - `FakeVMController.CheckDiskConsistency` returns an error →
    `RestoreError{Stage: CheckingDiskConsistency}`, staging dir
    preserved (assert it still exists after `Restore` returns).
  - target collision: pre-create the plain-named target, assert the
    `(2)` suffix is used; pre-create both, assert `(3)`.
  - unresolvable archive ID → clear error before any I/O.
  - `TargetDir` and `VMXPath` both empty → validation error, tagged
    `Verifying`.
- `extractArchive` unit tests in a new `extract_test.go`: round-trip
  through `createArchive` (zstd and gzip) → `extractArchive`, comparing
  extracted file contents/tree to the original; a corrupted/truncated
  archive input; a hand-crafted tar entry with a `../` path, asserting
  it's rejected rather than written outside `destDir`.
- `restoreTargetName` unit tests: no collision, one collision, two
  collisions, and the 100-attempt exhaustion case.
- `internal/cli/restore_internal_test.go` mirrors
  `run_internal_test.go`'s shape: plain and interactive paths via
  `restoreDeps`, the `--vm`/`archive-id` mutual-exclusion validation, the
  missing-VM-requires-`--dest` case.
- `internal/tui`: existing `model_test.go`/`run_test.go`/`view_test.go`
  continue to pass unchanged (the public `RunInteractive` contract is
  preserved) plus new tests for `RestoreInteractive` exercising the same
  checklist/percent-bar/cancellation behavior against restore's stage
  list.

## Risks & Open Questions

- **Cross-device staging-to-target move.** `StagingDir` defaults to
  `os.TempDir()`, which is not guaranteed to share a volume with
  `~/Virtual Machines` (or wherever the source VM actually lives) — the
  `copyDir`-fallback path in `Placing` handles this correctly but
  temporarily doubles disk usage on the target volume during the copy,
  the same real constraint `docs/design.md` already documents for
  backup's own staging copy. Worth confirming during implementation
  whether defaulting `StagingDir` to the *target's* parent directory
  instead of `os.TempDir()` avoids this more often in practice — deferred
  to implementation rather than decided here, since it's a tuning choice
  that doesn't change the design's shape.
- **No boot verification, as noted in Scope.** A restore can report
  success (checksum verified, disk chain consistent) for a VM whose
  guest OS still fails to boot for unrelated reasons. This is a known,
  accepted gap, not an oversight.
- **Concurrent restores of the same VM**, as noted in Scope — accepted
  as low-severity rather than solved with new locking.
- **`zstd`/`vmware-vdiskmanager` binary availability at restore time.**
  If the archive was compressed with `zstd` but the restoring machine
  doesn't have it installed (e.g. restoring on a machine other than the
  one that made the backup), `Extracting` fails with a clear "zstd not
  found" error rather than silently falling back — there's no
  gzip-equivalent fallback for decompressing a zstd stream, unlike
  `createArchive`'s create-time fallback. Same applies to
  `vmware-vdiskmanager` for `CheckDiskConsistency`, already a documented
  constraint (`vm.VMCLIController`'s existing lazy-resolution behavior)
  reused as-is here.
