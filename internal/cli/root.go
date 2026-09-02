package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "snapback",
		Short: "Zero-downtime backup manager for VMware Fusion VMs",
	}

	root.PersistentFlags().String("config", defaultConfigPath(), "path to config file")

	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
		newCleanupCmd(),
	)

	return root
}

// defaultConfigPath returns ~/.config/snapback/config.yaml, falling back to
// a relative path (with a warning on stderr) if the home directory can't be
// determined.
func defaultConfigPath() string {
	return defaultConfigPathFor(os.Stderr)
}

// defaultConfigPathFor implements defaultConfigPath, taking the warning
// output as a parameter so tests can capture it without touching os.Stderr.
func defaultConfigPathFor(warnOut io.Writer) string {
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(warnOut, "warning: could not determine home directory (%v); using relative config.yaml as the default --config path\n", err)
		return "config.yaml"
	}
	return filepath.Join(home, ".config", "snapback", "config.yaml")
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show backup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented(cmd)
		},
	}
}

func errNotImplemented(cmd *cobra.Command) error {
	return fmt.Errorf("%s: not yet implemented", cmd.Name())
}
