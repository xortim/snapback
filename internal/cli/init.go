package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

// initDeps groups init's external dependencies so tests can substitute a
// fake VM scanner, a config writer that captures its argument, and a
// fake existing-file check instead of touching the real filesystem or
// ~/Virtual Machines.
type initDeps struct {
	searchDirs  func() []string
	discoverVMs func(searchDirs []string) ([]discoveredVM, error)
	marshal     func(cfg *config.Config) ([]byte, error)
	writeFile   func(path string, data []byte) error
	fileExists  func(path string) bool
}

func newInitCmd() *cobra.Command {
	return newInitCmdWithDeps(initDeps{
		searchDirs:  defaultVMSearchDirs,
		discoverVMs: discoverVMs,
		marshal:     config.Marshal,
		writeFile:   writeConfigFile,
		fileExists:  configFileExists,
	})
}

// writeConfigFile creates the config file's parent directory if it
// doesn't already exist, then writes data to path with 0644 permissions
// -- world-readable, since config.yaml holds no secrets, just VM paths
// and retention settings. Left unwrapped: runInit already wraps whatever
// this returns as "write config: %w", and a second wrap here would just
// double that context.
//
// Writes to a temp file in the same directory first, then renames it
// into place, rather than truncating path directly -- with --force
// overwriting an existing config, a write that's interrupted partway
// (disk full, process killed) must not leave the user's previous,
// working config truncated.
func writeConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once Rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
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
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive config bootstrap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runInit(cmd, deps, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

func runInit(cmd *cobra.Command, deps initDeps, force bool) error {
	configPath, err := configPathForCmd(cmd)
	if err != nil {
		return err
	}
	if !force && deps.fileExists(configPath) {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	}

	out := cmd.OutOrStdout()
	in := bufio.NewScanner(cmd.InOrStdin())

	candidates, err := deps.discoverVMs(deps.searchDirs())
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}

	vms, err := promptVMs(out, in, candidates)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}

	destination, err := promptString(out, in, "Backup destination", "/Volumes/Backups/snapback")
	if err != nil {
		return err
	}
	compression, err := promptChoice(out, in, "Compression (zstd/gzip)", "zstd", []string{"zstd", "gzip"})
	if err != nil {
		return err
	}
	keepLast, err := promptInt(out, in, "Keep last N backups", 5)
	if err != nil {
		return err
	}
	keepDaily, err := promptInt(out, in, "Keep daily backups for N days", 7)
	if err != nil {
		return err
	}
	keepWeekly, err := promptInt(out, in, "Keep weekly backups for N weeks", 4)
	if err != nil {
		return err
	}
	notify, err := promptBool(out, in, "Enable notifications", true)
	if err != nil {
		return err
	}

	cfg := &config.Config{
		Destination: destination,
		Compression: compression,
		Retention: config.Retention{
			KeepLast:   keepLast,
			KeepDaily:  keepDaily,
			KeepWeekly: keepWeekly,
		},
		VMs:           vms,
		Notifications: config.Notifications{Enabled: notify},
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("built an invalid config: %w", err)
	}

	data, err := deps.marshal(cfg)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := deps.writeFile(configPath, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
}

// promptVMs lists candidates (found by discoverVMs) and asks the user
// which to include, or -- if none were found -- falls back to prompting
// for VMs one at a time by name and .vmx path.
func promptVMs(out io.Writer, in *bufio.Scanner, candidates []discoveredVM) ([]config.VM, error) {
	if len(candidates) == 0 {
		if _, err := fmt.Fprintln(out, "no VMs found automatically; enter them manually (blank name to stop)"); err != nil {
			return nil, err
		}
		return promptManualVMs(out, in)
	}

	if _, err := fmt.Fprintln(out, "discovered VMs:"); err != nil {
		return nil, err
	}
	for i, c := range candidates {
		if _, err := fmt.Fprintf(out, "  %d) %s (%s)\n", i+1, c.Name, c.VMX); err != nil {
			return nil, err
		}
	}

	selection, err := promptString(out, in, `Include which VMs? (comma-separated numbers, or "all")`, "all")
	if err != nil {
		return nil, err
	}

	var selected []discoveredVM
	if strings.EqualFold(selection, "all") {
		selected = candidates
	} else {
		for _, tok := range strings.Split(selection, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 1 || idx > len(candidates) {
				return nil, fmt.Errorf("%q is not a valid VM number (1-%d)", tok, len(candidates))
			}
			selected = append(selected, candidates[idx-1])
		}
	}

	vms := make([]config.VM, len(selected))
	for i, c := range selected {
		vms[i] = config.VM{Name: c.Name, VMX: c.VMX}
	}
	return vms, nil
}

func promptManualVMs(out io.Writer, in *bufio.Scanner) ([]config.VM, error) {
	var vms []config.VM
	for {
		name, err := promptString(out, in, "VM name (blank to stop)", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return vms, nil
		}
		vmx, err := promptString(out, in, "  .vmx path for "+name, "")
		if err != nil {
			return nil, err
		}
		if vmx == "" {
			return nil, fmt.Errorf("VM %q needs a .vmx path", name)
		}
		vms = append(vms, config.VM{Name: name, VMX: vmx})
	}
}

// promptString prints label with defaultVal shown, reads one line from
// in, and returns it trimmed, or defaultVal if the line is blank.
// Returns an error if in runs out of input or fails to read -- a
// truncated interactive session means the resulting config was never
// actually reviewed by the user, so init treats that as a hard failure
// rather than silently falling back to defaults.
func promptString(out io.Writer, in *bufio.Scanner, label, defaultVal string) (string, error) {
	if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultVal); err != nil {
		return "", err
	}
	if !in.Scan() {
		if err := in.Err(); err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return "", fmt.Errorf("read input: unexpected end of input")
	}
	line := strings.TrimSpace(in.Text())
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

func promptChoice(out io.Writer, in *bufio.Scanner, label, defaultVal string, choices []string) (string, error) {
	val, err := promptString(out, in, label, defaultVal)
	if err != nil {
		return "", err
	}
	for _, c := range choices {
		if val == c {
			return val, nil
		}
	}
	return "", fmt.Errorf("%q is not one of %v", val, choices)
}

func promptInt(out io.Writer, in *bufio.Scanner, label string, defaultVal int) (int, error) {
	val, err := promptString(out, in, label, strconv.Itoa(defaultVal))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", val)
	}
	return n, nil
}

func promptBool(out io.Writer, in *bufio.Scanner, label string, defaultVal bool) (bool, error) {
	defaultStr := "y"
	if !defaultVal {
		defaultStr = "n"
	}
	val, err := promptString(out, in, label+" (y/n)", defaultStr)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(val) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not y/n", val)
	}
}
