package tui

const (
	scheduleChoiceNone    = "none"
	scheduleChoiceDaily   = "daily"
	scheduleChoiceWeekly  = "weekly"
	scheduleChoiceMonthly = "monthly"
)

// scheduleChoices lists the schedule presets offered per VM, in display
// order. Each choice's string value doubles as the config.VM.Schedule
// value it resolves to (see resolveSchedule) -- ADR-005
// (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md)
// narrowed Schedule from free-form cron syntax to this closed enum,
// matching cron's own @daily/@weekly/@monthly meta-schedules (fixed
// midnight-based times, no time-of-day override). "none" is the only
// choice that isn't also a valid Schedule value; resolveSchedule maps it
// to "".
var scheduleChoices = []string{scheduleChoiceNone, scheduleChoiceDaily, scheduleChoiceWeekly, scheduleChoiceMonthly}

// resolveSchedule turns a schedule preset choice (one of scheduleChoices)
// into the string stored on config.VM.Schedule: a direct passthrough for
// every choice except "none", which resolves to "" (unscheduled).
func resolveSchedule(choice string) string {
	if choice == scheduleChoiceNone {
		return ""
	}
	return choice
}
