package vm

import (
	"fmt"
	"os"
	"os/exec"
)

// findFusionBinary locates a binary that ships inside Fusion.app rather
// than somewhere a shell would resolve by default (see the vmrun $PATH
// gotcha in docs/design.md, "Risks & Gotchas"): envVar override first,
// then candidatePaths, then $PATH under name. Shared by findVMCLI
// (vmcli.go) and findVDiskManager (vdiskmanager.go), which otherwise
// duplicate this exact lookup order.
func findFusionBinary(envVar, name string, candidatePaths []string) (string, error) {
	if p := os.Getenv(envVar); p != "" {
		return p, nil
	}
	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found: checked $%s, %v, and $PATH", name, envVar, candidatePaths)
}
