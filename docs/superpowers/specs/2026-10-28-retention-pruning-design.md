# Retention pruning (Phase 5) — design

Status: approved in brainstorming, 2026-10-28

## Context

`config.Retention{KeepLast, KeepDaily, KeepWeekly}` is parsed, validated
(non-negative), prompted for by the `init` wizard ("Keep last N
backups", "Keep daily backups for N days", "Keep weekly backups for N
weeks"), and displayed by `status --vm`. Nothing enforces it. Every
`run` adds an archive under `destination` and none are ever removed, so
a scheduled VM eventually fills its backup volume. This blocks an MVP
test on a second machine.

What exists today:

- `docs/design.md` step 8 already says the choreography prunes archives
  beyond the retention policy, and the command reference already lists
  `snapback prune`. Neither is implemented.
- `progress.Pruning` is a defined `Stage` that nothing emits.
- `backup.ListArchives(destination)` returns every directory under
  `destination` that holds a readable, well-formed `manifest.json`,
  newest first. Directories without one are skipped.
- `backup.AcquireLock(destination, vmName)` is the per-VM `flock` that
  `run` holds for its whole choreography and `cleanup` takes before
  deleting snapshots. A held lock returns an error wrapping
  `backup.ErrLocked`.
- `restore` already treats an archive vanishing mid-restore (e.g. pruned
  by an unattended run) as an error (`internal/cli/restore.go`).

## Decisions

- **All three rules ship.** Shipping only `keep_last` would leave two
  config fields that silently do nothing — the same class of bug this
  fixes.
- **Time-window semantics**, matching the wizard's wording. `keep_daily:
  7` means "the newest archive from each calendar day in the last 7
  days, counted back from now", not "the newest archive from each of the
  7 most recent days that have one". A weekly-scheduled VM therefore
  gets ~1 daily keep, not 7 weeks' worth. `keep_last` covers the
  long-gap case (a laptop closed for a month still keeps its last N).
- **Rules union.** An archive survives if any rule keeps it.
- **Safety floors.** The newest archive for a VM is never removed. An
  archive timestamped after `now` (clock skew) is always kept. A policy
  with every rule at 0 keeps everything.
- **Configured VMs only.** `prune` only considers archives whose
  manifest `vm_name` matches a VM currently in the config. Archives of
  a VM removed via `vm remove` are left alone, preserving that command's
  promise not to touch existing archives.
- **Pruning is the last step of `backup.Run`** (approach A), run under
  the lock `Run` already holds, and only after every earlier step
  succeeded. Rejected: calling prune from the CLI after `Run` returns
  (re-acquires the lock, and any other `Run` caller skips pruning), and
  a separate scheduled prune agent (a second LaunchAgent and more
  reconciliation for no gain).
- **Prune failure is non-fatal to `run`.** The archive just written is
  good; failing the backup because an old one couldn't be deleted would
  be wrong. The failure is surfaced on stderr (which lands in the
  per-VM log for scheduled runs). It is deliberately not added to the
  notification for the MVP: persistent prune failure eventually fills
  the disk, at which point `run` itself fails loudly and notifies.

## Design

### `internal/retention` (new, pure)

```go
type Policy struct {
    KeepLast   int
    KeepDaily  int
    KeepWeekly int
}

type Candidate struct {
    ID        string
    Timestamp time.Time
}

type Decision struct {
    Candidate
    Keep    bool
    Reasons []string // e.g. "last", "daily 2026-10-27", "weekly 2026-W43", "newest", "future"
}

func Select(cands []Candidate, p Policy, now time.Time) []Decision
```

- Input order doesn't matter. `Select` sorts newest first (ties broken
  on `ID`, matching `backup.sortArchives`), and returns decisions in that
  order.
- **keep_last N:** the N newest candidates, reason `last`.
- **keep_daily N:** for each local calendar day `d` from `now`'s day
  back to `now`'s day − (N−1), the newest candidate whose timestamp
  falls on `d`; reason `daily YYYY-MM-DD`. Calendar days are computed by
  date arithmetic in `now.Location()` (`time.Date(y, m, d-i, ...)`), not
  by subtracting 24h, so DST changeover days are handled correctly.
- **keep_weekly N:** for each ISO week `w` from `now`'s ISO week back to
  `now`'s week − (N−1), the newest candidate whose ISO week (in
  `now.Location()`) is `w`; reason `weekly YYYY-Www`. Weeks are walked
  by stepping the Monday of `now`'s week back 7 calendar days at a time,
  so year boundaries (2026-W53 → 2027-W01) fall out of `ISOWeek()`.
- Candidate timestamps are converted to `now.Location()` before
  bucketing. Tests pass a fixed `now` in a fixed location; production
  passes `time.Now()` (local time).
- Floors: the newest candidate is kept (reason `newest` if no rule
  already kept it); any candidate with `Timestamp.After(now)` is kept
  (reason `future`); if all three rules are 0, every candidate is kept
  (reason `no policy`).

No I/O, no knowledge of manifests or directories.

### `backup.Prune` (new, `internal/backup/prune.go`)

```go
type PruneResult struct {
    Kept    []PrunedArchive
    Removed []PrunedArchive
    Errs    []error // per-archive removal failures; others still processed
}

type PrunedArchive struct {
    Archive
    Reasons []string
}

func Prune(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error)
func pruneLocked(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error)
```

- `Prune` acquires `AcquireLock(destination, vmName)`, calls
  `pruneLocked`, releases. A held lock returns the `ErrLocked`-wrapping
  error unchanged so callers can `errors.Is` it. `Run` calls
  `pruneLocked` directly since it already holds the lock.
- `pruneLocked` calls `ListArchives(destination)`, filters to
  `Manifest.VMName == vmName`, maps each to a `retention.Candidate`
  (`ID: ArchiveID`, `Timestamp: Manifest.Timestamp`), runs `Select`, and
  partitions into `Kept`/`Removed`.
- The returned `error` is reserved for failures that prevent pruning at
  all (listing fails). Per-archive removal failures go in `Errs`.
- With `dryRun`, nothing is deleted; `Removed` lists what would be.
- **Removing one archive:** `os.Remove(<dir>/manifest.json)` first,
  then `os.RemoveAll(<dir>)`. Deleting the manifest is the single step
  that makes the directory stop being an archive to `ListArchives`,
  `list`, and `restore`. If `RemoveAll` then fails partway, what's left
  is an unlisted directory (reported in `Errs` with its path), never a
  half-deleted archive that still looks restorable. If removing the
  manifest itself fails, the archive is untouched and still listed;
  that's also reported in `Errs`.
- The archive directory path is `filepath.Join(destination,
  ArchiveID)`, the same layout `ListArchives` reads.
- **Symlinked archive directories are never deleted.** `ListArchives`
  follows a symlinked archive directory, and removing `manifest.json`
  through the link would delete inside the link's target. `pruneLocked`
  `os.Lstat`s each to-be-removed path first. If it's a symlink, the
  archive is left alone and an error naming it goes in `Errs`. It still
  counts as `Removed` for decision purposes, so it isn't shown as kept.

### `backup.Run` changes

- `Options` gains `Retention retention.Policy`.
- After checksumming and staging cleanup succeed, `Run` emits a
  `progress.Pruning` stage event and calls `pruneLocked(opts.Destination,
  opts.VMName, opts.Retention, now, false)`, with `now` from `opts.Now`.
- `Result` gains `Pruned []PrunedArchive` (the removed set) and
  `PruneErr error`. `PruneErr` combines the listing error, if any, with
  `errors.Join` of `PruneResult.Errs`. Neither affects `Run`'s returned
  error. The backup is a success.
- No pruning on any failure path: a run that didn't produce a good
  archive never deletes old ones.

### CLI

- `run`: passes `config.Retention` through to `Options.Retention`. On
  success, if `result.PruneErr != nil`, prints `warning: pruning old
  backups failed: <err>` to stderr. If anything was pruned, the plain
  (non-TTY) output adds one line, `pruned N old backup(s)`. The TUI
  shows the `pruning` stage like any other.
- `snapback prune [--vm <name>] [--dry-run]` (new,
  `internal/cli/prune.go`, same deps-injection pattern as `cleanup`):
  - Without `--vm`, every configured VM in config order. `--vm` must
    name a configured VM (same lookup and error as `run --vm`).
  - Per VM, calls `backup.Prune`. On `ErrLocked`, prints that the VM is
    being backed up and was skipped, and moves on (mirrors `cleanup`).
  - Output per VM: a table of archive ID, timestamp, and either `keep
    (<reasons>)` or `remove` (`would remove` under `--dry-run`), then a
    summary line.
  - Exits non-zero if any VM returned an error or had entries in
    `Errs`. Skipped-because-locked VMs don't affect the exit code.

### Docs

- `docs/design.md`: Phase 5 marked done in the roadmap. `snapback
  prune` row updated with `--vm`/`--dry-run`. Retention semantics
  (time windows, union, floors, configured VMs only) documented under
  the config reference.
- `README.md`: roadmap and status updated, `prune` added to quick start.
- `CLAUDE.md`: project status paragraph updated.

## Testing

All unit tests; no VM needed.

- **`retention.Select`**, table-driven:
  - each rule alone; overlapping rules (reasons accumulate);
  - the newest floor with `keep_last: 0`;
  - all rules zero keeps everything;
  - a future timestamp is kept;
  - multiple archives on one day: only the newest is the daily keep;
  - a day with no archive doesn't extend the window (time-window
    semantics): a weekly-scheduled VM with `keep_daily: 7` gets one
    daily keep;
  - a DST changeover day (America/New_York, 2026-11-01) buckets
    correctly;
  - ISO week year boundary: archives in 2026-W53 and 2027-W01 with
    `now` in 2027-W01 and `keep_weekly: 2`;
  - input order independence.
- **`backup.Prune`**, against a temp destination with real manifests
  written via the existing `writeTestManifest` helper:
  - archives for two VMs: only the named VM's are considered;
  - a directory without a manifest survives;
  - `dryRun` deletes nothing but reports `Removed`;
  - removed archives' directories are gone, and `ListArchives` no
    longer returns them;
  - a removal that fails (unwritable parent) lands in `Errs`, and other
    removals still happen;
  - the lock already held returns an `ErrLocked`-wrapping error and
    deletes nothing.
  - a symlinked archive directory selected for removal is left intact
    (link and target) and reported in `Errs`.
- **`backup.Run`** with the fake controller: a successful run with a
  policy removes old archives and populates `Result.Pruned`; a run that
  fails at any earlier stage deletes nothing; a prune failure sets
  `Result.PruneErr` and `Run` still returns no error; a `Pruning` stage
  event is emitted.
- **CLI**: `prune` across all VMs, `--vm`, unknown `--vm`, `--dry-run`
  output, skip-when-locked, exit code on removal error; `run` prints the
  stderr warning when `PruneErr` is set.

## Out of scope

- `keep_monthly` (monthly-scheduled VMs are covered by `keep_last`).
- Per-VM retention overrides.
- Pruning archives of VMs no longer in the config.
- Sweeping leftover unlisted directories from a failed `RemoveAll`
  (reported with their path; removed by hand).
- Prune failures in notifications.
