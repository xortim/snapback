package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FindArchive resolves archiveID to its Archive via ListArchives, returning
// a clear error if it isn't found -- before any I/O beyond the directory
// scan ListArchives itself already does. Exported for internal/cli's
// restore command, which needs to resolve an archive-id to its
// Manifest.VMName (for the missing-VM-requires-`--dest` check) before
// calling backup.Restore.
func FindArchive(destination, archiveID string) (Archive, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return Archive{}, err
	}
	for _, a := range archives {
		if a.ArchiveID == archiveID {
			return a, nil
		}
	}
	return Archive{}, fmt.Errorf("no archive %q found in %s", archiveID, destination)
}

// LatestArchiveForVM returns the newest archive whose Manifest.VMName
// matches vmName (ListArchives already returns newest-first), or an error
// if none exist. Backs `snapback restore --vm <name> --latest`.
func LatestArchiveForVM(destination, vmName string) (Archive, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return Archive{}, err
	}
	for _, a := range archives {
		if a.Manifest.VMName == vmName {
			return a, nil
		}
	}
	return Archive{}, fmt.Errorf("no archive found for VM %q in %s", vmName, destination)
}

// restoreTargetName returns "<bundleBase> - backup <yyyy-mm-dd>.vmwarevm"
// under parent, or that name with " (N)" inserted before the extension if
// the plain name already exists, trying N = 2, 3, ... until a free name is
// found. Capped at 100 attempts -- 100 same-day restores of the same VM
// without cleanup is almost certainly a bug, not a real use case.
func restoreTargetName(parent, bundleBase string, now time.Time) (string, error) {
	date := now.Format("2006-01-02")
	base := fmt.Sprintf("%s - backup %s.vmwarevm", bundleBase, date)
	if !pathExists(filepath.Join(parent, base)) {
		return base, nil
	}
	stem := fmt.Sprintf("%s - backup %s", bundleBase, date)
	for n := 2; n <= 100; n++ {
		candidate := fmt.Sprintf("%s (%d).vmwarevm", stem, n)
		if !pathExists(filepath.Join(parent, candidate)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("restoreTargetName: exhausted 100 collision attempts for %q under %q", base, parent)
}

// pathExists reports whether path exists (as anything -- file, dir, or
// symlink), without following a symlink to check its target.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
