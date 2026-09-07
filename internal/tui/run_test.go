package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestRunInteractive_Success_ReturnsResult(t *testing.T) {
	var out bytes.Buffer
	want := &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}

	backupFn := func(r progress.Reporter) (*backup.Result, error) {
		r.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking VMware Tools state"})
		r.Report(progress.Event{Stage: progress.Done, Message: "backup complete"})
		return want, nil
	}

	got, err := RunInteractive(&out, "myvm", func() {}, backupFn, tea.WithInput(strings.NewReader("")))
	if err != nil {
		t.Fatalf("RunInteractive() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("RunInteractive() result = %v, want %v", got, want)
	}
	if !strings.Contains(out.String(), "backup complete: /dest/myvm-x/archive.tar.zst") {
		t.Errorf("output = %q, want the completion line rendered", out.String())
	}
}

func TestRunInteractive_Failure_ReturnsError(t *testing.T) {
	var out bytes.Buffer
	wantErr := &backup.RunError{Stage: progress.Merging, Err: errors.New("delete snapshot: boom")}

	backupFn := func(r progress.Reporter) (*backup.Result, error) {
		r.Report(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"})
		return nil, wantErr
	}

	_, err := RunInteractive(&out, "myvm", func() {}, backupFn, tea.WithInput(strings.NewReader("")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunInteractive() error = %v, want it to be (or wrap) %v", err, wantErr)
	}
	if !strings.Contains(out.String(), "✗ merging") {
		t.Errorf("output = %q, want merging marked failed", out.String())
	}
}

// TestRunInteractive_ProgramQuitsBeforeResult_ReturnsError covers the case
// where bubbletea's Program.Run() returns via its own internal QuitMsg
// handling before Model.Update ever processes a resultMsg -- e.g. what
// happens internally on a real SIGTERM (see handleSignals in bubbletea's
// tea.go, which pushes a bare QuitMsg for any signal other than SIGINT).
// eventLoop handles a QuitMsg by returning the model exactly as it stood
// (here: zero value, finished == false) without calling Update at all, so
// m.result/m.err are both nil -- the bug being fixed is RunInteractive
// blindly returning that as (nil, nil), which looks like success.
//
// Reproducing a real SIGTERM from a test is racy (it can arrive before
// bubbletea's own signal.Notify has registered, which would kill the whole
// test process under the default disposition). tea.WithFilter is
// bubbletea's public hook for intercepting every message before it's
// processed; forcing the very first message the program receives to a
// QuitMsg drives the exact same eventLoop bypass deterministically,
// without depending on OS signal timing.
func TestRunInteractive_ProgramQuitsBeforeResult_ReturnsError(t *testing.T) {
	var out bytes.Buffer

	ctx, backupCancel := context.WithCancel(context.Background())
	defer backupCancel()
	backupDone := make(chan struct{})

	backupFn := func(r progress.Reporter) (*backup.Result, error) {
		<-ctx.Done() // blocks until RunInteractive's cancel() call (below) unblocks it
		close(backupDone)
		return nil, ctx.Err()
	}

	cancel := func() { backupCancel() }

	quitEarly := func(_ tea.Model, _ tea.Msg) tea.Msg {
		return tea.QuitMsg{}
	}

	got, err := RunInteractive(&out, "myvm", cancel, backupFn, tea.WithInput(strings.NewReader("")), tea.WithFilter(quitEarly))
	if !errors.Is(err, ErrInteractiveRunIncomplete) {
		t.Fatalf("RunInteractive() error = %v, want ErrInteractiveRunIncomplete", err)
	}
	if got != nil {
		t.Errorf("RunInteractive() result = %v, want nil", got)
	}

	// RunInteractive waits for backupFn to actually return before it
	// returns itself, so backupDone must already be closed here -- no
	// select/timeout race needed.
	select {
	case <-backupDone:
	default:
		t.Error("backupFn had not returned by the time RunInteractive returned")
	}
}
