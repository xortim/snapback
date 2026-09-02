package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
)

func newCleanupCmd() *cobra.Command {
	return newCleanupCmdWithDeps(defaultVMCmdDeps())
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
	addRequiredVMFlag(cmd, &vmName, "name of the VM to clean up, as configured")

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

	var matching []string
	for _, name := range snapshots {
		if strings.HasPrefix(name, backup.SnapshotPrefix) {
			matching = append(matching, name)
		}
	}

	out := cmd.OutOrStdout()
	if len(matching) == 0 {
		_, err := fmt.Fprintf(out, "no orphaned snapshots found for %q\n", vmName)
		return err
	}

	// Only lock once there's actually something to delete: a `run` for
	// this VM holds this same lock for its whole choreography (see
	// backup.AcquireLock's doc comment), so a snapshot can never be
	// deleted out from under it. If the lock is held, defer to whoever
	// holds it rather than fail -- this isn't a cleanup error, just
	// something to retry later.
	//
	// This still leaves a narrow, benign gap: `matching` above was
	// computed from ListSnapshots before this lock was acquired, so a
	// `run` can finish and release the lock in between. If it does, one
	// of the names in `matching` may already be gone by the time
	// DeleteSnapshots runs below, and DeleteSnapshots reports that as a
	// "not found" error for that name rather than success. That's not
	// data loss -- it's the `run` that raced this cleanup having already
	// removed the snapshot itself -- but it does surface here as a
	// nonzero exit rather than a silent no-op. The lock still fully
	// prevents cleanup from ever deleting a snapshot a live `run` owns;
	// this only affects how a successful race is reported.
	lock, err := backup.AcquireLock(cfg.Destination, vmCfg.Name)
	if err != nil {
		if errors.Is(err, backup.ErrLocked) {
			_, ferr := fmt.Fprintf(out, "backup for %q may be in progress (lock held); skipping cleanup\n", vmName)
			return ferr
		}
		return fmt.Errorf("acquire cleanup lock for %q: %w", vmName, err)
	}
	defer func() { _ = lock.Release() }()

	deleted, delErr := ctrl.DeleteSnapshots(vmCfg.VMX, matching)
	for _, name := range deleted {
		if _, err := fmt.Fprintf(out, "removed orphaned snapshot %q from %q\n", name, vmName); err != nil {
			return err
		}
	}
	if delErr != nil {
		return fmt.Errorf("cleanup %q: %w", vmName, delErr)
	}

	return nil
}
