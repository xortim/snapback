package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractArchive decompresses+untars srcPath (compressed as identified by
// compression, "zstd" or "gzip" -- Manifest.Compression, not re-sniffed)
// into destDir, which must not already exist. Mirrors createArchive's
// shape in reverse. If onWrite is non-nil, it's invoked with the running
// cumulative bytes written across all files.
func extractArchive(srcPath, destDir, compression string, onWrite func(cumulativeBytes int64)) error {
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("extract archive: %s already exists (left by a previous failed restore attempt -- if you're not currently retrying that restore, it's safe to remove and try again)", destDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()

	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return untarFrom(gz, destDir, onWrite)
	case "zstd":
		return untarFromZstd(in, destDir, onWrite)
	default:
		return fmt.Errorf("unknown compression %q", compression)
	}
}

// untarFromZstd streams in through the external zstd binary's decompressor
// and untars the result into destDir -- the reverse of tarToZstd
// (archive.go). Manifest.Compression already records that this archive
// was compressed with zstd, so there's no fallback-detection logic here:
// if zstd isn't on PATH, restoring this archive fails outright.
func untarFromZstd(in io.Reader, destDir string, onWrite func(cumulativeBytes int64)) error {
	if _, err := lookZstd(); err != nil {
		return fmt.Errorf("zstd not found on PATH (required to restore a zstd-compressed archive): %w", err)
	}
	cmd := exec.Command("zstd", "-d", "-q")
	cmd.Stdin = in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("zstd stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	untarErr := untarFrom(stdout, destDir, onWrite)
	// Drain any remaining stdout to unblock zstd if untarFrom exited early
	// (corrupt tar, path-traversal rejection) before fully reading its output.
	// This prevents a deadlock where zstd blocks on a full pipe buffer.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	if untarErr != nil {
		return untarErr
	}
	return nil
}

// untarFrom reads a tar stream from r and writes its entries under destDir,
// rejecting any entry whose resolved path would land outside destDir
// (rejecting ".."-escaping or absolute entry names) -- a cheap, standard
// guard against a corrupted or tampered archive writing outside the
// staging directory. If onWrite is non-nil, it's invoked as file bytes are
// written, with the running cumulative total across all files.
func untarFrom(r io.Reader, destDir string, onWrite func(cumulativeBytes int64)) error {
	tr := tar.NewReader(r)
	var cumulative int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		if !isWithinDir(destDir, target) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		// Older archives (created before createArchive started excluding
		// these) may still carry a Fusion "<file>.lck" directory copied
		// from the live source VM -- process-specific lock state that's
		// meaningless once restored elsewhere, and actively harmful:
		// vmware-vdiskmanager -e mistakes a restored, stale lock directory
		// for one another process already has the disk open, failing the
		// post-extraction disk consistency check over nothing (confirmed
		// real case, 2026-09-11). Drop any such entry on extraction too,
		// so an archive made before that fix still restores cleanly. This
		// runs after the traversal check above so a tampered entry can't
		// dodge it just by ending in ".lck".
		if hasLockDirComponent(hdr) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeSymlink:
			// Validate that symlink target doesn't escape destDir.
			// Reject absolute paths outright; for relative paths, resolve
			// against the symlink's directory and check it stays within destDir.
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("symlink %q has absolute target %q (escapes destination directory)", hdr.Name, hdr.Linkname)
			}
			resolvedTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
			if !isWithinDir(destDir, resolvedTarget) {
				return fmt.Errorf("symlink %q with target %q escapes destination directory", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			n, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
			cumulative += n
			if onWrite != nil {
				onWrite(cumulative)
			}
		default:
			// Anything else (device, fifo, ...) isn't something
			// createArchive ever produces from a .vmwarevm bundle -- skip
			// rather than fail the whole restore over it.
		}
	}
}

// hasLockDirComponent reports whether hdr names an entry that createArchive
// would have excluded as a Fusion lock directory ("<file>.lck"): either the
// entry is itself such a directory, or it's nested under one as an ancestor
// path component. A plain file whose own name happens to end in ".lck" is
// left alone, matching createArchive's directory-only exclusion (see its
// call site in untarFrom) -- so a genuine file with that suffix that
// createArchive did include isn't silently dropped on restore.
func hasLockDirComponent(hdr *tar.Header) bool {
	parts := strings.Split(strings.Trim(hdr.Name, "/"), "/")
	for i, part := range parts {
		isEntryItself := i == len(parts)-1
		if isEntryItself && hdr.Typeflag != tar.TypeDir {
			continue
		}
		if isLockDirName(part) {
			return true
		}
	}
	return false
}

// isWithinDir reports whether target, once resolved relative to dir,
// stays under dir -- i.e. its relative path doesn't start with "..".
func isWithinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
