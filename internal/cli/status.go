package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
)

// statusDeps groups status's external dependencies so tests can
// substitute a fake config loader and archive lister instead of touching
// the real filesystem.
type statusDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	listArchives func(destination string) ([]backup.Archive, error)
}

func newStatusCmd() *cobra.Command {
	return newStatusCmdWithDeps(statusDeps{
		loadConfig:   config.Load,
		listArchives: backup.ListArchives,
	})
}

func newStatusCmdWithDeps(deps statusDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show backup status",
		Long:  "Show one row per configured VM (last backup, total size, backup count), or with --vm, that VM's full archive history and retention policy. Scheduling info and --xbar output are not yet implemented.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runStatus(cmd, deps, vmName)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "show full archive history and retention policy for one VM, as configured")

	return cmd
}

func runStatus(cmd *cobra.Command, deps statusDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	var vmCfg config.VM
	if vmName != "" {
		var ok bool
		vmCfg, ok = findVMConfig(cfg.VMs, vmName)
		if !ok {
			return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
		}
	}

	archives, err := deps.listArchives(cfg.Destination)
	if err != nil {
		return fmt.Errorf("list archives: %w", err)
	}

	if vmName != "" {
		return runStatusForVM(cmd, vmCfg, cfg.Retention, archives)
	}
	return runStatusSummary(cmd, cfg.VMs, archives)
}

// archivesForVM returns the subset of archives belonging to vmName,
// preserving archives' relative order (ListArchives returns newest
// first, and both status views rely on that order for "last backup").
func archivesForVM(archives []backup.Archive, vmName string) []backup.Archive {
	var out []backup.Archive
	for _, a := range archives {
		if a.Manifest.VMName == vmName {
			out = append(out, a)
		}
	}
	return out
}

// runStatusSummary prints one row per configured VM: last backup
// timestamp, total size across all its archives, and archive count. A VM
// with no archives yet gets an explicit "no backups yet" row instead of
// a blank/zero one, so a newly configured VM reads as "needs a first
// backup" rather than looking like a rendering bug.
func runStatusSummary(cmd *cobra.Command, vms []config.VM, archives []backup.Archive) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "VM\tLAST BACKUP\tTOTAL SIZE\tBACKUPS"); err != nil {
		return err
	}
	for _, vmCfg := range vms {
		vmArchives := archivesForVM(archives, vmCfg.Name)

		lastBackup := "no backups yet"
		size := "-"
		if len(vmArchives) > 0 {
			lastBackup = vmArchives[0].Manifest.Timestamp.Local().Format(time.RFC3339)
			var totalSize int64
			for _, a := range vmArchives {
				totalSize += a.Manifest.SizeBytes
			}
			size = formatSize(totalSize)
		}

		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
			sanitizeForTable(vmCfg.Name), lastBackup, size, len(vmArchives))
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

// runStatusForVM prints one VM's retention policy followed by its full
// archive history (unlike the summary table, this includes each
// archive's tools_state, since seeing a run of crash-consistent backups
// is exactly the "full consistency detail" this view exists for).
func runStatusForVM(cmd *cobra.Command, vmCfg config.VM, retention config.Retention, archives []backup.Archive) error {
	out := cmd.OutOrStdout()
	vmArchives := archivesForVM(archives, vmCfg.Name)

	if _, err := fmt.Fprintf(out, "retention: keep last %d, keep daily %d, keep weekly %d\n",
		retention.KeepLast, retention.KeepDaily, retention.KeepWeekly); err != nil {
		return err
	}

	if len(vmArchives) == 0 {
		_, err := fmt.Fprintf(out, "no backups yet for %q\n", vmCfg.Name)
		return err
	}

	var totalSize int64
	for _, a := range vmArchives {
		totalSize += a.Manifest.SizeBytes
	}
	if _, err := fmt.Fprintf(out, "total size: %s\n", formatSize(totalSize)); err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ARCHIVE ID\tTIMESTAMP\tSIZE\tTOOLS STATE\tCOMMENT"); err != nil {
		return err
	}
	for _, a := range vmArchives {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			sanitizeForTable(a.ArchiveID),
			a.Manifest.Timestamp.Local().Format(time.RFC3339),
			formatSize(a.Manifest.SizeBytes),
			a.Manifest.ToolsState,
			sanitizeForTable(a.Manifest.Comment),
		)
		if err != nil {
			return err
		}
	}
	return w.Flush()
}
