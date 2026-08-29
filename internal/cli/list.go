package cli

import (
	"fmt"
	"math"
	"strings"
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

func newListCmd() *cobra.Command {
	return newListCmdWithDeps(listDeps{
		loadConfig:   config.Load,
		listArchives: backup.ListArchives,
	})
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
			sanitizeForTable(a.Manifest.Comment),
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
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024.0
	const units = "KMGTPE"
	value := float64(n)
	exp := -1
	for value >= unit && exp < len(units)-1 {
		value /= unit
		exp++
	}
	// %.1f rounds to one decimal place, which can round a value just under
	// unit (e.g. 1023.95) up to "1024.0" -- display that as 1.0 of the next
	// unit instead of 1024.0 of this one.
	if rounded := math.Round(value*10) / 10; rounded >= unit && exp < len(units)-1 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, units[exp])
}

// sanitizeForTable strips characters that would confuse tabwriter's
// column (tab) and row (newline) delimiters out of free-form text -- e.g.
// Manifest.Comment, which comes from a user-configured comment_template
// and isn't otherwise constrained.
func sanitizeForTable(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}
