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
