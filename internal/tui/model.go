// Package tui renders a backup or restore pipeline's progress as an
// interactive bubbletea checklist for a real terminal, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md and
// docs/superpowers/specs/2026-09-11-restore-design.md. It depends on
// internal/progress (the Event vocabulary) and internal/backup (only for
// the plain result/error data types run.go/restore.go's exported entry
// points return) -- never the reverse. Choreography code stays free of
// any rendering import.
package tui

import (
	"context"
	"errors"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

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

// Model is a bubbletea model rendering one pipeline run's progress.
// Normal callers only interact with it via RunInteractive/
// RestoreInteractive.
type Model struct {
	header     string
	cancel     context.CancelFunc
	rows       []stageRow
	barStages  []progress.Stage
	percent    float64
	showBar    bool
	start      time.Time
	elapsed    time.Duration
	result     pipelineResult
	err        error
	finished   bool
	cancelling bool
}

func newModel(header string, cancel context.CancelFunc, stages, barStages []progress.Stage) Model {
	rows := make([]stageRow, len(stages))
	for i, s := range stages {
		rows[i] = stageRow{stage: s, status: pending}
	}
	return Model{
		header:    header,
		cancel:    cancel,
		rows:      rows,
		barStages: barStages,
		start:     time.Now(),
	}
}

type eventMsg progress.Event

type resultMsg struct {
	result pipelineResult
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

// applyEvent updates rows in place for a live progress.Event: every stage
// before e.Stage in the fixed display order is marked done, e.Stage itself
// becomes active, and a Percent-bearing event updates the bar without
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
	if slices.Contains(m.barStages, e.Stage) {
		m.showBar = true
		if e.Message == "" {
			m.percent = e.Percent
		}
	}
}

// applyFinalStatus marks every row done (success) or the row matching the
// failing error's Stage as failed, called once when resultMsg arrives.
func (m *Model) applyFinalStatus() {
	if m.err == nil {
		for i := range m.rows {
			m.rows[i].status = done
		}
		return
	}
	var perr pipelineError
	if errors.As(m.err, &perr) {
		stage := perr.FailedStage()
		for i := range m.rows {
			if m.rows[i].stage == stage {
				m.rows[i].status = failed
				m.rows[i].message = perr.Error()
				return
			}
		}
	}
	if len(m.rows) > 0 {
		m.rows[0].status = failed
		m.rows[0].message = m.err.Error()
	}
}
