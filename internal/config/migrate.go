package config

import "strings"

// migrateLegacySchedule translates a config.VM.Schedule value written
// before ADR-005 (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md)
// narrowed the field to a closed "", "daily", "weekly", "monthly" enum.
// Before that change, main's `init` wizard (internal/tui/init_schedule.go's
// history) wrote raw 5-field cron expressions for its nightly/weekly/
// custom presets -- e.g. "0 2 * * *" for "nightly". Without this, an
// existing config.yaml written by that wizard fails config.Validate on
// every load after upgrading to a build with the closed enum, breaking
// every command (run/list/status/vm add/vm remove/schedule sync) for
// any user who already ran init.
//
// Already-valid enum values pass through unchanged. A value that isn't
// a recognized enum value and doesn't parse as a 5-field cron expression
// is left untouched, so Validate still rejects a genuinely malformed
// schedule instead of this function silently manufacturing a value for
// it.
//
// The cron -> enum mapping is a heuristic, not a lossless translation
// (cron can express schedules the enum can't, like "every Tuesday at
// 3am"): day-of-month takes priority over day-of-week when both are
// pinned, and a cron with neither pinned (any plain daily cron,
// including the wizard's old "nightly" preset) becomes "daily". This
// matches the closest available enum bucket for every pattern the old
// wizard's three presets (nightly/weekly/custom) could produce.
func migrateLegacySchedule(schedule string) string {
	switch schedule {
	case "", "daily", "weekly", "monthly":
		return schedule
	}

	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return schedule
	}
	dayOfMonth, dayOfWeek := fields[2], fields[4]
	switch {
	case dayOfMonth != "*":
		return "monthly"
	case dayOfWeek != "*":
		return "weekly"
	default:
		return "daily"
	}
}
