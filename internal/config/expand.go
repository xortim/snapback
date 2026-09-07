package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandTilde expands a leading "~" (the current user's home directory
// alone) or "~/..." prefix in path using os.UserHomeDir. Any other
// leading-tilde form (e.g. "~otheruser/...") is left untouched -- this
// package only resolves the current user's home, not arbitrary user
// lookups. Exported so internal/tui's init wizard can validate a
// proposed destination the same way Load resolves one already written
// to config.yaml, without duplicating this logic.
func ExpandTilde(path string) (string, error) {
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
