// Package style holds the semantic color palette from
// docs/superpowers/specs/2026-08-23-cli-ux-design.md's "Color palette —
// semantic status" table, shared by every rendering surface (run's TUI
// checklist, status's card/table views) so the same four colors mean the
// same four things everywhere in the CLI, per that spec's explicit
// requirement.
package style

import "github.com/charmbracelet/lipgloss"

var (
	// Done marks a step complete and fully verified (e.g. a backup taken
	// while VMware Tools were running and the guest was quiesced).
	Done = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	// Degraded marks a step complete but not fully verified (e.g. a
	// crash-consistent backup — tools were not running).
	Degraded = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341"))
	// Active marks a step currently in progress.
	Active = lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff"))
	// Failed marks an error.
	Failed = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149"))
	// Pending marks a step not yet reached -- dim rather than colored, per
	// the spec's "Gray (dim)" row.
	Pending = lipgloss.NewStyle().Faint(true)
	// Header marks a tabwriter table's header row -- bold rather than
	// colored, so it reads clearly against any terminal theme without
	// picking a color that has to coexist with the semantic palette above.
	Header = lipgloss.NewStyle().Bold(true)
	// Hint marks de-emphasized footnote text below a table (e.g. a "run
	// this other command" pointer) -- visually identical to Pending (dim
	// rather than colored) but kept as its own name since the two mean
	// different things: Pending is progress-state semantics, Hint is just
	// "this line is a footnote, not data."
	Hint = lipgloss.NewStyle().Faint(true)
)
