package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xortim/snapback/internal/retention"
)

// PrunedArchive is one archive Prune considered, with the reasons
// retention.Select gave for keeping it (empty for a removed archive).
type PrunedArchive struct {
	Archive
	Reasons []string
}

// PruneResult reports what Prune kept and removed (or, under dryRun,
// would remove). Errs holds per-archive removal failures; one failure
// doesn't stop the rest.
type PruneResult struct {
	Kept    []PrunedArchive
	Removed []PrunedArchive
	Errs    []error
}

// Prune applies p to vmName's archives under destination, holding the
// same per-VM lock run and cleanup use. A held lock returns an error
// wrapping ErrLocked. See
// docs/superpowers/specs/2026-10-28-retention-pruning-design.md.
func Prune(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
	lock, err := AcquireLock(destination, vmName)
	if err != nil {
		return PruneResult{}, err
	}
	defer func() { _ = lock.Release() }()
	return pruneLocked(destination, vmName, p, now, dryRun)
}

// pruneLocked is Prune without taking the lock -- Run calls it directly
// since it already holds the lock. The returned error is reserved for
// failures that prevent pruning at all (listing); per-archive removal
// failures go in PruneResult.Errs.
func pruneLocked(destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
	archives, err := ListArchives(destination)
	if err != nil {
		return PruneResult{}, fmt.Errorf("list archives: %w", err)
	}

	byID := make(map[string]Archive)
	var cands []retention.Candidate
	for _, a := range archives {
		// Manifest VMName equality, never the directory name: archive IDs
		// are "<vm>-<ts>", so "ubuntu" and "ubuntu-dev" share a prefix.
		if a.Manifest.VMName != vmName {
			continue
		}
		byID[a.ArchiveID] = a
		cands = append(cands, retention.Candidate{ID: a.ArchiveID, Timestamp: a.Manifest.Timestamp})
	}

	var res PruneResult
	for _, d := range retention.Select(cands, p, now) {
		pa := PrunedArchive{Archive: byID[d.ID], Reasons: d.Reasons}
		if d.Keep {
			res.Kept = append(res.Kept, pa)
			continue
		}
		res.Removed = append(res.Removed, pa)
		if dryRun {
			continue
		}
		if err := removeArchive(filepath.Join(destination, d.ID)); err != nil {
			res.Errs = append(res.Errs, err)
		}
	}
	return res, nil
}

// removeArchive deletes manifest.json first -- the single step that makes
// dir stop being an archive to ListArchives, list, and restore -- then
// the rest. A failure after that leaves an unlisted directory, never a
// half-deleted archive that still looks restorable. A symlinked archive
// directory is refused outright: removing manifest.json through the link
// would delete inside its target.
func removeArchive(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("prune %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("prune %s: archive directory is a symlink; not deleting through it, remove by hand", dir)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prune %s: remove manifest: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("prune %s: manifest removed but directory left behind, remove by hand: %w", dir, err)
	}
	return nil
}
