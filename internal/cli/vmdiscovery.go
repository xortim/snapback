package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// vmBundleExt is the directory extension Fusion uses for a VM bundle
// (e.g. myvm.vmwarevm). Matched case-insensitively in discoverVMs since a
// bundle copied or renamed from elsewhere can carry a differently-cased
// extension.
const vmBundleExt = ".vmwarevm"

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
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("scan %s: %w", dir, err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(strings.ToLower(entry.Name()), vmBundleExt) || !isVMBundleDir(dir, entry) {
				continue
			}
			name := entry.Name()[:len(entry.Name())-len(vmBundleExt)]
			vmx := filepath.Join(dir, entry.Name(), name+".vmx")
			info, err := os.Stat(vmx)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			found = append(found, discoveredVM{Name: name, VMX: vmx})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, nil
}

// isVMBundleDir reports whether entry (a child of dir) is a directory,
// following a symlink if entry itself is one -- os.DirEntry.IsDir()
// reflects only the dirent's own type and returns false for a symlink
// even when it resolves to a directory, which would otherwise hide a
// .vmwarevm bundle stored on other media and symlinked into the search
// directory to keep it visible in Fusion's library.
func isVMBundleDir(dir string, entry os.DirEntry) bool {
	if entry.Type()&os.ModeSymlink == 0 {
		return entry.IsDir()
	}
	info, err := os.Stat(filepath.Join(dir, entry.Name()))
	return err == nil && info.IsDir()
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
