// Package cli (internal test package) so this file can call NewRootCmd
// and manipulate its subcommands directly.
package cli

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

// errBoom is a shared sentinel error for tests across this package that
// need to inject a fake dependency failure (config load, archive listing,
// controller connect, etc.) and assert it's wrapped/propagated correctly.
var errBoom = errors.New("boom")

// swapSubcommand builds the real root command via NewRootCmd() -- so the
// --config persistent flag and everything else the real command wiring
// depends on can't drift from root.go -- then replaces the subcommand
// named name with replacement (typically built from a *WithDeps
// constructor so a test can inject fakes).
func swapSubcommand(t *testing.T, name string, replacement *cobra.Command) *cobra.Command {
	t.Helper()
	root := NewRootCmd()
	removed := false
	for _, sub := range root.Commands() {
		if sub.Name() == name {
			root.RemoveCommand(sub)
			removed = true
			break
		}
	}
	if !removed {
		t.Fatalf("swapSubcommand: NewRootCmd() has no %q subcommand to replace", name)
	}
	root.AddCommand(replacement)
	return root
}
