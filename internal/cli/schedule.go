package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
)

// scheduleDeps groups schedule sync's external dependencies so tests can
// substitute a fake config loader and a fake launchd.Installer instead
// of touching the real filesystem or launchctl.
type scheduleDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	newInstaller func() (launchd.Installer, error)
	executable   func() (string, error)
}

func defaultScheduleDeps() scheduleDeps {
	return scheduleDeps{
		loadConfig:   config.Load,
		newInstaller: defaultNewInstaller,
		executable:   os.Executable,
	}
}

// defaultNewInstaller constructs the real, launchctl-backed Installer.
// Shared by scheduleDeps, vmDeps (Task 9), and initDeps (Task 10) so all
// three call sites construct it identically.
func defaultNewInstaller() (launchd.Installer, error) {
	return launchd.NewLaunchctlInstaller()
}

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage launchd scheduling for configured VMs",
	}
	cmd.AddCommand(newScheduleSyncCmdWithDeps(defaultScheduleDeps()))
	return cmd
}

func newScheduleSyncCmdWithDeps(deps scheduleDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile installed launchd schedules with config.yaml",
		Long:  "Installs, updates, or removes each VM's LaunchAgent to match its config.yaml `schedule` field. Safe to re-run any time -- the main use is recovering after `schedule` is hand-edited outside `vm add`/`vm remove`/`init`, which auto-sync on their own.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runScheduleSync(cmd, deps)
		},
	}
	return cmd
}

func runScheduleSync(cmd *cobra.Command, deps scheduleDeps) error {
	cfg, _, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}
	return syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs)
}

// syncSchedules connects to launchd, resolves the running binary's path,
// runs launchd.Sync, and prints the result -- shared by `schedule sync`
// and the auto-sync call sites in `vm add`/`vm remove`/`init` (Tasks
// 9-10) so there's exactly one place that does this, not four.
func syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), vms []config.VM) error {
	installer, err := newInstaller()
	if err != nil {
		return fmt.Errorf("connect to launchd: %w", err)
	}
	binaryPath, err := executable()
	if err != nil {
		return fmt.Errorf("resolve snapback binary path: %w", err)
	}

	result, err := launchd.Sync(installer, vms, binaryPath)
	if err != nil {
		return fmt.Errorf("sync launchd schedules: %w", err)
	}
	return printSyncResult(cmd.OutOrStdout(), result)
}

func printSyncResult(out io.Writer, result launchd.SyncResult) error {
	if result.IsEmpty() {
		_, err := fmt.Fprintln(out, "nothing to do")
		return err
	}
	for _, name := range result.Installed {
		if _, err := fmt.Fprintf(out, "installed: %s\n", name); err != nil {
			return err
		}
	}
	for _, name := range result.Updated {
		if _, err := fmt.Fprintf(out, "updated: %s\n", name); err != nil {
			return err
		}
	}
	// Removed holds raw launchd labels, not VM names (by then the VM is
	// gone from config, so there's no name left to report) -- strip the
	// reverse-DNS prefix so this reads like the two lines above it, and
	// doesn't restate `vm remove foo`'s own message in a different
	// vocabulary.
	for _, label := range result.Removed {
		if _, err := fmt.Fprintf(out, "removed: %s\n", launchd.ShortLabel(label)); err != nil {
			return err
		}
	}
	return nil
}
