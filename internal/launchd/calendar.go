package launchd

// calendarKey is one key/value pair of a StartCalendarInterval plist
// dict (e.g. {"Hour", 0}). Represented as an ordered slice, not a map or
// a fixed all-fields struct -- see calendarInterval's doc comment for
// why the ordering and the "only include keys this preset needs" rule
// both matter.
type calendarKey struct {
	Name  string
	Value int
}

// calendarInterval returns the StartCalendarInterval keys for schedule
// ("daily", "weekly", or "monthly"; nil for "" or any other value),
// matching cron's own @daily/@weekly/@monthly meta-schedules: fixed
// midnight-based times, no time-of-day override.
//
// Each preset includes *only* the keys it needs -- daily never mentions
// Day/Weekday/Month at all, weekly never mentions Day, monthly never
// mentions Weekday. This is deliberate, not an oversight: Apple's
// launchd.plist(5) documents that missing keys are wildcards, but if
// both Day and Weekday are present in the same dict they're combined
// with OR, not AND ("the job will be started if either one matches").
// A shared Go struct covering all five calendar fields would make this
// trivial to get wrong by accident -- a plain int field's zero value is
// indistinguishable from an intentionally-set 0, so a monthly dict built
// from such a struct would carry an implicit, un-intended Weekday: 0
// alongside its real Day: 1, and OR-semantics would then fire the job
// every Sunday in addition to the 1st. Returning a hand-built slice per
// preset, containing only the relevant keys, makes that combination
// structurally impossible to introduce by accident.
func calendarInterval(schedule string) []calendarKey {
	switch schedule {
	case "daily":
		return []calendarKey{{"Hour", 0}, {"Minute", 0}}
	case "weekly":
		return []calendarKey{{"Weekday", 0}, {"Hour", 0}, {"Minute", 0}} // Sunday
	case "monthly":
		return []calendarKey{{"Day", 1}, {"Hour", 0}, {"Minute", 0}} // 1st of the month
	default:
		return nil
	}
}
