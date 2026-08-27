package backup

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyDir recursively copies the contents of src into dst, creating dst
// and any subdirectories as needed. Regular files are copied byte-for-byte;
// symlinks are recreated as symlinks (not followed). If onCopy is
// non-nil, it is invoked after each regular file finishes copying, with
// the running total of bytes copied so far across all files.
func copyDir(src, dst string, onCopy func(cumulativeBytes int64)) error {
	var cumulative int64
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			return os.Symlink(link, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		cumulative += info.Size()
		if onCopy != nil {
			onCopy(cumulative)
		}
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return out.Close()
}
