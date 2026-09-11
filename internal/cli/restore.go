package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
	"github.com/xortim/snapback/internal/vm"
)

// restoreDeps groups restore's external dependencies -- shares
// loadConfig/newController's shape with runDeps but is its own struct
// (mirrors run.go's own reasoning for not reusing vmCmdDeps): restore
// needs isTerminal/restoreInteractive, not run's isTerminal/runInteractive.
type restoreDeps struct {
	loadConfig         func(path string) (*config.Config, error)
	newController      func() (vm.Controller, error)
	isTerminal         func(w io.Writer) bool
	restoreInteractive func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error)
}

func defaultRestoreDeps() restoreDeps {
	base := defaultVMCmdDeps()
	return restoreDeps{
		loadConfig:    base.loadConfig,
		newController: base.newController,
		isTerminal:    defaultIsTerminal,
		restoreInteractive: func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error) {
			return tui.RestoreInteractive(out, label, cancel, restoreFn)
		},
	}
}

func newRestoreCmd() *cobra.Command {
	return newRestoreCmdWithDeps(defaultRestoreDeps())
}

func newRestoreCmdWithDeps(deps restoreDeps) *cobra.Command {
	var vmName, dest string
	var latest bool

	cmd := &cobra.Command{
		Use:   "restore [archive-id]",
		Short: "Restore a backup archive",
		Long:  "Verify, extract, and place a backup archive as a new, non-destructively-named .vmwarevm bundle next to the source -- never overwriting anything. Either an archive-id positional argument or --vm with --latest must be given, not both.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag/arg validation this RunE performs itself returns errors
			// that should still print usage -- cmd.SilenceUsage is set only
			// after validateRestoreSelectors passes, mirroring run.go's
			// split between flag-misuse errors and operational errors.
			var archiveID string
			if len(args) == 1 {
				archiveID = args[0]
			}
			if err := validateRestoreSelectors(archiveID, vmName, latest); err != nil {
				return err
			}
			cmd.SilenceUsage = true
			return restoreArchive(cmd, deps, archiveID, vmName, latest, dest)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to restore the latest archive for, as configured")
	cmd.Flags().BoolVar(&latest, "latest", false, "restore the newest archive for --vm")
	cmd.Flags().StringVar(&dest, "dest", "", "parent directory to place the restored bundle in (required if the archive's VM isn't in the current config)")

	return cmd
}

// validateRestoreSelectors enforces "exactly one of archive-id or --vm
// with --latest" per ADR-004.
func validateRestoreSelectors(archiveID, vmName string, latest bool) error {
	haveArchiveID := archiveID != ""
	haveVMLatest := vmName != "" && latest
	switch {
	case haveArchiveID && (vmName != "" || latest):
		return fmt.Errorf("archive-id and --vm/--latest are mutually exclusive")
	case !haveArchiveID && vmName != "" && !latest:
		return fmt.Errorf("--vm requires --latest")
	case !haveArchiveID && latest && vmName == "":
		return fmt.Errorf("--latest requires --vm")
	case !haveArchiveID && !haveVMLatest:
		return fmt.Errorf("exactly one of an archive-id argument or --vm with --latest is required")
	}
	return nil
}

func restoreArchive(cmd *cobra.Command, deps restoreDeps, archiveID, vmName string, latest bool, dest string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	resolvedArchiveID := archiveID
	label := archiveID
	if vmName != "" && latest {
		archive, err := backup.LatestArchiveForVM(cfg.Destination, vmName)
		if err != nil {
			return fmt.Errorf("resolve latest archive for %q: %w", vmName, err)
		}
		resolvedArchiveID = archive.ArchiveID
		label = vmName
	}

	opts := backup.RestoreOptions{
		ArchiveID:   resolvedArchiveID,
		Destination: cfg.Destination,
	}

	if dest != "" {
		opts.TargetDir = dest
	} else {
		lookupName := vmName
		if lookupName == "" {
			archive, err := backup.FindArchive(cfg.Destination, resolvedArchiveID)
			if err != nil {
				return fmt.Errorf("resolve archive %q: %w", resolvedArchiveID, err)
			}
			lookupName = archive.Manifest.VMName
		}
		vmCfg, ok := findVMConfig(cfg.VMs, lookupName)
		if !ok {
			return fmt.Errorf("VM %q not found in config %s -- pass --dest to restore without it", lookupName, configPath)
		}
		opts.VMXPath = vmCfg.VMX
	}

	out := cmd.OutOrStdout()

	if deps.isTerminal != nil && deps.isTerminal(out) && deps.restoreInteractive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		restoreFn := func(r progress.Reporter) (*backup.RestoreResult, error) {
			return backup.Restore(ctx, ctrl, r, opts)
		}
		_, err := deps.restoreInteractive(out, label, cancel, restoreFn)
		if err != nil {
			if !errors.Is(err, tui.ErrInteractiveRunIncomplete) {
				cmd.SilenceErrors = true
			}
			return err
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := backup.Restore(cmd.Context(), ctrl, reporter, opts)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "restore complete: %s\n", result.TargetPath)
	return nil
}
