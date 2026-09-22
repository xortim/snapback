package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
)

// dispatchRun runs runFn either through an interactive TUI renderer (when
// out is a terminal and interactive is configured) or via a plain
// progress.TerminalReporter, and is the one place that decision is made --
// extracted from run.go and restore.go, which used to each carry an
// almost-identical copy (see #79). An interactive run gets its own
// cancelable context so ctrl+c can stop it mid-flight, and cobra's own
// default error print is silenced afterward -- except for
// tui.ErrInteractiveRunIncomplete, where the TUI exited before ever
// rendering its own "error: ..." line, so cobra's line is the only
// message the user gets. A plain run uses cmd.Context() directly and
// always leaves cobra's default error print enabled.
//
// onError, if non-nil, runs after either path fails, before the error is
// returned -- e.g. run.go's orphaned-snapshot warning; restore.go has no
// analogous side effect and passes nil. onSuccess, if non-nil, renders
// the plain path's success output; it is never called on the interactive
// path, which has already rendered its own success state by the time
// interactive returns.
func dispatchRun[R any](
	cmd *cobra.Command,
	out io.Writer,
	isTerminal func(w io.Writer) bool,
	interactive func(out io.Writer, label string, cancel context.CancelFunc, fn func(progress.Reporter) (R, error)) (R, error),
	label string,
	runFn func(ctx context.Context, r progress.Reporter) (R, error),
	onError func(err error),
	onSuccess func(result R),
) error {
	if isTerminal != nil && isTerminal(out) && interactive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		fn := func(r progress.Reporter) (R, error) {
			return runFn(ctx, r)
		}
		_, err := interactive(out, label, cancel, fn)
		if err != nil {
			if !errors.Is(err, tui.ErrInteractiveRunIncomplete) {
				cmd.SilenceErrors = true
			}
			if onError != nil {
				onError(err)
			}
			return err
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := runFn(cmd.Context(), reporter)
	if err != nil {
		if onError != nil {
			onError(err)
		}
		return err
	}

	if onSuccess != nil {
		onSuccess(result)
	}
	return nil
}
