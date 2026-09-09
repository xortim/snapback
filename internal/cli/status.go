package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

// statusDeps groups status's external dependencies so tests can
// substitute a fake config loader, archive lister, VM scanner, and
// vm.Controller factory instead of touching the real filesystem.
type statusDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	listArchives  func(destination string) ([]backup.Archive, error)
	searchDirs    func() []string
	discoverVMs   func(searchDirs []string) ([]discoveredVM, error)
	newController func() (vm.Controller, error)
}

func newStatusCmd() *cobra.Command {
	base := defaultVMCmdDeps()
	return newStatusCmdWithDeps(statusDeps{
		loadConfig:    config.Load,
		listArchives:  backup.ListArchives,
		searchDirs:    defaultVMSearchDirs,
		discoverVMs:   discoverVMs,
		newController: base.newController,
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
		if err := runStatusForVM(cmd, vmCfg, cfg.Retention, archives); err != nil {
			return err
		}
		return warnDamagedDiskChains(cmd, deps, []config.VM{vmCfg})
	}

	if err := warnUndiscoveredVMs(cmd, deps, cfg.VMs, configPath); err != nil {
		return err
	}
	// Print the summary table before running the disk-consistency checks
	// below, so a slow or hung vmware-vdiskmanager call doesn't leave the
	// user staring at zero output -- warnDamagedDiskChains returns
	// promptly on ctx cancellation regardless (see its doc comment), but
	// an individual check already in flight when that happens can still
	// take a while to unblock.
	if err := runStatusSummary(cmd, cfg.VMs, archives); err != nil {
		return err
	}
	return warnDamagedDiskChains(cmd, deps, cfg.VMs)
}

// diskChainCheckResult is one VM's outcome from warnDamagedDiskChains'
// worker goroutines: idx preserves vms' original order for output (the
// workers complete in arbitrary order), and msg is the full line to print
// to stderr, or "" if the VM's chain needed no comment.
type diskChainCheckResult struct {
	idx int
	msg string
}

// checkOneVMDiskChain runs vmCfg's tools-state and disk-consistency checks
// and renders the result as a ready-to-print message (or "" if there's
// nothing to report) rather than writing to stderr directly, so
// warnDamagedDiskChains' worker goroutines can run concurrently without
// needing to synchronize interleaved writes.
func checkOneVMDiskChain(ctrl vm.Controller, vmCfg config.VM) diskChainCheckResult {
	toolsState, err := ctrl.CheckToolsState(vmCfg.VMX)
	if err != nil {
		return diskChainCheckResult{msg: fmt.Sprintf("note: could not check disk consistency for %q: %v\n", vmCfg.Name, err)}
	}
	// ToolsRunning is read as "VM is powered on" -- the same proxy
	// Run uses for its own pre/post-merge checks (internal/backup's
	// checkDisksConsistent doc comment), since vm.Controller has no
	// direct power-state query. A VM that's running but has no
	// VMware Tools installed (a normal, supported state) reports
	// ToolsNotInstalled/ToolsUnknown here too, so it falls through to
	// the check below against disk files a live vmware-vmx process
	// still holds open -- see the warning text for how that's hedged.
	if toolsState == vm.ToolsRunning {
		return diskChainCheckResult{}
	}
	if err := backup.CheckVMDiskConsistency(ctrl, vmCfg.VMX); err != nil {
		return diskChainCheckResult{msg: fmt.Sprintf(
			"warning: %q's disk chain may need repair: %v -- if %q is actually running without VMware Tools installed, this check can fail on lock contention against its open disk files rather than a real verdict (false positive); otherwise, run \"vmware-vdiskmanager -R <disk>.vmdk\" or repair it from Fusion\n",
			vmCfg.Name, err, vmCfg.Name)}
	}
	return diskChainCheckResult{}
}

// warnDamagedDiskChains checks every VM in vms' disk chain via
// checkOneVMDiskChain, printing a loud warning: line to stderr for any VM
// whose chain needs repair -- catching the incident recorded in
// CLAUDE.md, where a merge vmcli reported as successful didn't actually
// apply on disk and nothing surfaced it short of trying to power the VM
// on. Skipped for a running VM; see checkOneVMDiskChain's doc comment for
// why and its caveat. A newController failure is reported the same
// non-fatal way warnUndiscoveredVMs reports a scan failure, rather than
// aborting status's core job of reporting backup state.
//
// Each VM's check runs in its own goroutine -- CheckToolsState and
// CheckDiskConsistency are independent per VM and each is a blocking,
// subprocess-backed call (vmcli / vmware-vdiskmanager) with no per-call
// timeout and no way to cancel one already in flight (vm.Controller's
// methods take no context -- see ADR-003,
// docs/superpowers/specs/2026-08-27-run-progress-context-design.md), so
// running them one VM at a time would turn a dozen-VM config into a
// serial chain of shell-outs. This function itself still responds to ctx
// cancellation promptly between collecting results, even if a handful of
// its workers are left running in the background past that point --
// same accepted leak discoverVMsWithContext takes, for the same reason
// (the process is exiting right after).
func warnDamagedDiskChains(cmd *cobra.Command, deps statusDeps, vms []config.VM) error {
	ctrl, err := deps.newController()
	if err != nil {
		_, ferr := fmt.Fprintf(cmd.ErrOrStderr(), "note: could not check disk consistency: %v\n", err)
		return ferr
	}

	results := make(chan diskChainCheckResult, len(vms))
	for i, vmCfg := range vms {
		go func(i int, vmCfg config.VM) {
			r := checkOneVMDiskChain(ctrl, vmCfg)
			r.idx = i
			results <- r
		}(i, vmCfg)
	}

	messages := make([]string, len(vms))
	ctx := cmd.Context()
	for range vms {
		select {
		case <-ctx.Done():
			return fmt.Errorf("status cancelled: %w", ctx.Err())
		case r := <-results:
			messages[r.idx] = r.msg
		}
	}

	for _, msg := range messages {
		if msg == "" {
			continue
		}
		if _, err := fmt.Fprint(cmd.ErrOrStderr(), msg); err != nil {
			return err
		}
	}
	return nil
}

// warnUndiscoveredVMs cross-references VM discovery against cfg's
// configured VMs and prints a plain informational note to stderr for
// each discovered VM not present in the config -- catching the case
// where a VM was created (or skipped during init) and never added to
// the backup set. Not a warning/error: a VM excluded on purpose (a
// scratch VM) is a legitimate state. A discovery failure is reported
// the same way rather than aborting status's core job of reporting
// backup state.
func warnUndiscoveredVMs(cmd *cobra.Command, deps statusDeps, vms []config.VM, configPath string) error {
	candidates, err := discoverVMsWithContext(cmd.Context(), deps.discoverVMs, deps.searchDirs())
	if err != nil {
		_, ferr := fmt.Fprintf(cmd.ErrOrStderr(), "note: could not scan for VMs: %v\n", err)
		return ferr
	}

	configured := make(map[string]bool, len(vms))
	for _, vmCfg := range vms {
		configured[vmCfg.Name] = true
	}

	for _, c := range candidates {
		if configured[c.Name] {
			continue
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "note: discovered VM %q is not in config %s\n", c.Name, configPath); err != nil {
			return err
		}
	}
	return nil
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

// totalArchiveSize sums SizeBytes across archives.
func totalArchiveSize(archives []backup.Archive) int64 {
	var total int64
	for _, a := range archives {
		total += a.Manifest.SizeBytes
	}
	return total
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
			size = formatSize(totalArchiveSize(vmArchives))
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

	if _, err := fmt.Fprintf(out, "total size: %s\n", formatSize(totalArchiveSize(vmArchives))); err != nil {
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
