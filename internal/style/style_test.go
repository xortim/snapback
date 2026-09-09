// Package style (internal test package) verifies the shared semantic
// palette pins the exact colors from
// docs/superpowers/specs/2026-08-23-cli-ux-design.md's color table, since a
// typo'd hex here would silently desync run's checklist and status's card
// from the spec they're both supposed to share.
package style

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPalette_ForegroundColorsMatchSpec(t *testing.T) {
	cases := []struct {
		name  string
		style lipgloss.Style
		want  lipgloss.Color
	}{
		{"Done", Done, lipgloss.Color("#04B575")},
		{"Degraded", Degraded, lipgloss.Color("#e3b341")},
		{"Active", Active, lipgloss.Color("#58a6ff")},
		{"Failed", Failed, lipgloss.Color("#f85149")},
	}
	for _, c := range cases {
		if got := c.style.GetForeground(); got != c.want {
			t.Errorf("%s.GetForeground() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPending_IsFaintRatherThanColored(t *testing.T) {
	if !Pending.GetFaint() {
		t.Error("Pending.GetFaint() = false, want true (dim gray per the spec's \"step not yet reached\" row)")
	}
}
