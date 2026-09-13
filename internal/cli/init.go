package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/atomicfile"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
	"github.com/xortim/snapback/internal/tui"
)

// initDeps groups init's external dependencies so tests can substitute a
// fake VM scanner, a fake wizard, a config writer that captures its
// argument, and a fake existing-file check instead of touching the real
// filesystem, a real terminal, or requiring a Fusion install.
type initDeps struct {
	searchDirs   func() []string
	discoverVMs  func(searchDirs []string) ([]discoveredVM, error)
	loadConfig   func(path string) (*config.Config, error)
	marshal      func(cfg *config.Config) ([]byte, error)
	writeFile    func(path string, data []byte) error
	fileExists   func(path string) bool
	isTerminal   func(w io.Writer) bool
	isTerminalIn func(r io.Reader) bool
	runWizard    func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate, prior *config.Config) (*config.Config, error)
	newInstaller func() (launchd.Installer, error)
	executable   func() (string, error)
}

func newInitCmd() *cobra.Command {
	return newInitCmdWithDeps(initDeps{
		searchDirs:   defaultVMSearchDirs,
		discoverVMs:  discoverVMs,
		loadConfig:   config.Load,
		marshal:      config.Marshal,
		writeFile:    writeConfigFile,
		fileExists:   configFileExists,
		isTerminal:   defaultIsTerminal,
		isTerminalIn: defaultIsTerminalIn,
		runWizard:    tui.RunInitWizard,
		newInstaller: defaultNewInstaller,
		executable:   os.Executable,
	})
}

// writeConfigFile writes data to path with 0644 permissions --
// world-readable, since config.yaml holds no secrets, just VM paths and
// retention settings -- atomically (see internal/atomicfile), so a write
// interrupted partway (disk full, process killed) can't leave the user's
// previous, working config truncated when --force overwrites an existing
// one. Left unwrapped: runInit already wraps whatever this returns as
// "write config: %w", and a second wrap here would just double that
// context.
func writeConfigFile(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0o644)
}

// configFileExists reports whether path exists, treating any Stat error
// (not just os.ErrNotExist) as "does not exist" -- init's caller only
// needs a yes/no to decide whether --force is required, not the reason.
func configFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func newInitCmdWithDeps(deps initDeps) *cobra.Command {
	var force bool
	var extraSearchDirs []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive config bootstrap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runInit(cmd, deps, force, extraSearchDirs)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	cmd.Flags().StringArrayVar(&extraSearchDirs, "search-dir", nil, "additional directory to scan for VMs, alongside the defaults (repeatable)")
	return cmd
}

func runInit(cmd *cobra.Command, deps initDeps, force bool, extraSearchDirs []string) error {
	configPath, err := configPathForCmd(cmd)
	if err != nil {
		return err
	}
	exists := deps.fileExists(configPath)
	if !force && exists {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	}

	// --force over an established config: seed the wizard's defaults from
	// what's already there (see internal/tui/init.go's promptCoreSettings
	// and promptSchedules doc comments) instead of silently proposing to
	// reset destination/compression/retention/notifications/schedules
	// back to factory defaults or "none" -- runInit auto-syncs
	// LaunchAgents right after writing config below, so a reset schedule
	// here doesn't just change a config field, it deletes a working
	// LaunchAgent. A failure to load the existing config used to fall
	// back to prior == nil and just print a note -- but that silently
	// reset every VM's schedule to "none" the same way, so the very next
	// auto-sync would delete every real, working LaunchAgent with no
	// chance for the user to notice or object first (#88). Blocking here
	// is the safer default; the user can fix or remove the unparseable
	// config.yaml first, or rerun without --force.
	var prior *config.Config
	if force && exists {
		loaded, loadErr := deps.loadConfig(configPath)
		if loadErr != nil {
			return fmt.Errorf("could not load existing config at %s to preserve VM schedules: %w (fix or remove it, or rerun without --force)", configPath, loadErr)
		}
		prior = loaded
	}

	// extraSearchDirs (--search-dir, repeatable) is appended after the
	// defaults rather than replacing them: config.yaml doesn't exist yet
	// at the point init needs this (see #47), so there's no persisted
	// vm_search_dirs list to merge with -- just the two hardcoded
	// defaults plus whatever the user names on the command line for this
	// one run.
	searchDirs := append(deps.searchDirs(), extraSearchDirs...)
	candidates, err := discoverVMsWithContext(cmd.Context(), deps.discoverVMs, searchDirs)
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}
	tuiCandidates := make([]tui.VMCandidate, len(candidates))
	for i, c := range candidates {
		tuiCandidates[i] = tui.VMCandidate{Name: c.Name, VMX: c.VMX, Dir: c.Dir}
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	// A real bubbletea program needs a real terminal on *both* ends: it
	// renders into stdout, and it reads raw keypresses from stdin. Either
	// one not being a real terminal -- a redirected/piped stdout, or (the
	// case the naive out-only check used to miss) `snapback init <
	// answers.txt` run at an actual terminal, where stdout is a real tty
	// but stdin is a redirected file -- means the rich interactive path
	// can't work, so accessible mode is the safe default whenever either
	// check comes back false or unset.
	outIsTerminal := deps.isTerminal != nil && deps.isTerminal(out)
	inIsTerminal := deps.isTerminalIn != nil && deps.isTerminalIn(in)
	accessible := !outIsTerminal || !inIsTerminal

	cfg, err := deps.runWizard(cmd.Context(), in, out, accessible, tuiCandidates, prior)
	if err != nil {
		return err
	}

	if len(cfg.VMs) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}

	// cfg here is the wizard's freshly-built config, whose Destination is
	// still the raw, unexpanded value the user typed (e.g.
	// "~/Backups/snapback") -- unlike the other persistConfigAndSync call
	// sites (vm.go), which all load cfg via config.Load and so get it
	// pre-expanded. Expand a local copy just for the sync call; cfg.Destination
	// itself, and what gets written to config.yaml, must stay in the
	// portable "~/..." form.
	syncDest := cfg.Destination
	if deps.newInstaller != nil {
		expanded, err := config.ExpandTilde(cfg.Destination)
		if err != nil {
			return fmt.Errorf("expand destination: %w", err)
		}
		syncDest = expanded
	}
	if err := persistConfigAndSync(cmd, deps.marshal, deps.writeFile, deps.newInstaller, deps.executable, configPath, cfg, syncDest); err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
}

// discoverVMsWithContext runs scan (deps.discoverVMs) in a goroutine and
// races it against ctx.Done() -- like the accessible-mode input read
// internal/tui/init.go's runForm races the same way, a filesystem scan
// has no way to be interrupted directly, so without this a SIGINT
// arriving while ~/Virtual Machines sits on a stalled network or
// external volume would have nothing to notice it, leaving init hung
// despite ctx already being canceled. The goroutine leaks past
// cancellation, blocked on the scan, but the process is exiting anyway.
func discoverVMsWithContext(ctx context.Context, scan func([]string) ([]discoveredVM, error), searchDirs []string) ([]discoveredVM, error) {
	type result struct {
		vms []discoveredVM
		err error
	}
	done := make(chan result, 1)
	go func() {
		vms, err := scan(searchDirs)
		done <- result{vms, err}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("init cancelled: %w", ctx.Err())
	case r := <-done:
		return r.vms, r.err
	}
}
