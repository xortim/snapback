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
				cand("w44", utc(2026, 10, 27, 0)),       // Tue, W44
				cand("w43-late", utc(2026, 10, 25, 0)),  // Sun, W43
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
		cand("jan05", utc(2027, 1, 5, 0)),   // 2027-W01
		cand("jan02", utc(2027, 1, 2, 0)),   // 2026-W53
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
