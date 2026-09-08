package tui

import "testing"

func TestResolveSchedule(t *testing.T) {
	tests := []struct {
		name   string
		choice string
		custom string
		want   string
	}{
		{"none", scheduleChoiceNone, "", ""},
		{"none ignores stray custom text", scheduleChoiceNone, "0 3 * * *", ""},
		{"nightly", scheduleChoiceNightly, "", cronNightly},
		{"weekly", scheduleChoiceWeekly, "", cronWeekly},
		{"custom", scheduleChoiceCustom, "0 3 * * 1", "0 3 * * 1"},
	}
	for _, tt := range tests {
		if got := resolveSchedule(tt.choice, tt.custom); got != tt.want {
			t.Errorf("%s: resolveSchedule(%q, %q) = %q, want %q", tt.name, tt.choice, tt.custom, got, tt.want)
		}
	}
}

func TestScheduleChoices_ListsAllFourPresetsInOrder(t *testing.T) {
	want := []string{scheduleChoiceNone, scheduleChoiceNightly, scheduleChoiceWeekly, scheduleChoiceCustom}
	if len(scheduleChoices) != len(want) {
		t.Fatalf("len(scheduleChoices) = %d, want %d", len(scheduleChoices), len(want))
	}
	for i, w := range want {
		if scheduleChoices[i] != w {
			t.Errorf("scheduleChoices[%d] = %q, want %q", i, scheduleChoices[i], w)
		}
	}
}
