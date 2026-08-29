package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
)

// listDeps groups list's external dependencies so tests can substitute a
// fake config loader and archive lister instead of touching the real
// filesystem.
type listDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	listArchives func(destination string) ([]backup.Archive, error)
}

func newListCmdWithDeps(deps listDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup archives",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runList(cmd, deps)
		},
	}
}

func runList(cmd *cobra.Command, deps listDeps) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	archives, err := deps.listArchives(cfg.Destination)
	if err != nil {
		return fmt.Errorf("list archives: %w", err)
	}

	out := cmd.OutOrStdout()
	if len(archives) == 0 {
		_, err := fmt.Fprintln(out, "no backup archives found")
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ARCHIVE ID\tVM\tTIMESTAMP\tSIZE\tCOMMENT"); err != nil {
		return err
	}
	for _, a := range archives {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.ArchiveID,
			a.Manifest.VMName,
			a.Manifest.Timestamp.Local().Format(time.RFC3339),
			formatSize(a.Manifest.SizeBytes),
			a.Manifest.Comment,
		)
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

// formatSize renders n bytes as a human-readable IEC size (KiB/MiB/...),
// matching the units Finder/du -h use on macOS rather than SI (KB/MB).
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
