package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// restoreStages is the fixed, display-order subset of progress.Stage
// values backup.Restore reports.
var restoreStages = []progress.Stage{
	progress.Verifying,
	progress.Extracting,
	progress.CheckingDiskConsistency,
	progress.Placing,
}

var restoreBarStages = []progress.Stage{progress.Verifying, progress.Extracting}

// RestoreInteractive renders restoreFn's progress as an interactive
// checklist written to out, mirroring RunInteractive's shape for restore.
// label is shown in the header ("snapback restore <label>") -- callers
// pass whatever identifies the restore being run (an archive ID, or a VM
// name for the --vm/--latest path).
func RestoreInteractive(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error), extraOpts ...tea.ProgramOption) (*backup.RestoreResult, error) {
	pipelineFn := func(r progress.Reporter) (pipelineResult, error) {
		result, err := restoreFn(r)
		if result == nil {
			return nil, err
		}
		return result, err
	}
	header := fmt.Sprintf("snapback restore %s", label)
	res, err := runInteractivePipeline(out, header, cancel, restoreStages, restoreBarStages, pipelineFn, extraOpts...)
	if res == nil {
		return nil, err
	}
	result, ok := res.(*backup.RestoreResult)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %T from interactive restore", res)
	}
	return result, err
}
