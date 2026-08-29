package backup

import (
	"encoding/json"
	"errors"
	"fmt"
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
// has ever run) is reported as zero archives, not an error. A directory
// with no manifest.json is skipped rather than treated as an error too --
// that's the signature of a run that died before Checksumming wrote it
// (see choreography.go's succeeded/defer cleanup comment), not a corrupt
// archive.
func ListArchives(destination string) ([]Archive, error) {
	entries, err := os.ReadDir(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read destination %s: %w", destination, err)
	}

	var archives []Archive
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(destination, entry.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse manifest %s: %w", manifestPath, err)
		}
		archives = append(archives, Archive{ArchiveID: entry.Name(), Manifest: m})
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].Manifest.Timestamp.After(archives[j].Manifest.Timestamp)
	})
	return archives, nil
}
