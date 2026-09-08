package tui

const (
	scheduleChoiceNone    = "none"
	scheduleChoiceNightly = "nightly"
	scheduleChoiceWeekly  = "weekly"
	scheduleChoiceCustom  = "custom"

	cronNightly = "0 2 * * *"
	cronWeekly  = "0 2 * * 0"
)

// scheduleChoices lists the schedule presets offered per VM, in display
// order, per docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// "presets: nightly/weekly/custom cron". "none" is included because
// nothing consumes config.VM.Schedule yet (launchd wiring is unbuilt --
// see CLAUDE.md's "Other components" table) and the field is already
// `omitempty`, so leaving a VM unscheduled is an existing, legitimate
// state, not a gap this wizard needs to force a choice around.
var scheduleChoices = []string{scheduleChoiceNone, scheduleChoiceNightly, scheduleChoiceWeekly, scheduleChoiceCustom}

// resolveSchedule turns a schedule preset choice (one of scheduleChoices)
// plus whatever the always-asked custom-cron field held into the string
// stored on config.VM.Schedule. custom is ignored unless choice is
// scheduleChoiceCustom.
//
// The custom-cron field is asked unconditionally, on every VM, not only
// when choice == scheduleChoiceCustom -- huh's accessible-mode form
// runner does not consult Group.WithHideFunc (verified by reading
// huh@v1.0.0/form.go's runAccessible, which iterates every group via
// f.selector.Range with no hide check, unlike the real interactive
// path), so a conditionally-hidden group would still be prompted in
// accessible mode. Asking it unconditionally keeps both modes identical
// instead of silently diverging.
func resolveSchedule(choice, custom string) string {
	switch choice {
	case scheduleChoiceNightly:
		return cronNightly
	case scheduleChoiceWeekly:
		return cronWeekly
	case scheduleChoiceCustom:
		return custom
	default:
		return ""
	}
}
