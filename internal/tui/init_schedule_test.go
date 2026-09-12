package tui

import "testing"

func TestResolveSchedule(t *testing.T) {
	tests := []struct {
		name   string
		choice string
		want   string
	}{
		{"none", scheduleChoiceNone, ""},
		{"daily", scheduleChoiceDaily, "daily"},
		{"weekly", scheduleChoiceWeekly, "weekly"},
		{"monthly", scheduleChoiceMonthly, "monthly"},
	}
	for _, tt := range tests {
		if got := resolveSchedule(tt.choice); got != tt.want {
			t.Errorf("%s: resolveSchedule(%q) = %q, want %q", tt.name, tt.choice, got, tt.want)
		}
	}
}

func TestScheduleChoices_ListsAllFourPresetsInOrder(t *testing.T) {
	want := []string{scheduleChoiceNone, scheduleChoiceDaily, scheduleChoiceWeekly, scheduleChoiceMonthly}
	if len(scheduleChoices) != len(want) {
		t.Fatalf("len(scheduleChoices) = %d, want %d", len(scheduleChoices), len(want))
	}
	for i, w := range want {
		if scheduleChoices[i] != w {
			t.Errorf("scheduleChoices[%d] = %q, want %q", i, scheduleChoices[i], w)
		}
	}
}
