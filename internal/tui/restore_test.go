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

func TestRestoreInteractive_Success_ReturnsResult(t *testing.T) {
	var out bytes.Buffer
	want := &backup.RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		r.Report(progress.Event{Stage: progress.Verifying, Message: "verifying archive checksum"})
		r.Report(progress.Event{Stage: progress.Done, Message: "restore complete"})
		return want, nil
	}

	got, err := RestoreInteractive(&out, "myvm-x", func() {}, restoreFn, tea.WithInput(strings.NewReader("")))
	if err != nil {
		t.Fatalf("RestoreInteractive() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("RestoreInteractive() result = %v, want %v", got, want)
	}
	if !strings.Contains(out.String(), "restore complete: /vms/myvm - backup 2026-09-11.vmwarevm") {
		t.Errorf("output = %q, want the completion line rendered", out.String())
	}
	if !strings.Contains(out.String(), "snapback restore myvm-x") {
		t.Errorf("output = %q, want the restore header rendered", out.String())
	}
}

func TestRestoreInteractive_Failure_ReturnsError(t *testing.T) {
	var out bytes.Buffer
	wantErr := &backup.RestoreError{Stage: progress.CheckingDiskConsistency, Err: errors.New("disk check failed")}

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		r.Report(progress.Event{Stage: progress.CheckingDiskConsistency, Message: "checking restored disk consistency"})
		return nil, wantErr
	}

	_, err := RestoreInteractive(&out, "myvm-x", func() {}, restoreFn, tea.WithInput(strings.NewReader("")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("RestoreInteractive() error = %v, want it to be (or wrap) %v", err, wantErr)
	}
	if !strings.Contains(out.String(), "✗ checking disk consistency") {
		t.Errorf("output = %q, want checking-disk-consistency marked failed", out.String())
	}
}

func TestRestoreInteractive_ProgramQuitsBeforeResult_ReturnsError(t *testing.T) {
	var out bytes.Buffer

	ctx, restoreCancel := context.WithCancel(context.Background())
	defer restoreCancel()
	restoreDone := make(chan struct{})

	restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
		<-ctx.Done()
		close(restoreDone)
		return nil, ctx.Err()
	}
	cancel := func() { restoreCancel() }
	quitEarly := func(_ tea.Model, _ tea.Msg) tea.Msg { return tea.QuitMsg{} }

	got, err := RestoreInteractive(&out, "myvm-x", cancel, restoreFn, tea.WithInput(strings.NewReader("")), tea.WithFilter(quitEarly))
	if !errors.Is(err, ErrInteractiveRunIncomplete) {
		t.Fatalf("RestoreInteractive() error = %v, want ErrInteractiveRunIncomplete", err)
	}
	if got != nil {
		t.Errorf("RestoreInteractive() result = %v, want nil", got)
	}
	select {
	case <-restoreDone:
	default:
		t.Error("restoreFn had not returned by the time RestoreInteractive returned")
	}
}
