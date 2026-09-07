// Package tui renders backup.Run's progress as an interactive bubbletea
// checklist for a real terminal, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md. It depends on
// internal/progress (the Event vocabulary) and internal/backup (only for
// the plain *backup.Result/*backup.RunError data types) -- never the
// reverse. Choreography code stays free of any rendering import.
package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// stages is the fixed, display-order subset of progress.Stage values
// backup.Run actually reports today (Pruning/Notifying exist as Stage
// constants for future phases but aren't emitted yet, so they're left
// off this checklist rather than shown permanently pending).
var stages = []progress.Stage{
	progress.CheckingTools,
	progress.Snapshotting,
	progress.Copying,
	progress.Merging,
	progress.Compressing,
	progress.Checksumming,
}

type stageStatus int

const (
	pending stageStatus = iota
	active
	done
	failed
)

type stageRow struct {
	stage   progress.Stage
	status  stageStatus
	message string
}

// Model is a bubbletea model rendering one run's progress. Exported so
// internal/cli/run.go's tests can reference it if needed, though normal
// callers only interact with it via RunInteractive.
type Model struct {
	vmName     string
	cancel     context.CancelFunc
	rows       []stageRow
	percent    float64
	showBar    bool
	start      time.Time
	elapsed    time.Duration
	result     *backup.Result
	err        error
	finished   bool
	cancelling bool
}

func newModel(vmName string, cancel context.CancelFunc) Model {
	rows := make([]stageRow, len(stages))
	for i, s := range stages {
		rows[i] = stageRow{stage: s, status: pending}
	}
	return Model{
		vmName: vmName,
		cancel: cancel,
		rows:   rows,
		start:  time.Now(),
	}
}

type eventMsg progress.Event

type resultMsg struct {
	result *backup.Result
	err    error
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC && !m.finished {
			m.cancelling = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	case tickMsg:
		if m.finished {
			return m, nil
		}
		m.elapsed = time.Since(m.start)
		return m, tickCmd()
	case eventMsg:
		m.applyEvent(progress.Event(msg))
		return m, nil
	case resultMsg:
		m.result = msg.result
		m.err = msg.err
		m.finished = true
		m.applyFinalStatus()
		return m, tea.Quit
	}
	return m, nil
}

// applyEvent updates rows in place for a live progress.Event: every
// stage before e.Stage in the fixed display order is marked done (a
// stage that already finished, since events arrive in pipeline order),
// e.Stage itself becomes active, and a Percent-bearing event (the
// per-file ticks during Copying/Compressing) updates the bar without
// clearing that stage's last Message.
func (m *Model) applyEvent(e progress.Event) {
	idx := -1
	for i, row := range m.rows {
		if row.stage == e.Stage {
			idx = i
			break
		}
	}
	if idx == -1 {
		// Stage not in the displayed checklist (e.g. Done) -- nothing to
		// update here; resultMsg drives final-state rendering instead.
		return
	}
	for i := 0; i < idx; i++ {
		if m.rows[i].status != failed {
			m.rows[i].status = done
		}
	}
	m.rows[idx].status = active
	if e.Message != "" {
		m.rows[idx].message = e.Message
	}
	if e.Stage == progress.Copying || e.Stage == progress.Compressing {
		m.percent = e.Percent
		m.showBar = true
	}
}

// applyFinalStatus marks every row done (success) or the row matching
// the failing RunError's Stage as failed (leaving earlier rows done, so
// the failure is legible in context per the spec), called once when
// resultMsg arrives.
func (m *Model) applyFinalStatus() {
	if m.err == nil {
		for i := range m.rows {
			m.rows[i].status = done
		}
		return
	}
	var runErr *backup.RunError
	if errors.As(m.err, &runErr) {
		for i := range m.rows {
			if m.rows[i].stage == runErr.Stage {
				m.rows[i].status = failed
				m.rows[i].message = runErr.Err.Error()
				return
			}
		}
	}
	if len(m.rows) > 0 {
		m.rows[0].status = failed
		m.rows[0].message = m.err.Error()
	}
}
