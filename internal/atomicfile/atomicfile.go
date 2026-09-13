// Package atomicfile writes files atomically, so a crash or power loss
// mid-write can never leave a truncated, corrupt file behind for whatever
// reads it next (a config.yaml `snapback init --force` would otherwise
// overwrite in place, or a plist `launchctl bootstrap` would choke on --
// see #94). Shared by internal/cli (config.yaml) and internal/launchd
// (LaunchAgent plists), which previously carried near-identical copies of
// this logic (#97).
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile creates path's parent directory if it doesn't already exist,
// then writes data to path with the given permissions by writing a temp
// file in the same directory first and renaming it into place -- rather
// than truncating path directly, so a write interrupted partway never
// leaves a corrupt file where a previous, working one used to be.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".atomicfile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once Rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
