package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestView_PendingStageShowsCircleIcon(t *testing.T) {
	m := newModel("myvm", func() {})
	view := m.View()
	if !strings.Contains(view, "○ checking tools") {
		t.Errorf("view = %q, want a pending-icon row for checking tools", view)
	}
}

func TestView_ActiveStageShowsMessage(t *testing.T) {
	m := newModel("myvm", func() {})
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
	m := newModel("myvm", func() {})
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
	m := newModel("myvm", func() {})
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

// TestView_Success_NilResult_DoesNotPanic covers a backupFn returning
// (nil, nil) -- a valid Go zero-value combination the compiler doesn't
// prevent, and not something backup.Run itself does today, but Model and
// RunInteractive are exported so nothing stops a future or external
// caller's backupFn from doing it. View() must not dereference
// m.result.ArchivePath without checking m.result first.
func TestView_Success_NilResult_DoesNotPanic(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: nil, err: nil})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "backup complete") {
		t.Errorf("view = %q, want a completion line even with a nil result", view)
	}
}

func TestView_Failure_ShowsErrorAndCrossIcon(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"}))
	m = updated.(Model)
	updated, _ = m.Update(resultMsg{err: &backup.RunError{Stage: progress.Merging, Err: errBoom}})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "✗ merging") {
		t.Errorf("view = %q, want merging marked failed", view)
	}
	if !strings.Contains(view, "error:") {
		t.Errorf("view = %q, want an error summary line", view)
	}
}

func TestView_Cancelling_ShowsCancellingNotice(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "cancelling") {
		t.Errorf("view = %q, want a cancelling notice", view)
	}
}
