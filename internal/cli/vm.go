package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/tui"
)

// vmDeps groups vm add/remove's external dependencies so tests can
// substitute fakes instead of touching the real filesystem, a real
// terminal, or requiring a Fusion install -- mirrors initDeps's shape,
// since vm add reuses the same discovery + wizard-building-block +
// config-write pipeline init does, just scoped to VMs alone.
type vmDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	marshal      func(cfg *config.Config) ([]byte, error)
	writeFile    func(path string, data []byte) error
	searchDirs   func() []string
	discoverVMs  func(searchDirs []string) ([]discoveredVM, error)
	isTerminal   func(w io.Writer) bool
	isTerminalIn func(r io.Reader) bool
	addVMs       func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate) ([]config.VM, error)
}

func defaultVMDeps() vmDeps {
	return vmDeps{
		loadConfig:   config.Load,
		marshal:      config.Marshal,
		writeFile:    writeConfigFile,
		searchDirs:   defaultVMSearchDirs,
		discoverVMs:  discoverVMs,
		isTerminal:   defaultIsTerminal,
		isTerminalIn: defaultIsTerminalIn,
		addVMs:       tui.AddVMs,
	}
}

func newVMCmd() *cobra.Command {
	deps := defaultVMDeps()
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage the VMs listed in config.yaml",
	}
	cmd.AddCommand(newVMAddCmdWithDeps(deps), newVMRemoveCmdWithDeps(deps))
	return cmd
}

func newVMAddCmdWithDeps(deps vmDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Discover and add new VMs to an existing config",
		Long:  "Scans for VMs not already present in config.yaml (by name) and walks through the same selection/manual-entry/schedule prompts `init` uses, without re-asking about destination, compression, retention, or notifications.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runVMAdd(cmd, deps)
		},
	}
	return cmd
}

func runVMAdd(cmd *cobra.Command, deps vmDeps) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	discovered, err := discoverVMsWithContext(cmd.Context(), deps.discoverVMs, deps.searchDirs())
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}

	configured := make(map[string]bool, len(cfg.VMs))
	for _, vmCfg := range cfg.VMs {
		configured[vmCfg.Name] = true
	}
	var newCandidates []discoveredVM
	for _, c := range discovered {
		if !configured[c.Name] {
			newCandidates = append(newCandidates, c)
		}
	}
	tuiCandidates := make([]tui.VMCandidate, len(newCandidates))
	for i, c := range newCandidates {
		tuiCandidates[i] = tui.VMCandidate{Name: c.Name, VMX: c.VMX}
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	outIsTerminal := deps.isTerminal != nil && deps.isTerminal(out)
	inIsTerminal := deps.isTerminalIn != nil && deps.isTerminalIn(in)
	accessible := !outIsTerminal || !inIsTerminal

	added, err := deps.addVMs(cmd.Context(), in, out, accessible, tuiCandidates)
	if err != nil {
		return err
	}
	if len(added) == 0 {
		_, err := fmt.Fprintln(out, "no VMs added")
		return err
	}

	cfg.VMs = append(cfg.VMs, added...)
	if err := config.ValidateVMs(cfg.VMs); err != nil {
		return fmt.Errorf("invalid VM selection: %w", err)
	}

	data, err := deps.marshal(cfg)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := deps.writeFile(configPath, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	_, err = fmt.Fprintf(out, "added %d VM(s), wrote config to %s\n", len(added), configPath)
	return err
}

func newVMRemoveCmdWithDeps(deps vmDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a VM from config.yaml",
		Long:  "Removes the named VM from config.yaml, as configured. This only edits config.yaml -- it does not touch the VM itself or any of its existing backup archives.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runVMRemove(cmd, deps, args[0])
		},
	}
	return cmd
}

func runVMRemove(cmd *cobra.Command, deps vmDeps, name string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	remaining, removed := removeVMConfig(cfg.VMs, name)
	if !removed {
		return fmt.Errorf("no VM named %q in config %s", name, configPath)
	}
	cfg.VMs = remaining

	data, err := deps.marshal(cfg)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := deps.writeFile(configPath, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %q, wrote config to %s\n", name, configPath)
	return err
}

// removeVMConfig returns vms with the entry named name removed (a new
// slice; vms itself is left untouched) and whether one was found.
func removeVMConfig(vms []config.VM, name string) ([]config.VM, bool) {
	out := make([]config.VM, 0, len(vms))
	removed := false
	for _, vmCfg := range vms {
		if vmCfg.Name == name {
			removed = true
			continue
		}
		out = append(out, vmCfg)
	}
	return out, removed
}
