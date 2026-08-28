package backup

import (
	"io/fs"
	"path/filepath"
)

// dirSize returns the sum of all regular file sizes under root. It's a
// package-level var (like archive.go's lookZstd) so choreography_internal_test.go
// can override it to force a deterministic Run() failure at this step --
// dirSize has no other seam that fails without relying on permission bits
// or filesystem races (see CLAUDE.md's testing conventions).
var dirSize = func(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
