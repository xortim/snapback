package launchd

import (
	"bytes"
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
	// "Could not find service" means the label isn't currently loaded --
	// Sync's contract is idempotent removal, so that's not an error here.
	if err != nil && strings.Contains(err.Error(), "Could not find service") {
		return nil
	}
	return err
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
