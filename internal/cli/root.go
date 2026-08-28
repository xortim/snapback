package cli

import (
	"fmt"
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
	)

	return root
}

// defaultConfigPath returns ~/.config/snapback/config.yaml, falling back to
// a relative path if the home directory can't be determined.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".config", "snapback", "config.yaml")
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactive config bootstrap",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented(cmd)
		},
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup archives",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented(cmd)
		},
	}
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
