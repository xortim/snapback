package tui

import (
	"fmt"
	"strings"
	"time"

	bprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/xortim/snapback/internal/progress"
)

// Palette per docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// semantic color table. Yellow (crash-consistent tools state) isn't used
// for stage rows here -- progress.Event doesn't carry tools_state, only
// the manifest does after a run completes -- so it's reserved for the
// cancelling notice instead, which is a real "degraded, not failed"
// signal available today.
var (
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff"))
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149"))
	pendingStyle = lipgloss.NewStyle().Faint(true)
	noticeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341"))
)

const barWidth = 40

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "snapback run --vm %s\n\n", m.vmName)

	for _, row := range m.rows {
		b.WriteString(renderRow(row))
		b.WriteString("\n")
		if row.status == active && m.showBar && (row.stage == progress.Copying || row.stage == progress.Compressing) {
			bar := bprogress.New(bprogress.WithDefaultGradient())
			bar.Width = barWidth
			b.WriteString("  " + bar.ViewAs(m.percent) + "\n")
		}
	}

	fmt.Fprintf(&b, "\nelapsed: %s\n", m.elapsed.Round(time.Second))

	if m.cancelling && !m.finished {
		b.WriteString(noticeStyle.Render("cancelling... (waiting for the current step to finish)") + "\n")
	}
	if m.finished {
		if m.err == nil && m.result != nil {
			b.WriteString(doneStyle.Render(fmt.Sprintf("backup complete: %s", m.result.ArchivePath)) + "\n")
		} else if m.err == nil {
			b.WriteString(doneStyle.Render("backup complete") + "\n")
		} else {
			b.WriteString(failStyle.Render(fmt.Sprintf("error: %v", m.err)) + "\n")
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
		return "✓", doneStyle
	case active:
		return "◐", activeStyle
	case failed:
		return "✗", failStyle
	default:
		return "○", pendingStyle
	}
}
