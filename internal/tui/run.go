package tui

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// RunInteractive renders backupFn's progress as an interactive checklist
// written to out, and returns whatever backupFn returns. cancel is
// invoked if the user presses ctrl+c before the run finishes -- the
// caller is responsible for wiring cancel to the same context.Context
// backupFn's underlying backup.Run call actually respects (run.go does
// this via context.WithCancel(cmd.Context())). extraOpts is exposed
// purely for tests, to pass tea.WithInput on a non-terminal reader;
// production callers should leave it empty so bubbletea reads real
// keypresses (ctrl+c) from the real stdin.
func RunInteractive(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error), extraOpts ...tea.ProgramOption) (*backup.Result, error) {
	opts := append([]tea.ProgramOption{tea.WithOutput(out)}, extraOpts...)
	program := tea.NewProgram(newModel(vmName, cancel), opts...)
	reporter := NewReporter(program)

	go func() {
		result, err := backupFn(reporter)
		program.Send(resultMsg{result: result, err: err})
	}()

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}
	m, ok := finalModel.(Model)
	if !ok {
		return nil, fmt.Errorf("unexpected model type %T from bubbletea program", finalModel)
	}
	if !m.finished {
		// program.Run() can return here via bubbletea's own internal
		// SIGINT/SIGTERM handling (its QuitMsg path) without Update ever
		// having processed a resultMsg -- e.g. an external `kill -TERM` or
		// stdin not being a TTY. In that case m.result/m.err are both still
		// nil, and blindly returning them would look exactly like success
		// to the caller (internal/cli's runVM), silently skipping the
		// orphaned-snapshot warning for a backup that never actually
		// finished. Signal the in-flight backupFn goroutine to stop
		// (best-effort -- it may already be past the point where
		// cancellation helps) and report a definite failure instead.
		cancel()
		return nil, errors.New("interactive run ended before the backup finished")
	}
	return m.result, m.err
}
