# Retention Pruning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce `retention.keep_last`/`keep_daily`/`keep_weekly`. Each successful `run` prunes that VM's old archives, and a new `snapback prune [--vm] [--dry-run]` does the same on demand.

**Architecture:** A pure `internal/retention.Select` decides keep/remove from timestamps alone. `backup.Prune`/`pruneLocked` lists one VM's archives, calls `Select`, and deletes (manifest first, then the directory). `backup.Run` calls `pruneLocked` as its last step under the lock it already holds, and treats prune failure as non-fatal. `internal/cli` wires config into `Run` and adds the `prune` command.

**Tech Stack:** Go 1.26.5, stdlib only (`time`, `slices`, `os`), cobra for the CLI.

**Spec:** `docs/superpowers/specs/2026-10-28-retention-pruning-design.md`

## Global Constraints

- Time-window semantics: `keep_daily: N` = newest archive from each local calendar day in `[today-(N-1), today]`. `keep_weekly: N` = newest archive from each ISO week in `[thisWeek-(N-1), thisWeek]`. Days and weeks are computed in `now.Location()`.
- Rules union. Floors: an archive with `Timestamp.After(now)` is always kept (reason `future`) and is excluded from every rule, so a clock-skewed archive can never use up a `keep_last` slot or crowd out the archive `Run` just wrote. The newest non-future archive is always kept. All rules 0 means keep everything.
- Only archives whose manifest `VMName` exactly equals the VM name are candidates. Directories without a valid manifest are never touched.
- Removal order: `os.Remove(<dir>/manifest.json)`, then `os.RemoveAll(<dir>)`. A symlinked archive directory is never deleted. It's reported in `Errs` and still counted in `Removed`.
- Prune failure never fails `run`. Pruning never happens on a failed `run`.
- Reason strings, exactly: `last`, `daily YYYY-MM-DD`, `weekly YYYY-Www` (e.g. `weekly 2026-W43`), `newest`, `future`, `no policy`.
- Conventional commits with package scope, e.g. `feat(retention): ...`. Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Before the final push: `make lint`, `make test`, `make build` all pass (CI's own targets).

## Review Focus

1. **UTC manifest vs. local day:** manifests store UTC (`startTime := now().UTC()` in `Run`). An archive at `2026-10-28T02:00Z` belongs to Oct 27 in America/New_York. Bucketing must convert to `now.Location()` first. Test in Task 1 (`TestSelect_BucketsInNowLocation`).
2. **VM-name prefix collisions:** archive IDs are `<vm>-<ts>`, so VM `ubuntu` and VM `ubuntu-dev` share a directory-name prefix. Filtering must use manifest `VMName` equality, never the directory name. Test in Task 2 (`TestPrune_PrefixNamedVMNotConsidered`).
3. **The archive `Run` just wrote must survive** even with a policy that would otherwise drop it (e.g. `keep_weekly: 1` when an older archive is in the same week, or an older archive with a clock-skewed future timestamp sorting ahead of it). The newest-non-future floor plus excluding future archives from rules cover it. `TestSelect` case "future timestamp always kept" pins the skew half. Test in Task 3 (`TestRun_Prune_KeepsJustWrittenArchive`).
4. **Partial `RemoveAll`:** after the manifest is gone, a failing `RemoveAll` must leave the archive unlisted, with an error naming the path. Test in Task 2 (`TestPrune_PartialRemoveAll_LeavesUnlistedDirAndReportsPath`).
5. **Never-backed-up VM / missing destination:** `prune` must report nothing to prune, not error. Test in Task 5 (`TestPruneCmd_NoArchives_ReportsNothingToPrune`).

---

## File Structure

- Create `internal/retention/retention.go`: `Policy`, `Candidate`, `Decision`, `Select`. Pure, no I/O.
- Create `internal/retention/retention_test.go`.
- Create `internal/backup/prune.go`: `PrunedArchive`, `PruneResult`, `Prune`, `pruneLocked`, `removeArchive`.
- Create `internal/backup/prune_test.go` (package `backup`, internal, to reuse `writeTestManifest` from `list_test.go` and call `pruneLocked`).
- Modify `internal/backup/choreography.go`: `Options.Retention`, `Result.Pruned`/`PruneErr`, and the prune step in `Run`.
- Modify `internal/backup/choreography_test.go`: stage order includes `Pruning`, plus new Run prune tests.
- Modify `internal/cli/run.go`: pass retention, warn on `PruneErr`, print the pruned count.
- Create `internal/cli/retention.go`: `retentionPolicy(config.Retention) retention.Policy`.
- Create `internal/cli/prune.go` and `internal/cli/prune_internal_test.go`.
- Modify `internal/cli/root.go`: register `newPruneCmd()`.
- Modify `docs/design.md`, `README.md`, `CLAUDE.md`.

---

### Task 1: `internal/retention.Select`

**Files:**
- Create: `internal/retention/retention.go`
- Test: `internal/retention/retention_test.go`

**Interfaces:**
- Produces:
  ```go
  type Policy struct{ KeepLast, KeepDaily, KeepWeekly int }
  type Candidate struct{ ID string; Timestamp time.Time }
  type Decision struct{ Candidate; Keep bool; Reasons []string }
  func Select(cands []Candidate, p Policy, now time.Time) []Decision // newest first, ties by ID ascending
  ```

- [ ] **Step 1: Write the failing tests**

`internal/retention/retention_test.go`:

```go
package retention

import (
	"slices"
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func cand(id string, ts time.Time) Candidate { return Candidate{ID: id, Timestamp: ts} }

// kept returns the IDs Select kept, in Select's (newest-first) order.
func kept(ds []Decision) []string {
	var ids []string
	for _, d := range ds {
		if d.Keep {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func reasonsFor(t *testing.T, ds []Decision, id string) []string {
	t.Helper()
	for _, d := range ds {
		if d.ID == id {
			return d.Reasons
		}
	}
	t.Fatalf("no decision for %q", id)
	return nil
}

func TestSelect(t *testing.T) {
	now := utc(2026, 10, 28, 12) // Wednesday, ISO 2026-W44
	tests := []struct {
		name  string
		cands []Candidate
		p     Policy
		want  []string
	}{
		{
			name:  "keep_last only",
			cands: []Candidate{cand("a", utc(2026, 10, 1, 0)), cand("b", utc(2026, 10, 2, 0)), cand("c", utc(2026, 10, 3, 0))},
			p:     Policy{KeepLast: 2},
			want:  []string{"c", "b"},
		},
		{
			name:  "all zero keeps everything",
			cands: []Candidate{cand("a", utc(2026, 1, 1, 0)), cand("b", utc(2026, 2, 1, 0))},
			p:     Policy{},
			want:  []string{"b", "a"},
		},
		{
			name: "keep_daily keeps newest per day within window",
			cands: []Candidate{
				cand("today-late", utc(2026, 10, 28, 9)),
				cand("today-early", utc(2026, 10, 28, 1)),
				cand("yesterday", utc(2026, 10, 27, 5)),
				cand("outside", utc(2026, 10, 26, 5)),
			},
			p:    Policy{KeepDaily: 2},
			want: []string{"today-late", "yesterday"},
		},
		{
			name: "time window: weekly-scheduled VM with keep_daily 7 gets one daily keep",
			cands: []Candidate{
				cand("oct25", utc(2026, 10, 25, 0)),
				cand("oct18", utc(2026, 10, 18, 0)),
				cand("oct11", utc(2026, 10, 11, 0)),
				cand("oct04", utc(2026, 10, 4, 0)),
				cand("sep27", utc(2026, 9, 27, 0)),
				cand("sep20", utc(2026, 9, 20, 0)),
				cand("sep13", utc(2026, 9, 13, 0)),
			},
			p:    Policy{KeepDaily: 7},
			want: []string{"oct25"},
		},
		{
			name: "keep_weekly keeps newest per ISO week within window",
			cands: []Candidate{
				cand("w44", utc(2026, 10, 27, 0)),     // Tue, W44
				cand("w43-late", utc(2026, 10, 25, 0)), // Sun, W43
				cand("w43-early", utc(2026, 10, 19, 0)), // Mon, W43
				cand("w42", utc(2026, 10, 15, 0)),
			},
			p:    Policy{KeepWeekly: 2},
			want: []string{"w44", "w43-late"},
		},
		{
			name:  "newest floor with keep_last 0",
			cands: []Candidate{cand("old", utc(2026, 1, 1, 0)), cand("older", utc(2025, 1, 1, 0))},
			p:     Policy{KeepDaily: 1},
			want:  []string{"old"},
		},
		{
			name:  "future timestamp always kept",
			cands: []Candidate{cand("future", utc(2026, 11, 5, 0)), cand("now", utc(2026, 10, 28, 11)), cand("old", utc(2026, 1, 1, 0))},
			p:     Policy{KeepLast: 1},
			want:  []string{"future", "now"},
		},
		{
			name:  "empty input",
			cands: nil,
			p:     Policy{KeepLast: 3},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kept(Select(tt.cands, tt.p, now))
			if !slices.Equal(got, tt.want) {
				t.Errorf("kept = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelect_FutureTimestamp_OnlyFutureReason(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	ds := Select([]Candidate{cand("future", utc(2026, 11, 5, 0)), cand("real", utc(2026, 10, 28, 1))}, Policy{KeepLast: 1, KeepDaily: 30}, now)
	// Excluded from every rule: it doesn't take the keep_last slot.
	if got := reasonsFor(t, ds, "future"); !slices.Equal(got, []string{"future"}) {
		t.Errorf("future reasons = %v, want [future]", got)
	}
	if got := reasonsFor(t, ds, "real"); !slices.Equal(got, []string{"last", "daily 2026-10-28"}) {
		t.Errorf("real reasons = %v, want [last daily 2026-10-28]", got)
	}
}

func TestSelect_ReasonsAccumulate(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	ds := Select([]Candidate{cand("a", utc(2026, 10, 28, 3))}, Policy{KeepLast: 1, KeepDaily: 1, KeepWeekly: 1}, now)
	want := []string{"last", "daily 2026-10-28", "weekly 2026-W44"}
	if got := reasonsFor(t, ds, "a"); !slices.Equal(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
}

func TestSelect_NewestReason_WhenNoRuleKeepsIt(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	ds := Select([]Candidate{cand("a", utc(2026, 1, 1, 0))}, Policy{KeepDaily: 1}, now)
	if got := reasonsFor(t, ds, "a"); !slices.Equal(got, []string{"newest"}) {
		t.Errorf("reasons = %v, want [newest]", got)
	}
}

func TestSelect_NoPolicyReason(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	ds := Select([]Candidate{cand("a", utc(2026, 1, 1, 0))}, Policy{}, now)
	if got := reasonsFor(t, ds, "a"); !slices.Equal(got, []string{"no policy"}) {
		t.Errorf("reasons = %v, want [no policy]", got)
	}
}

func TestSelect_InputOrderIndependent(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	a, b, c := cand("a", utc(2026, 10, 1, 0)), cand("b", utc(2026, 10, 2, 0)), cand("c", utc(2026, 10, 3, 0))
	p := Policy{KeepLast: 2}
	x := Select([]Candidate{a, b, c}, p, now)
	y := Select([]Candidate{c, a, b}, p, now)
	var xi, yi []string
	for i := range x {
		xi = append(xi, x[i].ID)
		yi = append(yi, y[i].ID)
	}
	if !slices.Equal(xi, []string{"c", "b", "a"}) || !slices.Equal(xi, yi) {
		t.Errorf("order: %v vs %v, want both [c b a]", xi, yi)
	}
	if !slices.Equal(kept(x), kept(y)) {
		t.Errorf("kept differs by input order: %v vs %v", kept(x), kept(y))
	}
}

func TestSelect_TiesBreakOnID(t *testing.T) {
	now := utc(2026, 10, 28, 12)
	ts := utc(2026, 10, 1, 0)
	ds := Select([]Candidate{cand("b", ts), cand("a", ts)}, Policy{KeepLast: 1}, now)
	if got := kept(ds); !slices.Equal(got, []string{"a"}) {
		t.Errorf("kept = %v, want [a] (ties break on ID ascending)", got)
	}
}

func TestSelect_BucketsInNowLocation(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, ny)
	// 2026-10-28T02:00Z is 2026-10-27 22:00 EDT -- yesterday in New York.
	late := cand("utc-today-ny-yesterday", time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC))
	today := cand("today", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)) // 11:00 EDT
	ds := Select([]Candidate{late, today}, Policy{KeepDaily: 2}, now)
	if got := reasonsFor(t, ds, "utc-today-ny-yesterday"); !slices.Equal(got, []string{"daily 2026-10-27"}) {
		t.Errorf("reasons = %v, want [daily 2026-10-27]", got)
	}
}

func TestSelect_DSTSpringForward(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// DST starts 2027-03-14. now - 24h from 00:30 EDT on the 15th lands on
	// the 13th, skipping the 14th -- calendar arithmetic must not.
	now := time.Date(2027, 3, 15, 0, 30, 0, 0, ny)
	ds := Select([]Candidate{
		cand("mar15", time.Date(2027, 3, 15, 0, 10, 0, 0, ny)),
		cand("mar14", time.Date(2027, 3, 14, 12, 0, 0, 0, ny)),
		cand("mar13", time.Date(2027, 3, 13, 12, 0, 0, 0, ny)),
	}, Policy{KeepDaily: 2}, now)
	if got := kept(ds); !slices.Equal(got, []string{"mar15", "mar14"}) {
		t.Errorf("kept = %v, want [mar15 mar14]", got)
	}
}

func TestSelect_DSTFallBack(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// DST ends 2026-11-01 (a 25-hour day).
	now := time.Date(2026, 11, 2, 0, 30, 0, 0, ny)
	ds := Select([]Candidate{
		cand("nov02", time.Date(2026, 11, 2, 0, 10, 0, 0, ny)),
		cand("nov01-late", time.Date(2026, 11, 1, 23, 30, 0, 0, ny)),
		cand("nov01-early", time.Date(2026, 11, 1, 0, 30, 0, 0, ny)),
		cand("oct31", time.Date(2026, 10, 31, 23, 30, 0, 0, ny)),
	}, Policy{KeepDaily: 2}, now)
	if got := kept(ds); !slices.Equal(got, []string{"nov02", "nov01-late"}) {
		t.Errorf("kept = %v, want [nov02 nov01-late]", got)
	}
}

func TestSelect_ISOWeekYearBoundary(t *testing.T) {
	// 2026 has 53 ISO weeks: 2026-W53 is Mon 2026-12-28 .. Sun 2027-01-03.
	now := utc(2027, 1, 6, 12) // Wed, 2027-W01
	ds := Select([]Candidate{
		cand("jan05", utc(2027, 1, 5, 0)),  // 2027-W01
		cand("jan02", utc(2027, 1, 2, 0)),  // 2026-W53
		cand("dec29", utc(2026, 12, 29, 0)), // 2026-W53, older
		cand("dec27", utc(2026, 12, 27, 0)), // 2026-W52
	}, Policy{KeepWeekly: 2}, now)
	if got := kept(ds); !slices.Equal(got, []string{"jan05", "jan02"}) {
		t.Errorf("kept = %v, want [jan05 jan02]", got)
	}
	if got := reasonsFor(t, ds, "jan02"); !slices.Equal(got, []string{"weekly 2026-W53"}) {
		t.Errorf("jan02 reasons = %v, want [weekly 2026-W53]", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/retention/... -v`
Expected: FAIL to compile (`undefined: Select`, `Policy`, `Candidate`, `Decision`).

- [ ] **Step 3: Implement**

`internal/retention/retention.go`:

```go
// Package retention decides which backup archives a retention policy
// keeps. It is pure -- timestamps in, keep/remove decisions out -- so
// internal/backup owns all filesystem work. Semantics (time windows,
// union of rules, safety floors) are specified in
// docs/superpowers/specs/2026-10-28-retention-pruning-design.md.
package retention

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Policy mirrors config.Retention. A zero Policy keeps everything.
type Policy struct {
	KeepLast   int
	KeepDaily  int
	KeepWeekly int
}

// Candidate is one archive, identified by ID, as Select sees it.
type Candidate struct {
	ID        string
	Timestamp time.Time
}

// Decision is Select's verdict on one Candidate. Reasons lists every
// rule that kept it, in rule order; empty when Keep is false.
type Decision struct {
	Candidate
	Keep    bool
	Reasons []string
}

// Select applies p to cands as of now and returns one Decision per
// candidate, newest first (ties broken on ID ascending). Calendar days
// and ISO weeks are computed in now.Location(); candidate timestamps are
// converted to that location before bucketing.
func Select(cands []Candidate, p Policy, now time.Time) []Decision {
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b Candidate) int {
		if c := b.Timestamp.Compare(a.Timestamp); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	ds := make([]Decision, len(sorted))
	for i, c := range sorted {
		ds[i] = Decision{Candidate: c}
	}
	keep := func(i int, reason string) {
		ds[i].Keep = true
		ds[i].Reasons = append(ds[i].Reasons, reason)
	}

	if p == (Policy{}) {
		for i := range ds {
			keep(i, "no policy")
		}
		return ds
	}

	// Future-dated archives (clock skew) are kept unconditionally and
	// excluded from every rule below, so one can never take a keep_last
	// slot or the newest floor from a real archive -- e.g. the one Run
	// just wrote.
	var current []int // indices of non-future decisions, newest first
	for i := range ds {
		if ds[i].Timestamp.After(now) {
			keep(i, "future")
			continue
		}
		current = append(current, i)
	}

	for n, i := range current {
		if n >= p.KeepLast {
			break
		}
		keep(i, "last")
	}

	loc := now.Location()
	if p.KeepDaily > 0 {
		y, m, d := now.Date()
		window := make(map[string]bool, p.KeepDaily)
		for i := range p.KeepDaily {
			window[time.Date(y, m, d-i, 12, 0, 0, 0, loc).Format(time.DateOnly)] = true
		}
		keepNewestPerBucket(ds, now, window, func(t time.Time) string { return t.In(loc).Format(time.DateOnly) }, "daily ", keep)
	}

	if p.KeepWeekly > 0 {
		monday := startOfISOWeek(now)
		window := make(map[string]bool, p.KeepWeekly)
		for i := range p.KeepWeekly {
			window[isoWeekKey(time.Date(monday.Year(), monday.Month(), monday.Day()-7*i, 12, 0, 0, 0, loc))] = true
		}
		keepNewestPerBucket(ds, now, window, func(t time.Time) string { return isoWeekKey(t.In(loc)) }, "weekly ", keep)
	}

	if len(current) > 0 && !ds[current[0]].Keep {
		keep(current[0], "newest")
	}
	return ds
}

// keepNewestPerBucket keeps the first (i.e. newest, since ds is sorted
// newest first) non-future decision in each bucket that's in window.
func keepNewestPerBucket(ds []Decision, now time.Time, window map[string]bool, bucket func(time.Time) string, prefix string, keep func(int, string)) {
	seen := make(map[string]bool, len(window))
	for i := range ds {
		if ds[i].Timestamp.After(now) {
			continue
		}
		key := bucket(ds[i].Timestamp)
		if window[key] && !seen[key] {
			seen[key] = true
			keep(i, prefix+key)
		}
	}
}

// startOfISOWeek returns noon on the Monday of t's ISO week, in t's
// location. Noon keeps later day arithmetic clear of DST transitions.
func startOfISOWeek(t time.Time) time.Time {
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday is the last day of an ISO week
	}
	y, m, d := t.Date()
	return time.Date(y, m, d-(wd-1), 12, 0, 0, 0, t.Location())
}

func isoWeekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}
```

The reason order this produces is `last`, `daily`, `weekly`, then `newest`. `future` is only ever a reason on its own. This matches `TestSelect_ReasonsAccumulate` and `TestSelect_FutureTimestamp_OnlyFutureReason`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/retention/... -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/retention
git commit -m "feat(retention): add time-window retention policy selection

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `backup.Prune`

**Files:**
- Create: `internal/backup/prune.go`
- Test: `internal/backup/prune_test.go` (package `backup`)

**Interfaces:**
- Consumes: `retention.Policy`, `retention.Candidate`, `retention.Select` (Task 1); the existing `ListArchives(destination string) ([]Archive, error)`, `Archive{ArchiveID string; Manifest Manifest}`, `AcquireLock`, `ErrLocked`; `writeTestManifest(t, destination, archiveID, Manifest)` from `list_test.go`.
- Produces:
  ```go
  type PrunedArchive struct { Archive; Reasons []string }
  type PruneResult struct { Kept, Removed []PrunedArchive; Errs []error }
  func Prune(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error)
  func pruneLocked(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error)
  ```

- [ ] **Step 1: Write the failing tests**

`internal/backup/prune_test.go`:

```go
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/retention"
)

var pruneNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

func ids(as []PrunedArchive) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.ArchiveID)
	}
	return out
}

func listedIDs(t *testing.T, destination string) []string {
	t.Helper()
	as, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives: %v", err)
	}
	var out []string
	for _, a := range as {
		out = append(out, a.ArchiveID)
	}
	return out
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection doesn't work as root")
	}
}

func TestPrune_RemovesBeyondPolicy_OnlyNamedVM(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	writeTestManifest(t, dest, "myvm-3", Manifest{VMName: "myvm", Timestamp: day(3)})
	writeTestManifest(t, dest, "other-1", Manifest{VMName: "other", Timestamp: day(1)})

	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("Errs = %v, want none", res.Errs)
	}
	if got := ids(res.Kept); !slices.Equal(got, []string{"myvm-3"}) {
		t.Errorf("Kept = %v, want [myvm-3]", got)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-2", "myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-2 myvm-1]", got)
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-3", "other-1"}) {
		t.Errorf("listed after prune = %v, want [myvm-3 other-1]", got)
	}
	for _, id := range []string{"myvm-1", "myvm-2"} {
		if _, err := os.Stat(filepath.Join(dest, id)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (stat err = %v)", id, err)
		}
	}
}

func TestPrune_KeptCarriesReasons(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(28)})
	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Kept) != 1 || !slices.Equal(res.Kept[0].Reasons, []string{"last"}) {
		t.Errorf("Kept = %+v, want one archive with reasons [last]", res.Kept)
	}
}

func TestPrune_PrefixNamedVMNotConsidered(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "ubuntu-20261001T000000Z", Manifest{VMName: "ubuntu", Timestamp: day(1)})
	writeTestManifest(t, dest, "ubuntu-dev-20261002T000000Z", Manifest{VMName: "ubuntu-dev", Timestamp: day(2)})
	writeTestManifest(t, dest, "ubuntu-dev-20261003T000000Z", Manifest{VMName: "ubuntu-dev", Timestamp: day(3)})

	res, err := Prune(dest, "ubuntu", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want none (ubuntu-dev archives are not ubuntu's)", ids(res.Removed))
	}
	if got := listedIDs(t, dest); len(got) != 3 {
		t.Errorf("listed after prune = %v, want all 3", got)
	}
}

func TestPrune_DirWithoutManifestSurvives(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	stray := filepath.Join(dest, "myvm-stray")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("manifest-less dir was touched: %v", err)
	}
}

func TestPrune_DryRunDeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})

	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, true)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-1]", got)
	}
	if got := listedIDs(t, dest); len(got) != 2 {
		t.Errorf("listed after dry run = %v, want both", got)
	}
}

func TestPrune_ManifestRemoveFails_ArchiveIntactOthersStillRemoved(t *testing.T) {
	skipIfRoot(t)
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	writeTestManifest(t, dest, "myvm-3", Manifest{VMName: "myvm", Timestamp: day(3)})
	locked := filepath.Join(dest, "myvm-1")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 1 || !strings.Contains(res.Errs[0].Error(), "myvm-1") {
		t.Errorf("Errs = %v, want one error naming myvm-1", res.Errs)
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-3", "myvm-1"}) {
		t.Errorf("listed after prune = %v, want [myvm-3 myvm-1] (myvm-2 removed, myvm-1 intact)", got)
	}
}

func TestPrune_PartialRemoveAll_LeavesUnlistedDirAndReportsPath(t *testing.T) {
	skipIfRoot(t)
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	sub := filepath.Join(dest, "myvm-1", "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(res.Errs) != 1 || !strings.Contains(res.Errs[0].Error(), filepath.Join(dest, "myvm-1")) {
		t.Errorf("Errs = %v, want one error naming %s", res.Errs, filepath.Join(dest, "myvm-1"))
	}
	if got := listedIDs(t, dest); !slices.Equal(got, []string{"myvm-2"}) {
		t.Errorf("listed after prune = %v, want [myvm-2] (myvm-1 unlisted once its manifest is gone)", got)
	}
}

func TestPrune_SymlinkedArchiveNotDeleted(t *testing.T) {
	dest := t.TempDir()
	target := t.TempDir()
	writeTestManifest(t, target, "real", Manifest{VMName: "myvm", Timestamp: day(1)})
	link := filepath.Join(dest, "myvm-1")
	if err := os.Symlink(filepath.Join(target, "real"), link); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})

	res, err := Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if got := ids(res.Removed); !slices.Equal(got, []string{"myvm-1"}) {
		t.Errorf("Removed = %v, want [myvm-1] (decided removed, even though not deleted)", got)
	}
	if len(res.Errs) != 1 || !strings.Contains(res.Errs[0].Error(), "symlink") {
		t.Errorf("Errs = %v, want one symlink error", res.Errs)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "real", "manifest.json")); err != nil {
		t.Errorf("symlink target's manifest removed: %v", err)
	}
}

func TestPrune_LockHeld_ReturnsErrLockedAndDeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writeTestManifest(t, dest, "myvm-1", Manifest{VMName: "myvm", Timestamp: day(1)})
	writeTestManifest(t, dest, "myvm-2", Manifest{VMName: "myvm", Timestamp: day(2)})
	lock, err := AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	_, err = Prune(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Prune() error = %v, want ErrLocked", err)
	}
	if got := listedIDs(t, dest); len(got) != 2 {
		t.Errorf("listed = %v, want both untouched", got)
	}
}

func TestPrune_MissingDestination_NoArchivesNoError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "never-created")
	res, err := pruneLocked(dest, "myvm", retention.Policy{KeepLast: 1}, pruneNow, false)
	if err != nil {
		t.Fatalf("pruneLocked() error = %v, want nil", err)
	}
	if len(res.Kept)+len(res.Removed)+len(res.Errs) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/backup/ -run TestPrune -v`
Expected: FAIL to compile (`undefined: Prune`, `pruneLocked`, `PrunedArchive`).

- [ ] **Step 3: Implement**

`internal/backup/prune.go`:

```go
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xortim/snapback/internal/retention"
)

// PrunedArchive is one archive Prune considered, with the reasons
// retention.Select gave for keeping it (empty for a removed archive).
type PrunedArchive struct {
	Archive
	Reasons []string
}

// PruneResult reports what Prune kept and removed (or, under dryRun,
// would remove). Errs holds per-archive removal failures; one failure
// doesn't stop the rest.
type PruneResult struct {
	Kept    []PrunedArchive
	Removed []PrunedArchive
	Errs    []error
}

// Prune applies p to vmName's archives under destination, holding the
// same per-VM lock run and cleanup use. A held lock returns an error
// wrapping ErrLocked. See
// docs/superpowers/specs/2026-10-28-retention-pruning-design.md.
func Prune(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
	lock, err := AcquireLock(destination, vmName)
	if err != nil {
		return PruneResult{}, err
	}
	defer func() { _ = lock.Release() }()
	return pruneLocked(destination, vmName, p, now, dryRun)
}

// pruneLocked is Prune without taking the lock -- Run calls it directly
// since it already holds the lock. The returned error is reserved for
// failures that prevent pruning at all (listing); per-archive removal
// failures go in PruneResult.Errs.
func pruneLocked(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return PruneResult{}, fmt.Errorf("list archives: %w", err)
	}

	byID := make(map[string]Archive)
	var cands []retention.Candidate
	for _, a := range archives {
		// Manifest VMName equality, never the directory name: archive IDs
		// are "<vm>-<ts>", so "ubuntu" and "ubuntu-dev" share a prefix.
		if a.Manifest.VMName != vmName {
			continue
		}
		byID[a.ArchiveID] = a
		cands = append(cands, retention.Candidate{ID: a.ArchiveID, Timestamp: a.Manifest.Timestamp})
	}

	var res PruneResult
	for _, d := range retention.Select(cands, p, now) {
		pa := PrunedArchive{Archive: byID[d.ID], Reasons: d.Reasons}
		if d.Keep {
			res.Kept = append(res.Kept, pa)
			continue
		}
		res.Removed = append(res.Removed, pa)
		if dryRun {
			continue
		}
		if err := removeArchive(filepath.Join(destination, d.ID)); err != nil {
			res.Errs = append(res.Errs, err)
		}
	}
	return res, nil
}

// removeArchive deletes manifest.json first -- the single step that makes
// dir stop being an archive to ListArchives, list, and restore -- then
// the rest. A failure after that leaves an unlisted directory, never a
// half-deleted archive that still looks restorable. A symlinked archive
// directory is refused outright: removing manifest.json through the link
// would delete inside its target.
func removeArchive(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("prune %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("prune %s: archive directory is a symlink; not deleting through it, remove by hand", dir)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prune %s: remove manifest: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("prune %s: manifest removed but directory left behind, remove by hand: %w", dir, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/backup/ -run TestPrune -v`
Expected: PASS. `TestPrune_MissingDestination_NoArchivesNoError` relies on `ListArchives` returning empty, not an error, for a missing destination (the existing `TestListArchives_MissingDestinationReturnsEmptyNotError` already pins that).

- [ ] **Step 5: Commit**

```bash
git add internal/backup/prune.go internal/backup/prune_test.go
git commit -m "feat(backup): add Prune for retention-policy archive removal

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Prune as `Run`'s last step

**Files:**
- Modify: `internal/backup/choreography.go` (`Options`, `Result`, and the end of `Run`, after `writeManifest` and `succeeded = true`)
- Test: `internal/backup/choreography_test.go` (package `backup_test`)

**Interfaces:**
- Consumes: `pruneLocked`, `PrunedArchive` (Task 2); `retention.Policy` (Task 1).
- Produces:
  ```go
  // Options gains:
  Retention retention.Policy
  // Result gains:
  Pruned   []PrunedArchive // archives removed by the post-backup prune
  PruneErr error           // non-nil if pruning failed in any way; never affects Run's error
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/backup/choreography_test.go`, update `TestRun_HappyPath_ReportsStagesInOrder`'s `wantOrder` to include `progress.Pruning` between `progress.Checksumming` and `progress.Done`:

```go
	wantOrder := []progress.Stage{
		progress.CheckingTools,
		progress.Snapshotting,
		progress.Copying,
		progress.Merging,
		progress.Compressing,
		progress.Checksumming,
		progress.Pruning,
		progress.Done,
	}
```

Add `"github.com/xortim/snapback/internal/retention"` to the imports and append:

```go
// writeOldArchive plants a prior archive for vmName under destination,
// the way an earlier Run would have left it (directory + manifest.json).
func writeOldArchive(t *testing.T, destination, archiveID, vmName string, ts time.Time) string {
	t.Helper()
	dir := filepath.Join(destination, archiveID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(backup.Manifest{VMName: vmName, Timestamp: ts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runOptsForPrune(t *testing.T, dest string, p retention.Policy, now time.Time) backup.Options {
	t.Helper()
	return backup.Options{
		VMName:      "myvm",
		VMXPath:     writeMinimalVMX(t),
		Destination: dest,
		StagingDir:  t.TempDir(),
		Compression: "gzip",
		Retention:   p,
		Now:         func() time.Time { return now },
	}
}

func TestRun_Prune_RemovesOldArchivesOnSuccess(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old1 := writeOldArchive(t, dest, "myvm-20261001T000000Z", "myvm", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	old2 := writeOldArchive(t, dest, "myvm-20261002T000000Z", "myvm", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	res, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, runOptsForPrune(t, dest, retention.Policy{KeepLast: 2}, now))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.PruneErr != nil {
		t.Fatalf("PruneErr = %v, want nil", res.PruneErr)
	}
	if len(res.Pruned) != 1 || res.Pruned[0].ArchiveID != "myvm-20261001T000000Z" {
		t.Errorf("Pruned = %+v, want just myvm-20261001T000000Z", res.Pruned)
	}
	if _, err := os.Stat(old1); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("oldest archive still present (err = %v)", err)
	}
	if _, err := os.Stat(old2); err != nil {
		t.Errorf("second-newest archive removed: %v", err)
	}
}

func TestRun_Prune_KeepsJustWrittenArchive(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	// Same ISO week as now, but older: keep_weekly 1 alone would keep
	// only one archive for this week, and it must be the new one.
	old := writeOldArchive(t, dest, "myvm-20261026T000000Z", "myvm", time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	res, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, runOptsForPrune(t, dest, retention.Policy{KeepWeekly: 1}, now))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Fatalf("just-written archive's manifest gone: %v", err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("older same-week archive still present (err = %v)", err)
	}
}

func TestRun_Prune_FailureIsNonFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection doesn't work as root")
	}
	dest := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old := writeOldArchive(t, dest, "myvm-20261001T000000Z", "myvm", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err := os.Chmod(old, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(old, 0o700) })
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	res, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, runOptsForPrune(t, dest, retention.Policy{KeepLast: 1}, now))
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (prune failure must not fail the backup)", err)
	}
	if res.PruneErr == nil || !strings.Contains(res.PruneErr.Error(), "myvm-20261001T000000Z") {
		t.Errorf("PruneErr = %v, want an error naming the stuck archive", res.PruneErr)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Errorf("new archive missing: %v", err)
	}
}

func TestRun_Prune_NotRunOnFailure(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old := writeOldArchive(t, dest, "myvm-20261001T000000Z", "myvm", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	if _, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, runOptsForPrune(t, dest, retention.Policy{KeepLast: 1}, now)); err == nil {
		t.Fatal("Run() error = nil, want the injected merge failure")
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("old archive removed by a failed run: %v", err)
	}
}
```

(`vm.FakeVMController.DeleteSnapshotErr` is the existing injection field, the same one `TestRun_DeleteSnapshotError_StillCleansUpStaging` uses.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/backup/ -run 'TestRun_(Prune|HappyPath_ReportsStagesInOrder)' -v`
Expected: FAIL to compile (`unknown field Retention`, `res.Pruned undefined`, `res.PruneErr undefined`).

- [ ] **Step 3: Implement**

In `internal/backup/choreography.go`, add the import `"github.com/xortim/snapback/internal/retention"`, then extend `Options`:

```go
type Options struct {
	VMName      string
	VMXPath     string
	Destination string           // parent directory the backup's output directory is created under
	StagingDir  string           // parent directory for the temporary staging copy; os.TempDir() if empty
	Compression string           // "zstd", "gzip", or "" (prefer zstd, fall back to gzip)
	Retention   retention.Policy // applied to this VM's archives after a successful backup; zero keeps everything
	Now         func() time.Time // defaults to time.Now if nil
}
```

Extend `Result`:

```go
type Result struct {
	ArchiveID    string
	OutputDir    string
	ArchivePath  string
	ManifestPath string
	Manifest     Manifest
	// Pruned lists the archives the post-backup prune removed. PruneErr
	// is non-nil if that prune failed in any way; it never makes Run
	// return an error -- the archive just written is good regardless.
	Pruned   []PrunedArchive
	PruneErr error
}
```

Replace the end of `Run`, from `succeeded = true` through the final `return`, with:

```go
	succeeded = true

	// Stage: Pruning -- last, under the lock Run already holds, and only
	// reached once everything above succeeded. now() rather than
	// startTime: day/week buckets are computed in now's location (local
	// time in production), and startTime was forced to UTC.
	reporter.Report(progress.Event{Stage: progress.Pruning, Message: "pruning old archives"})
	pruneRes, pruneErr := pruneLocked(opts.Destination, opts.VMName, opts.Retention, now(), false)
	if len(pruneRes.Errs) > 0 {
		pruneErr = errors.Join(append([]error{pruneErr}, pruneRes.Errs...)...)
	}

	reporter.Report(progress.Event{Stage: progress.Done, Message: "backup complete"})
	return &Result{
		ArchiveID:    archiveID,
		OutputDir:    outputDir,
		ArchivePath:  archivePath,
		ManifestPath: manifestPath,
		Manifest:     manifest,
		Pruned:       pruneRes.Removed,
		PruneErr:     pruneErr,
	}, nil
}
```

(`errors.Join` drops nil arguments, so a nil listing error plus per-archive errors joins to just the per-archive errors.)

- [ ] **Step 4: Run the full backup suite**

Run: `go test ./internal/backup/... -v`
Expected: PASS, including every pre-existing `TestRun_*` test. Those tests pass a zero `Retention`, which keeps everything, so their output directories are untouched.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "feat(backup): prune old archives as Run's final step

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Wire retention into `snapback run`

**Files:**
- Create: `internal/cli/retention.go`
- Modify: `internal/cli/run.go` (`runVMWithConfig`)
- Test: `internal/cli/run_internal_test.go`

**Interfaces:**
- Consumes: `backup.Options.Retention`, `backup.Result.Pruned`, `backup.Result.PruneErr` (Task 3).
- Produces: `func retentionPolicy(r config.Retention) retention.Policy` in package `cli` (Task 5 uses it).

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/run_internal_test.go` (add `"encoding/json"` and `"time"` to imports if missing):

```go
func writeOldCLIArchive(t *testing.T, dest, id, vmName string, ts time.Time) string {
	t.Helper()
	dir := filepath.Join(dest, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(backup.Manifest{VMName: vmName, Timestamp: ts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunCmd_AppliesRetentionAndReportsPrunedCount(t *testing.T) {
	vmxPath := writeVMBundle(t)
	dest := t.TempDir()
	old := writeOldCLIArchive(t, dest, "myvm-20200101T000000Z", "myvm", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: dest,
				Compression: "gzip",
				Retention:   config.Retention{KeepLast: 1},
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old archive still present (err = %v)", err)
	}
	if !strings.Contains(out.String(), "pruned 1 old backup(s)") {
		t.Errorf("stdout = %q, want a pruned-count line", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestRunCmd_PruneFailure_WarnsButSucceeds(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection doesn't work as root")
	}
	vmxPath := writeVMBundle(t)
	dest := t.TempDir()
	old := writeOldCLIArchive(t, dest, "myvm-20200101T000000Z", "myvm", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := os.Chmod(old, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(old, 0o700) })
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: dest,
				Compression: "gzip",
				Retention:   config.Retention{KeepLast: 1},
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil (prune failure is non-fatal)", err)
	}
	if !strings.Contains(errOut.String(), "warning: pruning old backups failed:") {
		t.Errorf("stderr = %q, want the prune-failure warning", errOut.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunCmd_(AppliesRetention|PruneFailure)' -v`
Expected: FAIL. The old archive survives and there's no pruned-count line or warning, because retention isn't passed through yet.

- [ ] **Step 3: Implement**

`internal/cli/retention.go`:

```go
package cli

import (
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

// retentionPolicy converts the config's retention block to the policy
// internal/retention evaluates. Shared by run and prune.
func retentionPolicy(r config.Retention) retention.Policy {
	return retention.Policy{KeepLast: r.KeepLast, KeepDaily: r.KeepDaily, KeepWeekly: r.KeepWeekly}
}
```

In `internal/cli/run.go`'s `runVMWithConfig`, add `Retention` to `opts`:

```go
	opts := backup.Options{
		VMName:      vmCfg.Name,
		VMXPath:     vmCfg.VMX,
		Destination: cfg.Destination,
		Compression: cfg.Compression,
		Retention:   retentionPolicy(cfg.Retention),
	}
```

Change the plain-output success callback passed to `dispatchRun` to:

```go
		func(res *backup.Result) {
			_, _ = fmt.Fprintf(out, "backup complete: %s\n", res.ArchivePath)
			if n := len(res.Pruned); n > 0 {
				_, _ = fmt.Fprintf(out, "pruned %d old backup(s)\n", n)
			}
		},
```

Replace the tail of `runVMWithConfig` (after `dispatchRun`) with:

```go
	if err != nil {
		return nil, err
	}
	// Printed for both the interactive and plain paths -- the TUI shows the
	// pruning stage but not its failure, and for a scheduled run stderr is
	// the per-VM log.
	if result != nil && result.PruneErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: pruning old backups failed: %v\n", result.PruneErr)
	}
	return result, nil
}
```

- [ ] **Step 4: Run the CLI suite**

Run: `go test ./internal/cli/... -v`
Expected: PASS, including the existing `TestRunCmd_HappyPath_PrintsArchivePath`. Its config has zero retention, so nothing is pruned and stderr stays empty.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/retention.go internal/cli/run.go internal/cli/run_internal_test.go
git commit -m "feat(cli): apply retention policy after run, warn on prune failure

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `snapback prune` command

**Files:**
- Create: `internal/cli/prune.go`
- Modify: `internal/cli/root.go` (add `newPruneCmd(),` to `root.AddCommand` after `newCleanupCmd(),`)
- Test: `internal/cli/prune_internal_test.go`

**Interfaces:**
- Consumes: `backup.Prune`, `backup.PruneResult`, `backup.PrunedArchive`, `backup.ErrLocked` (Task 2); `retentionPolicy` (Task 4); existing `loadConfigForCmd`, `findVMConfig`, `swapSubcommand`, `config.Load`.
- Produces: `newPruneCmd() *cobra.Command`, `newPruneCmdWithDeps(pruneDeps) *cobra.Command`, and
  ```go
  type pruneDeps struct {
      loadConfig func(path string) (*config.Config, error)
      prune      func(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (backup.PruneResult, error)
      now        func() time.Time
  }
  ```

- [ ] **Step 1: Write the failing tests**

`internal/cli/prune_internal_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

var pruneTestNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func writePruneArchive(t *testing.T, dest, id, vmName string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dest, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(backup.Manifest{VMName: vmName, Timestamp: ts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func execPrune(t *testing.T, deps pruneDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := swapSubcommand(t, "prune", newPruneCmdWithDeps(deps))
	var out, errOut bytes.Buffer
	root.SetArgs(append([]string{"prune"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func realPruneDeps(cfg *config.Config) pruneDeps {
	return pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune:      backup.Prune,
		now:        func() time.Time { return pruneTestNow },
	}
}

func TestPruneCmd_AllVMs_RemovesAndReports(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-1", "b", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-2", "b", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}

	out, _, err := execPrune(t, realPruneDeps(cfg))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"a-2", "keep (last)", "a-1", "remove", "b-1", "b-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	for _, id := range []string{"a-1", "b-1"} {
		if _, err := os.Stat(filepath.Join(dest, id)); !os.IsNotExist(err) {
			t.Errorf("%s still present (err = %v)", id, err)
		}
	}
}

func TestPruneCmd_VMFlag_OnlyThatVM(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-1", "b", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "b-2", "b", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}

	if _, _, err := execPrune(t, realPruneDeps(cfg), "--vm", "a"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a-1")); !os.IsNotExist(err) {
		t.Errorf("a-1 still present")
	}
	if _, err := os.Stat(filepath.Join(dest, "b-1")); err != nil {
		t.Errorf("b-1 removed despite --vm a: %v", err)
	}
}

func TestPruneCmd_UnknownVM_Errors(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "a"}}}
	_, _, err := execPrune(t, realPruneDeps(cfg), "--vm", "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("Execute() error = %v, want an unknown-VM error naming nope", err)
	}
}

func TestPruneCmd_DryRun_DeletesNothing(t *testing.T) {
	dest := t.TempDir()
	writePruneArchive(t, dest, "a-1", "a", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	writePruneArchive(t, dest, "a-2", "a", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	cfg := &config.Config{Destination: dest, Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}

	out, _, err := execPrune(t, realPruneDeps(cfg), "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "would remove") {
		t.Errorf("stdout = %q, want 'would remove'", out)
	}
	if _, err := os.Stat(filepath.Join(dest, "a-1")); err != nil {
		t.Errorf("dry run deleted a-1: %v", err)
	}
}

func TestPruneCmd_LockedVM_SkippedNotFailed(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}, {Name: "b"}}}
	var pruned []string
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(dest, vmName string, p retention.Policy, now time.Time, dryRun bool) (backup.PruneResult, error) {
			if vmName == "a" {
				return backup.PruneResult{}, fmt.Errorf("%w: %q", backup.ErrLocked, vmName)
			}
			pruned = append(pruned, vmName)
			return backup.PruneResult{}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	out, _, err := execPrune(t, deps)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (locked VM is skipped, not failed)", err)
	}
	if !strings.Contains(out, `backup for "a" may be in progress (lock held); skipping`) {
		t.Errorf("stdout = %q, want a skip notice for a", out)
	}
	if len(pruned) != 1 || pruned[0] != "b" {
		t.Errorf("pruned = %v, want [b]", pruned)
	}
}

func TestPruneCmd_RemovalError_NonZeroExit(t *testing.T) {
	cfg := &config.Config{Destination: t.TempDir(), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	deps := pruneDeps{
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		prune: func(string, string, retention.Policy, time.Time, bool) (backup.PruneResult, error) {
			return backup.PruneResult{Errs: []error{errBoom}}, nil
		},
		now: func() time.Time { return pruneTestNow },
	}

	_, stderr, err := execPrune(t, deps)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for a removal error")
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want the removal error printed", stderr)
	}
}

func TestPruneCmd_NoArchives_ReportsNothingToPrune(t *testing.T) {
	cfg := &config.Config{Destination: filepath.Join(t.TempDir(), "never-created"), Retention: config.Retention{KeepLast: 1}, VMs: []config.VM{{Name: "a"}}}
	out, _, err := execPrune(t, realPruneDeps(cfg))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, `"a": no archives`) {
		t.Errorf("stdout = %q, want a no-archives line for a", out)
	}
}
```

(`errBoom` already exists in package `cli` tests. It's used by `cleanup_internal_test.go`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run TestPruneCmd -v`
Expected: FAIL to compile (`undefined: newPruneCmdWithDeps`, `pruneDeps`). `swapSubcommand` would also fail on the missing `prune` subcommand.

- [ ] **Step 3: Implement**

`internal/cli/prune.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

// pruneDeps groups prune's swappable dependencies: the config loader,
// backup.Prune itself (so tests can inject a held lock or a removal
// error), and the clock.
type pruneDeps struct {
	loadConfig func(path string) (*config.Config, error)
	prune      func(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (backup.PruneResult, error)
	now        func() time.Time
}

func defaultPruneDeps() pruneDeps {
	return pruneDeps{loadConfig: config.Load, prune: backup.Prune, now: time.Now}
}

func newPruneCmd() *cobra.Command {
	return newPruneCmdWithDeps(defaultPruneDeps())
}

func newPruneCmdWithDeps(deps pruneDeps) *cobra.Command {
	var vmName string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove backup archives beyond the retention policy",
		Long:  "Apply the config's retention policy (keep_last, keep_daily, keep_weekly) to each configured VM's archives, or just --vm's. `run` already does this after every successful backup; this is for applying a changed policy or checking it with --dry-run. Archives of VMs no longer in the config are never touched.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runPrune(cmd, deps, vmName, dryRun)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "prune only this configured VM (default: every configured VM)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without deleting anything")
	return cmd
}

func runPrune(cmd *cobra.Command, deps pruneDeps, vmName string, dryRun bool) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vms := cfg.VMs
	if vmName != "" {
		v, ok := findVMConfig(cfg.VMs, vmName)
		if !ok {
			return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
		}
		vms = []config.VM{v}
	}

	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	policy := retentionPolicy(cfg.Retention)
	now := deps.now()
	failed := 0
	for _, v := range vms {
		res, err := deps.prune(cfg.Destination, v.Name, policy, now, dryRun)
		if errors.Is(err, backup.ErrLocked) {
			_, _ = fmt.Fprintf(out, "backup for %q may be in progress (lock held); skipping\n", v.Name)
			continue
		}
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "error: prune %q: %v\n", v.Name, err)
			failed++
			continue
		}
		printPruneResult(out, v.Name, res, dryRun)
		for _, e := range res.Errs {
			_, _ = fmt.Fprintf(errOut, "error: %v\n", e)
		}
		if len(res.Errs) > 0 {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("prune failed for %d VM(s)", failed)
	}
	return nil
}

func printPruneResult(w io.Writer, vmName string, res backup.PruneResult, dryRun bool) {
	if len(res.Kept)+len(res.Removed) == 0 {
		_, _ = fmt.Fprintf(w, "%q: no archives\n", vmName)
		return
	}
	removeVerb := "remove"
	if dryRun {
		removeVerb = "would remove"
	}
	_, _ = fmt.Fprintf(w, "%q:\n", vmName)
	// Same header-bolding table renderer list uses (table.go).
	_ = renderTabwriterTable(w, func(tw *tabwriter.Writer) error {
		_, _ = fmt.Fprintln(tw, "ARCHIVE\tTIMESTAMP\tACTION")
		for _, a := range res.Kept {
			_, _ = fmt.Fprintf(tw, "%s\t%s\tkeep (%s)\n", a.ArchiveID, a.Manifest.Timestamp.Local().Format(time.DateTime), strings.Join(a.Reasons, ", "))
		}
		for _, a := range res.Removed {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", a.ArchiveID, a.Manifest.Timestamp.Local().Format(time.DateTime), removeVerb)
		}
		return nil
	})
	_, _ = fmt.Fprintf(w, "kept %d, %s %d\n", len(res.Kept), removeVerb, len(res.Removed))
}
```

In `internal/cli/root.go`, add `newPruneCmd(),` right after `newCleanupCmd(),` in `root.AddCommand(...)`.

- [ ] **Step 4: Run the CLI suite**

Run: `go test ./internal/cli/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/prune.go internal/cli/prune_internal_test.go internal/cli/root.go
git commit -m "feat(cli): add prune command with --vm and --dry-run

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Docs, full verification

**Files:**
- Modify: `docs/design.md`, `README.md`, `CLAUDE.md`

- [ ] **Step 1: `docs/design.md`**
  - Roadmap: change the Phase 5 line (currently `- [ ] **Phase 5 — Retention:** the retention config ... is parsed, validated, and displayed in status --vm, but nothing enforces it yet — no prune command exists.`) to `- [x] **Phase 5 — Retention:**`. Say that `run` prunes each VM's archives after every successful backup and that `snapback prune [--vm] [--dry-run]` applies the policy on demand, and link the spec `superpowers/specs/2026-10-28-retention-pruning-design.md`.
  - Command reference: change the `snapback prune` row to `snapback prune [--vm <name>] [--dry-run]` with description "Apply the retention policy to every configured VM's archives (or one VM's); `--dry-run` shows what would be removed. `run` already prunes after each successful backup."
  - Config reference: after the YAML block, add a short "Retention semantics" paragraph: time windows (`keep_daily: 7` = newest archive from each of the last 7 calendar days, counted back from now, local time; `keep_weekly` the same over ISO weeks), rules union, newest always kept, all-zero keeps everything, only VMs currently in the config are pruned.

- [ ] **Step 2: `README.md`**
  - Roadmap: Phase 5 checked, with the same one-line summary.
  - Status: append one sentence saying retention pruning has landed (`run` prunes after each successful backup, `snapback prune` on demand).
  - Quick start: add `snapback prune --dry-run              # preview what the retention policy would remove` after the `schedule sync` line.

- [ ] **Step 3: `CLAUDE.md`**
  - In "Project status", add a paragraph after the Phase 2 paragraph saying Phase 5 (retention) has landed: pure `internal/retention.Select` (time-window semantics), `backup.Prune`/`pruneLocked` (manifest-first deletion, symlinked archives refused), called as `Run`'s last step under its lock (non-fatal), plus `snapback prune [--vm] [--dry-run]`.
  - Command surface line: `prune` → `prune [--vm] [--dry-run]`.

- [ ] **Step 4: Run CI's targets**

Run: `make lint && make test && make build`
Expected: all pass. Fix any lint findings inline. If lint fails with a typecheck/export-data error on Go 1.26, bump the golangci-lint pin in the Makefile instead of working around it.

- [ ] **Step 5: Commit**

```bash
git add docs/design.md README.md CLAUDE.md
git commit -m "docs: document retention pruning and the prune command

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
