package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestView_PendingStageShowsCircleIcon(t *testing.T) {
	m := newRunModel("myvm", func() {})
	view := m.View()
	if !strings.Contains(view, "○ checking tools") {
		t.Errorf("view = %q, want a pending-icon row for checking tools", view)
	}
}

func TestView_ActiveStageShowsMessage(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot snapback-x"}))
	m = updated.(Model)
	view := m.View()
	if !strings.Contains(view, "checking tools") {
		t.Errorf("view = %q, want the prior stage still listed", view)
	}
	if !strings.Contains(view, "snapshotting - taking snapshot snapback-x") {
		t.Errorf("view = %q, want the active stage's message shown", view)
	}
}

func TestView_CopyingWithPercent_ShowsProgressBar(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Percent: 0.5}))
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "50%") {
		t.Errorf("view = %q, want a rendered progress percentage", view)
	}
}

func TestView_Success_ShowsArchivePath(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "backup complete: /dest/myvm-x/archive.tar.zst") {
		t.Errorf("view = %q, want the archive path in the completion line", view)
	}
	if !strings.Contains(view, "✓ checksumming") {
		t.Errorf("view = %q, want the last stage checked off on success", view)
	}
}

// TestView_Success_RestoreResult_ShowsNextSteps covers view.go's rendering
// of pipelineResult.NextSteps() below Summary() -- *backup.RestoreResult
// has one (open the bundle to register it with Fusion), unlike
// *backup.Result, whose empty NextSteps() prints nothing (see
// TestView_Success_ShowsArchivePath, which asserts the opposite for a
// backup).
func TestView_Success_RestoreResult_ShowsNextSteps(t *testing.T) {
	m := newModel("snapback restore myvm-x", func() {}, nil, nil)
	result := &backup.RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}
	updated, _ := m.Update(resultMsg{result: result})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, result.Summary()) {
		t.Errorf("view = %q, want the restore summary line", view)
	}
	if !strings.Contains(view, result.NextSteps()) {
		t.Errorf("view = %q, want the restore's next-steps hint", view)
	}
}

// TestView_Success_NilResult_DoesNotPanic covers a backupFn returning
// (nil, nil) -- a valid Go zero-value combination the compiler doesn't
// prevent, and not something backup.Run itself does today, but Model and
// RunInteractive are exported so nothing stops a future or external
// caller's backupFn from doing it. View() must not dereference
// m.result.ArchivePath without checking m.result first.
func TestView_Success_NilResult_DoesNotPanic(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: nil, err: nil})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "complete") {
		t.Errorf("view = %q, want a completion line even with a nil result", view)
	}
}

func TestView_Failure_ShowsErrorOnFailedRowOnly(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"}))
	m = updated.(Model)
	updated, _ = m.Update(resultMsg{err: &backup.RunError{Stage: progress.Merging, Err: errBoom}})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "✗ merging - "+errBoom.Error()) {
		t.Errorf("view = %q, want merging marked failed with the error message", view)
	}
	// The failed row above already carries the full error message -- a
	// second "error: <same message>" summary line would just repeat it
	// (confirmed real case, 2026-09-11: this showed the same multi-line
	// error twice in the terminal). See view.go's finished-state switch.
	if strings.Count(view, errBoom.Error()) != 1 {
		t.Errorf("view = %q, want the error message to appear exactly once", view)
	}
}

// TestView_Failure_NoMatchingRow_FallsBackToSummaryLine covers the
// defensive branch view.go's finished-state switch falls back to when no
// row is marked failed -- not exercised by run/restore's own fixed stage
// lists (applyFinalStatus's own fallback always marks row 0 failed when
// nothing else matches), but Model is exported, so nothing stops a future
// caller from constructing one with an empty stages list.
func TestView_Failure_NoMatchingRow_FallsBackToSummaryLine(t *testing.T) {
	m := newModel("myvm", func() {}, nil, nil)
	updated, _ := m.Update(resultMsg{err: errBoom})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "error: "+errBoom.Error()) {
		t.Errorf("view = %q, want the fallback error summary line when there are no rows", view)
	}
}

func TestView_Cancelling_ShowsCancellingNotice(t *testing.T) {
	m := newRunModel("myvm", func() {})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "cancelling") {
		t.Errorf("view = %q, want a cancelling notice", view)
	}
}
