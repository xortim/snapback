package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	bprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/xortim/snapback/internal/style"
)

const barWidth = 40

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", m.header)

	for _, row := range m.rows {
		b.WriteString(renderRow(row))
		b.WriteString("\n")
		if row.status == active && m.showBar && slices.Contains(m.barStages, row.stage) {
			bar := bprogress.New(bprogress.WithDefaultGradient())
			bar.Width = barWidth
			b.WriteString("  " + bar.ViewAs(m.percent) + "\n")
		}
	}

	fmt.Fprintf(&b, "\nelapsed: %s\n", m.elapsed.Round(time.Second))

	if m.cancelling && !m.finished {
		b.WriteString(style.Degraded.Render("cancelling... (waiting for the current step to finish)") + "\n")
	}
	if m.finished {
		switch {
		case m.err == nil && m.result != nil:
			b.WriteString(style.Done.Render(m.result.Summary()) + "\n")
			if next := m.result.NextSteps(); next != "" {
				b.WriteString(style.Hint.Render(next) + "\n")
			}
		case m.err == nil:
			b.WriteString(style.Done.Render("complete") + "\n")
		case !slices.ContainsFunc(m.rows, func(r stageRow) bool { return r.status == failed }):
			// applyFinalStatus always marks some row failed with the full
			// error message when m.rows is non-empty (the case for every
			// real run/restore stage list) -- this line is a fallback for
			// the only scenario where that doesn't happen (an empty rows
			// list), not a normal-path summary. Printing it unconditionally
			// duplicated the failed row's own message verbatim underneath
			// it on every failure (confirmed real case, 2026-09-11).
			b.WriteString(style.Failed.Render(fmt.Sprintf("error: %v", m.err)) + "\n")
		}
	}
	return b.String()
}

func renderRow(row stageRow) string {
	icon, style := iconFor(row.status)
	line := icon + " " + row.stage.String()
	if row.message != "" {
		line += " - " + row.message
	}
	return style.Render(line)
}

func iconFor(s stageStatus) (string, lipgloss.Style) {
	switch s {
	case done:
		return "✓", style.Done
	case active:
		return "◐", style.Active
	case failed:
		return "✗", style.Failed
	default:
		return "○", style.Pending
	}
}
