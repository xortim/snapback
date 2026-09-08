package vm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// vdiskManagerCandidatePaths are checked, in order, when
// $SNAPBACK_VDISKMANAGER_PATH is unset and vmware-vdiskmanager isn't on
// $PATH -- mirrors vmcliCandidatePaths' reasoning: it lives under
// Fusion.app rather than a location a shell would resolve by default.
var vdiskManagerCandidatePaths = []string{
	"/Applications/VMware Fusion.app/Contents/Library/vmware-vdiskmanager",
}

// findVDiskManager locates the vmware-vdiskmanager binary:
// $SNAPBACK_VDISKMANAGER_PATH override first, then the known Fusion
// install location, then $PATH. Mirrors findVMCLI.
func findVDiskManager() (string, error) {
	if p := os.Getenv("SNAPBACK_VDISKMANAGER_PATH"); p != "" {
		return p, nil
	}
	for _, p := range vdiskManagerCandidatePaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("vmware-vdiskmanager"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("vmware-vdiskmanager not found: checked $SNAPBACK_VDISKMANAGER_PATH, %v, and $PATH", vdiskManagerCandidatePaths)
}

// execDiskConsistencyCheck runs `vmware-vdiskmanager -e diskPath` --
// vdiskmanager's own read-only consistency check, the same one that
// caught the real incident this exists for (docs/design.md's "Risks &
// Gotchas"): it opens diskPath's full parent chain and reports whether
// any link needs repair, without modifying anything. A nonzero exit
// means the chain failed the check; vdiskmanager writes its actual
// diagnosis to stderr (confirmed empirically against a real Fusion
// install -- e.g. "Disk chain is not consistent: ..."), so that's
// preferred over the bare exit-status error, falling back to stdout and
// then the raw error if stderr is empty.
func execDiskConsistencyCheck(path string) func(diskPath string) error {
	return func(diskPath string) error {
		cmd := exec.Command(path, "-e", diskPath)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = strings.TrimSpace(stdout.String())
			}
			if msg == "" {
				return fmt.Errorf("disk consistency check: %w", err)
			}
			return fmt.Errorf("disk consistency check: %s", msg)
		}
		return nil
	}
}
