package cli

import (
	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

// vmCmdDeps groups the external dependencies shared by every subcommand
// that operates on a single named VM (run, cleanup): a config loader and
// a vm.Controller factory, both swappable in tests for a fake instead of
// touching the real filesystem or requiring a Fusion install. run.go and
// cleanup.go each keep their own name for this (runDeps, cleanupDeps) as
// type aliases below -- same shape, so the struct and its default/flag
// plumbing live here once instead of twice.
type vmCmdDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newController func() (vm.Controller, error)
}

// runDeps and cleanupDeps are aliases (not distinct types), so existing
// call sites and tests can keep referring to a command-specific name
// without duplicating vmCmdDeps's fields.
type runDeps = vmCmdDeps
type cleanupDeps = vmCmdDeps

// defaultVMCmdDeps returns the real, production dependencies: config.Load
// and a real vm.VMCLIController.
func defaultVMCmdDeps() vmCmdDeps {
	return vmCmdDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			return vm.NewVMCLIController()
		},
	}
}

// addRequiredVMFlag registers the --vm flag on cmd into *vmName with
// usage as its help text, and marks it required. Shared by every
// single-VM subcommand (run, cleanup) since MarkFlagRequired's own error
// only fires for a typo'd flag name -- a programmer error this package
// wants to catch immediately as a panic, not thread through as a runtime
// error from some future `--vm` invocation.
func addRequiredVMFlag(cmd *cobra.Command, vmName *string, usage string) {
	cmd.Flags().StringVar(vmName, "vm", "", usage)
	if err := cmd.MarkFlagRequired("vm"); err != nil {
		panic(err)
	}
}
