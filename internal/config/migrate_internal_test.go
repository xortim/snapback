package config

import "testing"

func TestMigrateLegacySchedule_PassesThroughValidEnumValues(t *testing.T) {
	for _, sched := range []string{"", "daily", "weekly", "monthly"} {
		if got := migrateLegacySchedule(sched); got != sched {
			t.Errorf("migrateLegacySchedule(%q) = %q, want unchanged", sched, got)
		}
	}
}

func TestMigrateLegacySchedule_NightlyCronBecomesDaily(t *testing.T) {
	// "0 2 * * *" is cronNightly from the pre-ADR-005 wizard
	// (internal/tui/init_schedule.go on main): every day, no
	// day-of-month or day-of-week restriction.
	if got := migrateLegacySchedule("0 2 * * *"); got != "daily" {
		t.Errorf("migrateLegacySchedule(nightly cron) = %q, want %q", got, "daily")
	}
}

func TestMigrateLegacySchedule_WeeklyCronBecomesWeekly(t *testing.T) {
	// "0 2 * * 0" is cronWeekly from the pre-ADR-005 wizard: fixed
	// day-of-week (Sunday), no day-of-month restriction.
	if got := migrateLegacySchedule("0 2 * * 0"); got != "weekly" {
		t.Errorf("migrateLegacySchedule(weekly cron) = %q, want %q", got, "weekly")
	}
}

func TestMigrateLegacySchedule_CustomCronWithDayOfMonthBecomesMonthly(t *testing.T) {
	// A user-typed "custom" cron (the wizard's third preset) pinning a
	// day-of-month, e.g. "run on the 1st": day-of-month takes priority
	// over day-of-week in the heuristic since it's the more specific
	// constraint.
	if got := migrateLegacySchedule("0 3 1 * *"); got != "monthly" {
		t.Errorf("migrateLegacySchedule(day-of-month cron) = %q, want %q", got, "monthly")
	}
}

func TestMigrateLegacySchedule_CustomCronWithDayOfWeekBecomesWeekly(t *testing.T) {
	if got := migrateLegacySchedule("30 4 * * 3"); got != "weekly" {
		t.Errorf("migrateLegacySchedule(day-of-week cron) = %q, want %q", got, "weekly")
	}
}

func TestMigrateLegacySchedule_UnparseableGarbageIsLeftUnchanged(t *testing.T) {
	// Not 5 fields -- Validate must still reject this as a genuinely
	// malformed schedule, not have it silently coerced to something.
	if got := migrateLegacySchedule("bogus"); got != "bogus" {
		t.Errorf("migrateLegacySchedule(garbage) = %q, want it left unchanged for Validate to reject", got)
	}
}
