package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xortim/snapback/internal/retention"
)

// ErrSymlinkedArchive marks a removal refused because the archive
// directory is a symlink; see removeArchive.
var ErrSymlinkedArchive = errors.New("archive directory is a symlink")

// PrunedArchive is one archive Prune considered, with the reasons
// retention.Select gave for keeping it (empty for a removed archive).
//
// Err is the removeArchive failure for this archive, nil on success and
// always nil for Kept entries and under dryRun. An entry in Removed whose
// Err is non-nil is still on disk (or is an unlisted leftover directory).
type PrunedArchive struct {
	Archive
	Reasons []string
	Err     error
}

// PruneResult reports what Prune kept and removed. Removed is the set
// selected for removal (under dryRun: would be removed); an entry whose
// Err is non-nil is still on disk (or is an unlisted leftover dir) -- see
// Err. Errs holds the same per-archive removal failures, collected; one
// failure doesn't stop the rest.
type PruneResult struct {
	Kept    []PrunedArchive
	Removed []PrunedArchive
	Errs    []error
}

// Prune applies p to vmName's archives under destination, holding the
// same per-VM lock run and cleanup use. A held lock returns an error
// wrapping ErrLocked. See
// docs/superpowers/specs/2026-10-28-retention-pruning-design.md.
func Prune(ctx context.Context, destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
	// AcquireLock MkdirAlls <destination>/.snapback-locks; against an
	// unmounted external drive that would create the path on the boot
	// volume and make macOS later mount the real drive as "<name> 1".
	if _, err := os.Stat(destination); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return PruneResult{}, nil
		}
		return PruneResult{}, fmt.Errorf("stat destination: %w", err)
	}
	lock, err := AcquireLock(destination, vmName)
	if err != nil {
		return PruneResult{}, err
	}
	defer func() { _ = lock.Release() }()
	return pruneLocked(ctx, destination, vmName, p, now, dryRun)
}

// pruneLocked is Prune without taking the lock -- Run calls it directly
// since it already holds the lock. The returned error is reserved for
// failures that prevent pruning at all (listing) and for ctx being
// cancelled, which is checked before each removal: on cancellation the
// partial PruneResult so far comes back with an error wrapping ctx.Err(),
// and archives not yet reached are left in place. Per-archive removal
// failures go in PruneResult.Errs.
func pruneLocked(ctx context.Context, destination, vmName string, p retention.Policy, now time.Time, dryRun bool) (PruneResult, error) {
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
		if !dryRun {
			if err := ctx.Err(); err != nil {
				return res, fmt.Errorf("prune interrupted: %w", err)
			}
			if err := removeArchive(filepath.Join(destination, d.ID)); err != nil {
				pa.Err = err
				res.Errs = append(res.Errs, err)
			}
		}
		res.Removed = append(res.Removed, pa)
	}
	return res, nil
}

// removeArchive deletes manifest.json first -- the single step that makes
// dir stop being an archive to ListArchives, list, and restore -- then
// the rest. A failure after that leaves an unlisted directory, never a
// half-deleted archive that still looks restorable. A symlinked archive
// directory is refused outright (ErrSymlinkedArchive): removing
// manifest.json through the link would delete inside its target. The
// error names the target and the by-hand fix, since the archive stays
// outside the policy -- and keeps failing every prune -- until then.
func removeArchive(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("prune %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(dir)
		if err != nil {
			target = "<unreadable link>"
		} else if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dir), target)
		}
		return fmt.Errorf("prune %s: %w to %s; snapback won't delete through a link. "+
			"It's outside the retention policy, so remove it by hand: delete the link (rm %q), "+
			"then the archive it points to (rm -r %q) if nothing else needs it",
			dir, ErrSymlinkedArchive, target, dir, target)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prune %s: remove manifest: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("prune %s: manifest removed but directory left behind, remove by hand: %w", dir, err)
	}
	return nil
}
