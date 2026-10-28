package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

// pruneDeps groups prune's swappable dependencies: the config loader,
// backup.Prune itself (so tests can inject a held lock or a removal
// error), and the clock.
type pruneDeps struct {
	loadConfig func(path string) (*config.Config, error)
	prune      func(ctx context.Context, destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (backup.PruneResult, error)
	now        func() time.Time
}

func defaultPruneDeps() pruneDeps {
	return pruneDeps{loadConfig: config.Load, prune: backup.Prune, now: time.Now}
}

func newPruneCmd() *cobra.Command {
	return newPruneCmdWithDeps(defaultPruneDeps())
}

func newPruneCmdWithDeps(deps pruneDeps) *cobra.Command {
	var vmName string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove backup archives beyond the retention policy",
		Long:  "Apply the config's retention policy (keep_last, keep_daily, keep_weekly) to each configured VM's archives, or just --vm's. `run` already does this after every successful backup; this is for applying a changed policy or checking it with --dry-run. Archives of VMs no longer in the config are never touched.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runPrune(cmd, deps, vmName, dryRun)
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "prune only this configured VM (default: every configured VM)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without deleting anything")
	return cmd
}

func runPrune(cmd *cobra.Command, deps pruneDeps, vmName string, dryRun bool) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vms := cfg.VMs
	if vmName != "" {
		v, ok := findVMConfig(cfg.VMs, vmName)
		if !ok {
			return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
		}
		vms = []config.VM{v}
	}

	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	policy := retentionPolicy(cfg.Retention)
	now := deps.now()
	failed := 0
	for _, v := range vms {
		res, err := deps.prune(cmd.Context(), cfg.Destination, v.Name, policy, now, dryRun)
		if errors.Is(err, context.Canceled) {
			// Stop here rather than moving on to the next VM: the
			// operator asked to stop.
			_, _ = fmt.Fprintf(errOut, "prune %q interrupted after removing %d archive(s); the rest were left in place\n", v.Name, removedCount(res))
			return err
		}
		if errors.Is(err, backup.ErrLocked) {
			_, _ = fmt.Fprintf(out, "backup for %q may be in progress (lock held); skipping\n", v.Name)
			continue
		}
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "error: prune %q: %v\n", v.Name, err)
			failed++
			continue
		}
		printPruneResult(out, v.Name, res, dryRun)
		for _, e := range res.Errs {
			_, _ = fmt.Fprintf(errOut, "error: %v\n", e)
		}
		if len(res.Errs) > 0 {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("prune failed for %d VM(s)", failed)
	}
	return nil
}

func printPruneResult(w io.Writer, vmName string, res backup.PruneResult, dryRun bool) {
	if len(res.Kept)+len(res.Removed) == 0 {
		_, _ = fmt.Fprintf(w, "%q: no archives\n", vmName)
		return
	}
	removeVerb := "remove"
	if dryRun {
		removeVerb = "would remove"
	}
	_, _ = fmt.Fprintf(w, "%q:\n", vmName)
	// Same header-bolding table renderer list uses (table.go).
	_ = renderTabwriterTable(w, func(tw *tabwriter.Writer) error {
		_, _ = fmt.Fprintln(tw, "ARCHIVE\tTIMESTAMP\tACTION")
		for _, a := range res.Kept {
			_, _ = fmt.Fprintf(tw, "%s\t%s\tkeep (%s)\n", a.ArchiveID, a.Manifest.Timestamp.Local().Format(time.DateTime), strings.Join(a.Reasons, ", "))
		}
		for _, a := range res.Removed {
			action := removeVerb
			switch {
			case errors.Is(a.Err, backup.ErrSymlinkedArchive):
				action = "remove failed (symlink, see below)"
			case a.Err != nil:
				action = "remove failed"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", a.ArchiveID, a.Manifest.Timestamp.Local().Format(time.DateTime), action)
		}
		return nil
	})
	failed := len(res.Removed) - removedCount(res)
	summaryVerb := "removed"
	if dryRun {
		summaryVerb = "would remove"
	}
	line := fmt.Sprintf("kept %d, %s %d", len(res.Kept), summaryVerb, len(res.Removed)-failed)
	if failed > 0 {
		line += fmt.Sprintf(", failed %d", failed)
	}
	_, _ = fmt.Fprintln(w, line)
}

// removedCount is how many of res.Removed were actually removed (or, under
// dry run, would be).
func removedCount(res backup.PruneResult) int {
	n := 0
	for _, a := range res.Removed {
		if a.Err == nil {
			n++
		}
	}
	return n
}
