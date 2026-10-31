package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xortim/snapback/internal/version"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "snapback",
		Short:   "Zero-downtime backup manager for VMware Fusion VMs",
		Version: version.Get().String(),
	}
	// Cobra's default is "snapback version <v>"; the Info string already
	// reads as a version, so drop the redundant word.
	root.SetVersionTemplate("snapback {{.Version}}\n")

	// Default left empty rather than calling defaultConfigPath() here: that
	// would resolve (and warn on) the home directory at flag-registration
	// time, on every invocation, even ones that never consume it (--help,
	// completion, or explicit --config). configPathForCmd resolves the
	// default lazily, only when a command actually needs it. See #28.
	root.PersistentFlags().String("config", "", "path to config file (default \"~/.config/snapback/config.yaml\")")

	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
		newCleanupCmd(),
		newPruneCmd(),
		newVMCmd(),
		newRestoreCmd(),
		newScheduleCmd(),
		newNotifyCmd(),
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
