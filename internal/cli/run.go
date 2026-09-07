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
type runDeps struct {
	loadConfig     func(path string) (*config.Config, error)
	newController  func() (vm.Controller, error)
	isTerminal     func(w io.Writer) bool
	runInteractive func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error)
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

func defaultRunDeps() runDeps {
	base := defaultVMCmdDeps()
	return runDeps{
		loadConfig:    base.loadConfig,
		newController: base.newController,
		isTerminal:    defaultIsTerminal,
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return tui.RunInteractive(out, vmName, cancel, backupFn)
		},
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
		return err
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	opts := backup.Options{
		VMName:      vmCfg.Name,
		VMXPath:     vmCfg.VMX,
		Comment:     vmCfg.CommentTemplate,
		Destination: cfg.Destination,
		Compression: cfg.Compression,
	}

	out := cmd.OutOrStdout()

	if deps.isTerminal != nil && deps.isTerminal(out) && deps.runInteractive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		backupFn := func(r progress.Reporter) (*backup.Result, error) {
			return backup.Run(ctx, ctrl, r, opts)
		}
		_, err := deps.runInteractive(out, vmName, cancel, backupFn)
		if err != nil {
			// The TUI already rendered its own "error: <err>" line before
			// returning here -- except for tui.ErrInteractiveRunIncomplete,
			// where the program exited before ever rendering that line.
			// Silence cobra's own default error print in the normal case
			// (without this, a failed interactive run shows the same error
			// twice), but leave it enabled for the incomplete case so the
			// user sees *some* message instead of a silent exit 1. This
			// doesn't affect the exit code: cmd/snapback/main.go only
			// checks whether err != nil.
			if !errors.Is(err, tui.ErrInteractiveRunIncomplete) {
				cmd.SilenceErrors = true
			}
			warnIfMaybeOrphaned(cmd, vmName, err)
			return err
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := backup.Run(cmd.Context(), ctrl, reporter, opts)
	if err != nil {
		warnIfMaybeOrphaned(cmd, vmName, err)
		return err
	}

	_, _ = fmt.Fprintf(out, "backup complete: %s\n", result.ArchivePath)
	return nil
}

// warnIfMaybeOrphaned prints a pointer to `snapback cleanup` on stderr
// if err is a *backup.RunError tagged at Stage: Snapshotting or later --
// see backup.Run's doc comment for why that Stage range specifically
// means a snapshot may have been left behind on the source VM.
func warnIfMaybeOrphaned(cmd *cobra.Command, vmName string, err error) {
	var runErr *backup.RunError
	if errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; run `snapback cleanup --vm %s` to remove it\n", vmName, vmName)
	}
}

func findVMConfig(vms []config.VM, name string) (config.VM, bool) {
	for _, v := range vms {
		if v.Name == name {
			return v, true
		}
	}
	return config.VM{}, false
}
