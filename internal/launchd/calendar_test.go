package launchd

import (
	"testing"
	"time"
)

func TestCalendarInterval_ExactKeySets(t *testing.T) {
	tests := []struct {
		schedule string
		want     []calendarKey
	}{
		{"daily", []calendarKey{{"Hour", 0}, {"Minute", 0}}},
		{"weekly", []calendarKey{{"Weekday", 0}, {"Hour", 0}, {"Minute", 0}}},
		{"monthly", []calendarKey{{"Day", 1}, {"Hour", 0}, {"Minute", 0}}},
		{"", nil},
	}
	for _, tt := range tests {
		got := calendarInterval(tt.schedule)
		if len(got) != len(tt.want) {
			t.Fatalf("calendarInterval(%q) = %+v, want %+v", tt.schedule, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("calendarInterval(%q)[%d] = %+v, want %+v", tt.schedule, i, got[i], tt.want[i])
			}
		}
	}
}

func TestCalendarInterval_NeverCombinesDayAndWeekday(t *testing.T) {
	for _, schedule := range []string{"daily", "weekly", "monthly"} {
		var hasDay, hasWeekday bool
		for _, k := range calendarInterval(schedule) {
			if k.Name == "Day" {
				hasDay = true
			}
			if k.Name == "Weekday" {
				hasWeekday = true
			}
		}
		if hasDay && hasWeekday {
			t.Errorf("calendarInterval(%q) includes both Day and Weekday -- Apple's launchd.plist(5) OR-semantics for that combination would make this fire far more often than intended", schedule)
		}
	}
}

// intervalMatches reproduces launchd's own StartCalendarInterval
// matching rule for the dicts this package generates: every present key
// must match t exactly (AND semantics). This package never emits a dict
// with both Day and Weekday present (see the test above), so the
// separate OR-semantics Apple documents for that combination never
// applies to output from calendarInterval -- this helper does not need
// to implement it.
func intervalMatches(interval []calendarKey, t time.Time) bool {
	for _, k := range interval {
		var actual int
		switch k.Name {
		case "Minute":
			actual = t.Minute()
		case "Hour":
			actual = t.Hour()
		case "Day":
			actual = t.Day()
		case "Weekday":
			actual = int(t.Weekday())
		case "Month":
			actual = int(t.Month())
		}
		if actual != k.Value {
			return false
		}
	}
	return true
}

func countMatchesOverYear(t *testing.T, schedule string, year int) int {
	t.Helper()
	interval := calendarInterval(schedule)
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, time.UTC)
	count := 0
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		if intervalMatches(interval, day) {
			count++
		}
	}
	return count
}

func TestCalendarInterval_DailyFiresOncePerDay(t *testing.T) {
	if got := countMatchesOverYear(t, "daily", 2027); got != 365 { // 2027 is not a leap year
		t.Errorf("daily preset matched %d times in 2027, want 365", got)
	}
	if got := countMatchesOverYear(t, "daily", 2028); got != 366 { // 2028 is a leap year
		t.Errorf("daily preset matched %d times in leap year 2028, want 366", got)
	}
}

func TestCalendarInterval_WeeklyFiresOnceAWeek(t *testing.T) {
	got := countMatchesOverYear(t, "weekly", 2027)
	if got != 52 && got != 53 {
		t.Errorf("weekly preset matched %d times in 2027, want 52 or 53 (any Gregorian year has that many occurrences of a given weekday)", got)
	}
}

func TestCalendarInterval_MonthlyFiresOnceAMonth(t *testing.T) {
	if got := countMatchesOverYear(t, "monthly", 2027); got != 12 {
		t.Errorf("monthly preset matched %d times in 2027, want 12", got)
	}
}

// TestCalendarInterval_RegressionForSharedStructBug reproduces the exact
// failure mode a naive shared all-fields struct would have caused if
// this package had used one: an implicit Weekday: 0 leaking into the
// monthly dict, or an implicit Day: 0 leaking into weekly's, engaging
// launchd's documented Day+Weekday OR-semantics and firing several
// times a month instead of once. If calendarInterval regresses to
// including the other axis's key, this test's monthly/weekly match
// counts above would jump well past 12 / 52-53 -- this test exists
// specifically so that jump is caught here, in-process, rather than
// discovered as unwanted 2am backups in production.
func TestCalendarInterval_RegressionForSharedStructBug(t *testing.T) {
	monthly := countMatchesOverYear(t, "monthly", 2027)
	if monthly > 12 {
		t.Fatalf("monthly preset matched %d times in 2027 (want exactly 12) -- Day+Weekday OR-semantics bug has regressed", monthly)
	}
	weekly := countMatchesOverYear(t, "weekly", 2027)
	if weekly > 53 {
		t.Fatalf("weekly preset matched %d times in 2027 (want 52 or 53) -- Day+Weekday OR-semantics bug has regressed", weekly)
	}
}
