package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "snapback",
		Short: "Zero-downtime backup manager for VMware Fusion VMs",
	}

	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
	)

	return root
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

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
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
