package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

// cleanupDeps groups cleanup's external dependencies so tests can
// substitute a fake config loader and vm.Controller instead of touching
// the real filesystem or requiring a Fusion install.
type cleanupDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

func newCleanupCmd() *cobra.Command {
	return newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	})
}

func newCleanupCmdWithDeps(deps cleanupDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove orphaned snapback snapshots",
		Long:  "Find and remove any snapshot on the named VM matching the snapback-<timestamp> naming pattern -- leftovers from a run that died between the snapshot and delete-snapshot choreography steps.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runCleanup itself
			// returns -- flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runCleanup(cmd, deps, vmName)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "name of the VM to clean up, as configured")
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}

	return cmd
}

func runCleanup(cmd *cobra.Command, deps cleanupDeps, vmName string) error {
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

	snapshots, err := ctrl.ListSnapshots(vmCfg.VMX)
	if err != nil {
		return fmt.Errorf("list snapshots for %q: %w", vmName, err)
	}

	out := cmd.OutOrStdout()
	var deleteErrs []error
	found := false
	for _, name := range snapshots {
		if !strings.HasPrefix(name, backup.SnapshotPrefix) {
			continue
		}
		found = true
		if err := ctrl.DeleteSnapshot(vmCfg.VMX, name); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("remove orphaned snapshot %q: %w", name, err))
			continue
		}
		if _, err := fmt.Fprintf(out, "removed orphaned snapshot %q from %q\n", name, vmName); err != nil {
			return err
		}
	}

	if len(deleteErrs) > 0 {
		return fmt.Errorf("cleanup %q: %w", vmName, errors.Join(deleteErrs...))
	}

	if !found {
		_, err := fmt.Fprintf(out, "no orphaned snapshots found for %q\n", vmName)
		return err
	}

	return nil
}
