package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoveredVM is one candidate VM found by discoverVMs, offered to the
// user during `snapback init` for inclusion in config.yaml.
type discoveredVM struct {
	Name string
	VMX  string
}

// discoverVMs scans each directory in searchDirs (one level down, not
// recursively -- matching how Fusion lays out ~/Virtual Machines) for
// *.vmwarevm bundles, returning one discoveredVM per bundle that
// contains a .vmx file matching the bundle's own name -- the layout
// every Fusion-created VM follows. A directory that doesn't exist is
// skipped, not an error: ~/Virtual Machines may not exist if Fusion was
// never run or VMs live elsewhere, and the caller can still fall back to
// manual entry. Results are sorted by Name for deterministic output.
func discoverVMs(searchDirs []string) ([]discoveredVM, error) {
	var found []discoveredVM
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan %s: %w", dir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".vmwarevm") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".vmwarevm")
			vmx := filepath.Join(dir, entry.Name(), name+".vmx")
			if _, err := os.Stat(vmx); err != nil {
				continue
			}
			found = append(found, discoveredVM{Name: name, VMX: vmx})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, nil
}

// defaultVMSearchDirs returns the directories discoverVMs scans by
// default: just ~/Virtual Machines, Fusion's standard location (see
// docs/design.md's config reference example). Returns nil (not an
// error) if the home directory can't be determined -- init falls back to
// manual VM entry in that case, same as when the directory doesn't
// exist.
func defaultVMSearchDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, "Virtual Machines")}
}
