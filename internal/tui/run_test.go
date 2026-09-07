package tui

import (
	"bytes"
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
