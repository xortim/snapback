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

// ErrInteractiveRunIncomplete is returned by RunInteractive/
// RestoreInteractive when the bubbletea program exits (e.g. via its own
// SIGINT/SIGTERM handling) before ever processing a resultMsg -- unlike
// every other error these return, no "error: <err>" line was ever
// rendered to out, so callers must not treat this the same as an error
// the TUI already displayed (see internal/cli/run.go's use of errors.Is
// here).
var ErrInteractiveRunIncomplete = errors.New("interactive run ended before the backup finished")

// runStages is the fixed, display-order subset of progress.Stage values
// backup.Run actually reports today (Pruning/Notifying exist as Stage
// constants for future phases but aren't emitted yet, so they're left off
// this checklist rather than shown permanently pending).
var runStages = []progress.Stage{
	progress.CheckingTools,
	progress.Snapshotting,
	progress.Copying,
	progress.Merging,
	progress.Compressing,
	progress.Checksumming,
}

var runBarStages = []progress.Stage{progress.Copying, progress.Compressing}

// newRunModel builds a Model configured for run's header/stage
// list/bar stages -- the shape newModel had before this file's
// generalization. model_test.go and view_test.go call this (instead of
// newModel directly) so their assertions are unaffected by newModel's
// wider signature.
func newRunModel(vmName string, cancel context.CancelFunc) Model {
	return newModel(fmt.Sprintf("snapback run --vm %s", vmName), cancel, runStages, runBarStages)
}

// RunInteractive renders backupFn's progress as an interactive checklist
// written to out, and returns whatever backupFn returns. cancel is invoked
// if the user presses ctrl+c before the run finishes -- the caller is
// responsible for wiring cancel to the same context.Context backupFn's
// underlying backup.Run call actually respects (run.go does this via
// context.WithCancel(cmd.Context())). extraOpts is exposed purely for
// tests, to pass tea.WithInput on a non-terminal reader; production
// callers should leave it empty so bubbletea reads real keypresses
// (ctrl+c) from the real stdin.
func RunInteractive(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error), extraOpts ...tea.ProgramOption) (*backup.Result, error) {
	pipelineFn := func(r progress.Reporter) (pipelineResult, error) {
		result, err := backupFn(r)
		if result == nil {
			return nil, err
		}
		return result, err
	}
	header := fmt.Sprintf("snapback run --vm %s", vmName)
	res, err := runInteractivePipeline(out, header, cancel, runStages, runBarStages, pipelineFn, extraOpts...)
	if res == nil {
		return nil, err
	}
	result, ok := res.(*backup.Result)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T from interactive run", res)
	}
	return result, err
}

// runInteractivePipeline is the generalized internals RunInteractive and
// RestoreInteractive both wrap, supplying their own header/stage
// list/bar stages. pipelineFn runs the actual backup/restore against
// reporter, returning nil (not a nil-valued concrete pointer wrapped in a
// non-nil interface) on error.
func runInteractivePipeline(out io.Writer, header string, cancel context.CancelFunc, stages, barStages []progress.Stage, pipelineFn func(progress.Reporter) (pipelineResult, error), extraOpts ...tea.ProgramOption) (pipelineResult, error) {
	opts := append([]tea.ProgramOption{tea.WithOutput(out)}, extraOpts...)
	program := tea.NewProgram(newModel(header, cancel, stages, barStages), opts...)
	reporter := NewReporter(program)

	done := make(chan struct{})
	go func() {
		result, err := pipelineFn(reporter)
		program.Send(resultMsg{result: result, err: err})
		close(done)
	}()

	finalModel, err := program.Run()
	if err != nil {
		cancel()
		<-done
		return nil, err
	}
	m, ok := finalModel.(Model)
	if !ok {
		cancel()
		<-done
		return nil, fmt.Errorf("unexpected model type %T from bubbletea program", finalModel)
	}
	if !m.finished {
		cancel()
		<-done
		return nil, ErrInteractiveRunIncomplete
	}
	<-done
	return m.result, m.err
}
