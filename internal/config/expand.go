package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandTilde expands a leading "~" (the current user's home directory
// alone) or "~/..." prefix in path using os.UserHomeDir. Any other
// leading-tilde form (e.g. "~otheruser/...") is left untouched -- this
// package only resolves the current user's home, not arbitrary user
// lookups.
func expandTilde(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
