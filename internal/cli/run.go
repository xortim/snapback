package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
)

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(defaultVMCmdDeps())
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

	reporter := progress.NewTerminalReporter(cmd.OutOrStdout())
	result, err := backup.Run(cmd.Context(), ctrl, reporter, opts)
	if err != nil {
		var runErr *backup.RunError
		if errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; run `snapback cleanup --vm %s` to remove it\n", vmName, vmName)
		}
		return err
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "backup complete: %s\n", result.ArchivePath)
	return nil
}

func findVMConfig(vms []config.VM, name string) (config.VM, bool) {
	for _, v := range vms {
		if v.Name == name {
			return v, true
		}
	}
	return config.VM{}, false
}
