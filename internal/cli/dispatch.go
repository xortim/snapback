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
	fail := func(err error) error {
		if onError != nil {
			onError(err)
		}
		return err
	}

	if isTerminalWriter(isTerminal, out) && interactive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		fn := func(r progress.Reporter) (R, error) {
			return runFn(ctx, r)
		}
		if _, err := interactive(out, label, cancel, fn); err != nil {
			if !errors.Is(err, tui.ErrInteractiveRunIncomplete) {
				cmd.SilenceErrors = true
			}
			return fail(err)
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := runFn(cmd.Context(), reporter)
	if err != nil {
		return fail(err)
	}

	if onSuccess != nil {
		onSuccess(result)
	}
	return nil
}

// isTerminalWriter reports whether fn is non-nil and reports w as a real
// terminal -- the nil-guarded-call idiom shared with vm.go and init.go,
// which check the same thing for their own out/in pair before deciding
// whether the accessible (non-interactive) path is required.
func isTerminalWriter(fn func(w io.Writer) bool, w io.Writer) bool {
	return fn != nil && fn(w)
}

// isTerminalReader is isTerminalWriter's read-side counterpart, for the
// isTerminalIn func(io.Reader) bool dependencies (init.go, vm.go).
func isTerminalReader(fn func(r io.Reader) bool, r io.Reader) bool {
	return fn != nil && fn(r)
}
