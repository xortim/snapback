package launchd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Installer is the shell-out/filesystem boundary for launchd plist
// management -- Sync (Task 7) is written and tested against
// FakeInstaller, mirroring vm.Controller/vm.FakeVMController's split, so
// its choreography never needs a real launchctl or a real
// ~/Library/LaunchAgents to run its unit tests.
type Installer interface {
	// Write renders agent's plist and writes it to plistPath (creating
	// the parent directory if needed), returning the path written and
	// whether the content differs from whatever was already at that
	// path (true if the file didn't exist before).
	Write(agent Agent) (plistPath string, changed bool, err error)
	// Bootstrap loads the plist at plistPath into the current user's GUI
	// launchd domain.
	Bootstrap(plistPath string) error
	// Bootout unloads the job with the given label from the GUI domain.
	// Not an error if the job isn't currently loaded.
	Bootout(label string) error
	// Remove deletes the plist file for label from disk. Not an error if
	// it's already gone.
	Remove(label string) error
	// List returns the labels of every snapback-managed plist file
	// currently on disk (matching "com.tim.snapback.*"), regardless of
	// whether it's currently bootstrapped.
	List() ([]string, error)
}

// LaunchctlInstaller is the real Installer, shelling out to launchctl
// against plist files under Dir.
type LaunchctlInstaller struct {
	// Dir is where plist files are read/written -- normally
	// ~/Library/LaunchAgents. Exported and settable so integration tests
	// can point it at a scratch directory instead of the real one.
	Dir string
}

// NewLaunchctlInstaller returns a LaunchctlInstaller rooted at
// ~/Library/LaunchAgents.
func NewLaunchctlInstaller() (*LaunchctlInstaller, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	return &LaunchctlInstaller{Dir: filepath.Join(home, "Library", "LaunchAgents")}, nil
}

func (l *LaunchctlInstaller) plistPath(label string) string {
	return filepath.Join(l.Dir, label+".plist")
}

func (l *LaunchctlInstaller) Write(agent Agent) (string, bool, error) {
	path := l.plistPath(agent.Label)
	data, err := renderPlist(agent)
	if err != nil {
		return "", false, err
	}
	existing, readErr := os.ReadFile(path)
	changed := readErr != nil || !bytes.Equal(existing, data)
	if !changed {
		return path, false, nil
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create %s: %w", l.Dir, err)
	}
	// The plist's StandardOutPath/StandardErrorPath point at
	// ~/Library/Logs/snapback/<label>.log; launchd won't create that
	// directory itself, so a scheduled run would produce no log at all on
	// a fresh install unless we create it here.
	if agent.LogPath != "" {
		logDir := filepath.Dir(agent.LogPath)
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return "", false, fmt.Errorf("create %s: %w", logDir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", false, fmt.Errorf("write %s: %w", path, err)
	}
	return path, true, nil
}

func (l *LaunchctlInstaller) Bootstrap(plistPath string) error {
	return runLaunchctl("bootstrap", guiDomain(), plistPath)
}

func (l *LaunchctlInstaller) Bootout(label string) error {
	err := runLaunchctl("bootout", guiDomain()+"/"+label)
	if err != nil && isNotLoadedError(err) {
		return nil
	}
	return err
}

// isNotLoadedError reports whether err from `launchctl bootout` means
// "that label wasn't loaded in the first place" rather than a real
// failure. Sync's contract is idempotent removal (and, since the install
// path also boots out defensively before bootstrapping, idempotent
// install), so this must not surface as an error.
//
// Deliberately defensive: launchctl's exact wording here isn't
// verifiable in this environment (no live macOS/launchd in CI), and it
// has differed across releases -- "Could not find service" is what
// `launchctl print` emits, while modern `bootout gui/<uid>/<label>` on
// an unloaded label reports "Boot-out failed: 3: No such process" and
// exits 3. All three forms are tolerated rather than betting on one.
// TestIntegration_BootoutNeverBootstrapped (launchctl_integration_test.go,
// behind -tags=integration + SNAPBACK_INTEGRATION=1) is where this
// should eventually be confirmed against real launchd.
func isNotLoadedError(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "Could not find service") || strings.Contains(msg, "No such process")
}

func (l *LaunchctlInstaller) Remove(label string) error {
	err := os.Remove(l.plistPath(label))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *LaunchctlInstaller) List() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(l.Dir, labelPrefix+"*.plist"))
	if err != nil {
		return nil, err
	}
	labels := make([]string, len(matches))
	for i, m := range matches {
		labels[i] = strings.TrimSuffix(filepath.Base(m), ".plist")
	}
	return labels, nil
}

func guiDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
