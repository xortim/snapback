package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// errBoom is a shared sentinel error, reused by view_test.go (same
// package) wherever a test just needs some non-nil error, not a
// specific one.
var errBoom = errors.New("boom")

func TestNewModel_StartsAllRowsPending(t *testing.T) {
	m := newRunModel("myvm", func() {})
	if len(m.rows) != len(runStages) {
		t.Fatalf("len(rows) = %d, want %d", len(m.rows), len(runStages))
	}
	for _, row := range m.rows {
		if row.status != pending {
			t.Errorf("stage %v status = %v, want pending", row.stage, row.status)
		}
	}
}

func TestUpdate_EventMsg_MarksActiveAndPriorStagesDone(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot x"}))
	m = updated.(Model)

	for _, row := range m.rows {
		switch row.stage {
		case progress.CheckingTools:
			if row.status != done {
				t.Errorf("CheckingTools status = %v, want done", row.status)
			}
		case progress.Snapshotting:
			if row.status != active {
				t.Errorf("Snapshotting status = %v, want active", row.status)
			}
			if row.message != "taking snapshot x" {
				t.Errorf("Snapshotting message = %q, want %q", row.message, "taking snapshot x")
			}
		default:
			if row.status != pending {
				t.Errorf("%v status = %v, want pending", row.stage, row.status)
			}
		}
	}
}

func TestUpdate_EventMsg_PercentOnlyUpdatesBarWithoutClearingMessage(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Percent: 0.42}))
	m = updated.(Model)

	if !m.showBar {
		t.Error("showBar = false, want true after a Percent-bearing Copying event")
	}
	if m.percent != 0.42 {
		t.Errorf("percent = %v, want 0.42", m.percent)
	}
	for _, row := range m.rows {
		if row.stage == progress.Copying && row.message != "copying VM bundle to staging" {
			t.Errorf("Copying message = %q, want it preserved across the percent-only event", row.message)
		}
	}
}

// TestUpdate_EventMsg_MessageBearingEventDoesNotResetPercent covers a
// message-bearing event (Percent left at its zero value) arriving after
// percent ticks have already advanced the bar -- e.g. a future "resuming
// after retry" event mid-Copying. Applying that event's zero Percent
// would visibly snap the bar back to 0%.
func TestUpdate_EventMsg_MessageBearingEventDoesNotResetPercent(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Percent: 0.75}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "resuming after retry"}))
	m = updated.(Model)

	if m.percent != 0.75 {
		t.Errorf("percent = %v, want 0.75 preserved across a later message-bearing event", m.percent)
	}
}

func TestUpdate_ResultMsg_Success_MarksAllRowsDone(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, cmd := m.Update(resultMsg{result: &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}})
	m = updated.(Model)

	if !m.finished {
		t.Error("finished = false, want true after resultMsg")
	}
	if cmd == nil {
		t.Error("Update(resultMsg) returned a nil cmd, want tea.Quit")
	}
	for _, row := range m.rows {
		if row.status != done {
			t.Errorf("stage %v status = %v, want done after a successful result", row.stage, row.status)
		}
	}
}

func TestUpdate_ResultMsg_Failure_MarksMatchingStageFailed(t *testing.T) {
	m := newRunModel("myvm", func() {})
	// Advance to Merging first, as a real run would.
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"}))
	m = updated.(Model)

	runErr := &backup.RunError{Stage: progress.Merging, Err: errors.New("delete snapshot: boom")}
	updated, _ = m.Update(resultMsg{err: runErr})
	m = updated.(Model)

	for _, row := range m.rows {
		switch row.stage {
		case progress.CheckingTools, progress.Snapshotting, progress.Copying:
			if row.status != done {
				t.Errorf("%v status = %v, want done (already completed before the failure)", row.stage, row.status)
			}
		case progress.Merging:
			if row.status != failed {
				t.Errorf("Merging status = %v, want failed", row.status)
			}
			if row.message != "delete snapshot: boom" {
				t.Errorf("Merging message = %q, want the RunError text", row.message)
			}
		default:
			if row.status != pending {
				t.Errorf("%v status = %v, want pending", row.stage, row.status)
			}
		}
	}
}

func TestUpdate_ResultMsg_FailureBeforeAnyDisplayedStage_MarksFirstRowFailed(t *testing.T) {
	m := newRunModel("myvm", func() {})
	// ctx canceled before Run ever reported CheckingTools.
	runErr := &backup.RunError{Stage: progress.CheckingTools, Err: context.Canceled}
	updated, _ := m.Update(resultMsg{err: runErr})
	m = updated.(Model)

	if m.rows[0].status != failed {
		t.Errorf("rows[0].status = %v, want failed", m.rows[0].status)
	}
}

func TestUpdate_CtrlC_CallsCancelAndSetsCancelling(t *testing.T) {
	var canceled bool
	m := newRunModel("myvm", func() { canceled = true })

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if !canceled {
		t.Error("cancel was not called on ctrl+c")
	}
	if !m.cancelling {
		t.Error("cancelling = false, want true after ctrl+c")
	}
}

func TestUpdate_CtrlC_AfterFinished_DoesNotCallCancel(t *testing.T) {
	var canceled bool
	m := newRunModel("myvm", func() { canceled = true })
	updated, _ := m.Update(resultMsg{result: &backup.Result{}})
	m = updated.(Model)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if canceled {
		t.Error("cancel was called after the run already finished, want no-op")
	}
}

func TestUpdate_Tick_AdvancesElapsedAndReschedules(t *testing.T) {
	m := newRunModel("myvm", func() {})
	m.start = time.Now().Add(-5 * time.Second)

	updated, cmd := m.Update(tickMsg(time.Now()))
	m = updated.(Model)

	if m.elapsed < 4*time.Second {
		t.Errorf("elapsed = %v, want >= ~5s", m.elapsed)
	}
	if cmd == nil {
		t.Error("Update(tickMsg) returned a nil cmd, want another tick scheduled")
	}
}

func TestUpdate_Tick_AfterFinished_DoesNotReschedule(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: &backup.Result{}})
	m = updated.(Model)

	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd != nil {
		t.Error("Update(tickMsg) after finished returned a non-nil cmd, want nil (no more ticks)")
	}
}
