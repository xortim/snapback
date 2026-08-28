package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// runDeps groups run's external dependencies so tests can substitute a
// fake config loader and vm.Controller instead of touching the real
// filesystem or requiring a Fusion install.
type runDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(runDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	})
}

func newRunCmdWithDeps(deps runDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVM(cmd, deps, vmName)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to back up, as configured")
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}

	return cmd
}

func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", configPath, err)
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
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; remove it manually until `snapback cleanup` exists\n", vmName)
		}
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "backup complete: %s\n", result.ArchivePath)
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
