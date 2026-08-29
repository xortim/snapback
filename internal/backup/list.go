package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Archive describes one completed backup: its ArchiveID (the directory
// name written by Run, "<vmname>-<timestamp>") and the Manifest read back
// from that directory's manifest.json.
type Archive struct {
	ArchiveID string
	Manifest  Manifest
}

// ListArchives scans destination for archive directories and returns
// their manifests, newest first. destination not existing yet (no backup
// has ever run there) is reported as zero archives, not an error -- but
// an empty destination string (an unconfigured/misconfigured config, not
// a legitimate first-run path) is rejected outright: os.ReadDir("") also
// fails with ErrNotExist, and letting that fall through would silently
// report "no archives" for a config that never set destination at all. A
// directory whose manifest.json is missing, unreadable, or malformed is
// skipped rather than aborting the whole scan -- writeManifest
// (manifest.go) isn't atomic, so a manifest can legitimately be mid-write
// if ListArchives races a concurrent, still-running `snapback run`;
// treating that as "this one archive isn't ready yet" keeps every other,
// already-complete archive visible instead of hiding all of them behind
// one in-progress or corrupt entry.
func ListArchives(destination string) ([]Archive, error) {
	if destination == "" {
		return nil, fmt.Errorf("destination is required")
	}

	entries, err := os.ReadDir(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read destination %s: %w", destination, err)
	}

	var archives []Archive
	for _, entry := range entries {
		if !isArchiveDir(destination, entry) {
			continue
		}
		manifestPath := filepath.Join(destination, entry.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			// Missing (a run that died before Checksumming), truncated (raced
			// a concurrent in-flight run's non-atomic writeManifest), or
			// otherwise unreadable -- every case is "this one archive isn't
			// ready to be listed yet", not a reason to hide every other
			// archive by aborting the whole scan.
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		archives = append(archives, Archive{ArchiveID: entry.Name(), Manifest: m})
	}

	sortArchives(archives)
	return archives, nil
}

// sortArchives sorts archives newest-first by Manifest.Timestamp, breaking
// ties on equal timestamps (one-second resolution, so same-second archives
// are common) by ArchiveID ascending, so output order is deterministic
// regardless of input order.
func sortArchives(archives []Archive) {
	sort.Slice(archives, func(i, j int) bool {
		ti, tj := archives[i].Manifest.Timestamp, archives[j].Manifest.Timestamp
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return archives[i].ArchiveID < archives[j].ArchiveID
	})
}

// isArchiveDir reports whether entry (a child of destination) is a
// directory, following a symlink if entry itself is one -- a symlinked
// archive directory (e.g. one relocated onto slower storage) should list
// like any other.
func isArchiveDir(destination string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(destination, entry.Name()))
	return err == nil && info.IsDir()
}
