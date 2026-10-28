package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
	"github.com/xortim/snapback/internal/notify"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
	"github.com/xortim/snapback/internal/vm"
)

// runDeps groups run's external dependencies. It shares loadConfig and
// newController's shape with cleanupDeps/vmCmdDeps (deps.go) but is its
// own struct, not an alias -- run also needs isTerminal (decides
// interactive vs. plain rendering) and runInteractive (the interactive
// renderer itself), neither of which cleanup has any use for. A nil
// isTerminal is treated as "not a terminal": every existing test's
// runDeps{...} literal leaves it unset, and every existing test's
// stdout is a *bytes.Buffer (never a real terminal) anyway, so this
// keeps all of those tests exercising the plain-output path unchanged.
// A nil newNotifier means never notify, so existing runDeps{...} test
// literals stay valid.
type runDeps struct {
	loadConfig     func(path string) (*config.Config, error)
	newController  func() (vm.Controller, error)
	isTerminal     func(w io.Writer) bool
	runInteractive func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error)
	newNotifier    func() notify.Notifier
}

// defaultIsTerminal reports whether w is a real terminal. Only *os.File
// can be a terminal; any other io.Writer (a *bytes.Buffer in tests, a
// pipe, a redirected-to-file launchd invocation) is not.
func defaultIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// defaultIsTerminalIn is defaultIsTerminal's read-side counterpart: only
// *os.File can be a terminal, so any other io.Reader (a *bytes.Buffer or
// strings.Reader in tests, a pipe, redirected-from-file input) is not.
// internal/cli/init.go uses this alongside defaultIsTerminal -- a real
// bubbletea program needs both a real terminal stdout to render into and
// a real terminal stdin to read raw keypresses from; either one being
// something else (e.g. `snapback init < answers.txt` run at an actual
// terminal, where stdout is a tty but stdin is a redirected file) means
// the rich interactive path can't work.
func defaultIsTerminalIn(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func defaultRunDeps() runDeps {
	base := defaultVMCmdDeps()
	return runDeps{
		loadConfig:    base.loadConfig,
		newController: base.newController,
		isTerminal:    defaultIsTerminal,
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return tui.RunInteractive(out, vmName, cancel, backupFn)
		},
		newNotifier: func() notify.Notifier { return notify.NewOSAScript() },
	}
}

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(defaultRunDeps())
}

func newRunCmdWithDeps(deps runDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runVM itself returns --
			// flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runVM(cmd, deps, vmName)
		},
	}
	addRequiredVMFlag(cmd, &vmName, "name of the VM to back up, as configured")

	return cmd
}

func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		// No config means no way to know whether notifications are
		// enabled -- see the run-notifications spec's "covered failure
		// points".
		return err
	}
	result, err := runVMWithConfig(cmd, deps, cfg, configPath, vmName)
	notifyRunOutcome(cmd, deps, cfg, vmName, result, err)
	return err
}

// runVMWithConfig is everything runVM does after config has loaded. It
// returns the backup.Result (nil on failure, or if the run never
// started) so runVM can notify on every return path from one place --
// an early return added here later can't silently skip notification.
func runVMWithConfig(cmd *cobra.Command, deps runDeps, cfg *config.Config, configPath, vmName string) (*backup.Result, error) {
	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return nil, fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	if logPath, err := launchd.LogPath(vmCfg.Name); err == nil {
		if err := launchd.RotateIfOversized(logPath); err != nil {
			// Rotation failing is never a reason to skip the actual
			// backup -- warn and continue, same posture warnIfMaybeOrphaned
			// already takes for a non-fatal, best-effort side channel.
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not rotate log %s: %v\n", logPath, err)
		}
	}

	ctrl, err := deps.newController()
	if err != nil {
		return nil, fmt.Errorf("connect to VM controller: %w", err)
	}

	opts := backup.Options{
		VMName:      vmCfg.Name,
		VMXPath:     vmCfg.VMX,
		Destination: cfg.Destination,
		Compression: cfg.Compression,
	}

	out := cmd.OutOrStdout()

	// The TUI already rendered its own "error: <err>" line by the time
	// interactive returns -- except for tui.ErrInteractiveRunIncomplete,
	// where the program exited before ever rendering that line. dispatchRun
	// silences cobra's own default error print in the normal case (without
	// this, a failed interactive run shows the same error twice), but
	// leaves it enabled for the incomplete case so the user sees *some*
	// message instead of a silent exit 1. This doesn't affect the exit
	// code: cmd/snapback/main.go only checks whether err != nil.
	var result *backup.Result
	err = dispatchRun(cmd, out, deps.isTerminal, deps.runInteractive, vmName,
		func(ctx context.Context, r progress.Reporter) (*backup.Result, error) {
			res, err := backup.Run(ctx, ctrl, r, opts)
			result = res
			return res, err
		},
		func(err error) { warnIfMaybeOrphaned(cmd, vmName, err) },
		func(res *backup.Result) {
			_, _ = fmt.Fprintf(out, "backup complete: %s\n", res.ArchivePath)
		},
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// notifyRunOutcome sends a macOS notification for a finished run when
// notifications are enabled and nobody is watching (stdout isn't a
// terminal -- e.g. a launchd-scheduled run). Failures always notify;
// successes only when on_success is set. A send failure is a stderr
// warning only: it never changes run's outcome. The notification gets a
// context detached from the command's cancellation so a SIGINT'd run
// still reports its failure. See
// docs/superpowers/specs/2026-10-26-run-notifications-design.md.
func notifyRunOutcome(cmd *cobra.Command, deps runDeps, cfg *config.Config, vmName string, result *backup.Result, runErr error) {
	if deps.newNotifier == nil || !cfg.Notifications.Enabled || isTerminalWriter(deps.isTerminal, cmd.OutOrStdout()) {
		return
	}
	var n notify.Notification
	switch {
	case runErr != nil:
		n = notify.Failure(vmName, runErr, snapshotMayRemain(runErr))
	case !cfg.Notifications.OnSuccess:
		return
	default:
		archiveID := ""
		if result != nil {
			archiveID = result.ArchiveID
		}
		n = notify.Success(vmName, archiveID)
	}
	if err := deps.newNotifier().Notify(context.WithoutCancel(cmd.Context()), n); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not send notification: %v\n", err)
	}
}

// warnIfMaybeOrphaned prints a pointer to `snapback cleanup` on stderr
// if snapshotMayRemain(err).
func warnIfMaybeOrphaned(cmd *cobra.Command, vmName string, err error) {
	if snapshotMayRemain(err) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; run `snapback cleanup --vm %s` to remove it\n", vmName, vmName)
	}
}

// snapshotMayRemain reports whether err is a *backup.RunError tagged at
// Stage: Snapshotting or later -- see backup.Run's doc comment for why
// that Stage range means a snapshot may have been left behind. Shared by
// warnIfMaybeOrphaned and notifyRunOutcome so the stderr warning and the
// notification's cleanup hint can never disagree.
func snapshotMayRemain(err error) bool {
	var runErr *backup.RunError
	return errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting
}

func findVMConfig(vms []config.VM, name string) (config.VM, bool) {
	for _, v := range vms {
		if v.Name == name {
			return v, true
		}
	}
	return config.VM{}, false
}
