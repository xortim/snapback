package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// fallbackDecompressionMultiplier bounds total decompressed bytes as a
	// multiple of the compressed archive's on-disk size, used only when
	// the archive's manifest predates Manifest.UncompressedSizeBytes (an
	// archive created before that field existed) and so carries no ground
	// truth to check against. Deliberately generous: a VM disk with large
	// zero-filled regions can legitimately compress at very high ratios,
	// and this is a last-resort guard against a truly pathological
	// expansion, not a tight bound.
	//
	// Note how little this does for a gzip archive specifically: deflate
	// caps a back-reference's match length at 258 bytes, so its compression
	// ratio on the highly repetitive (RLE-style) data an archive bomb is
	// built from asymptotes at roughly 510:1. A 500x multiplier therefore
	// only trips on a gzip archive compressed at very nearly that
	// theoretical ceiling. It earns its keep for zstd, which reaches far
	// higher ratios on constant data -- don't read the gzip path as
	// meaningfully bounded by this.
	fallbackDecompressionMultiplier = 500

	// decompressionSlackBytes is added on top of a manifest's recorded
	// UncompressedSizeBytes to get the real cap. It's not a
	// compression-ratio guess, and it isn't compensating for any known
	// systematic gap either: UncompressedSizeBytes is counted at tar time
	// from the same regular-file bytes, with the same "*.lck" exclusion,
	// that extraction re-produces, so the two should agree exactly. This is
	// plain defensive headroom for the rounding and per-entry bookkeeping
	// around that equality (tar's 512-byte header/padding granularity, a
	// manifest written by some future createArchive variant that counts
	// marginally differently). A real .vmwarevm bundle has a handful of
	// files, not thousands, so this is generous relative to that overhead.
	decompressionSlackBytes = 64 * 1024
)

// copyCapped copies from src to dst, stopping once remaining bytes have
// been written, and reports whether src had more data beyond that budget
// rather than silently truncating. It copies remaining+1 bytes in a single
// pass: if src is exhausted at or before remaining bytes, io.CopyN returns
// io.EOF and copyCapped reports limitHit=false; if the full remaining+1
// bytes copy without hitting src's EOF, there was more data than the
// budget allowed, and copyCapped reports limitHit=true (having written one
// byte past the budget, immediately followed by the caller aborting the
// whole extraction -- one byte of overrun is an acceptable cost for
// detecting the overrun without a second read pass).
func copyCapped(dst io.Writer, src io.Reader, remaining int64) (written int64, limitHit bool, err error) {
	n, cerr := io.CopyN(dst, src, remaining+1)
	switch cerr {
	case nil:
		return n, true, nil
	case io.EOF:
		return n, false, nil
	default:
		return n, false, cerr
	}
}

// extractArchive decompresses+untars srcPath (compressed as identified by
// compression, "zstd" or "gzip" -- Manifest.Compression, not re-sniffed)
// into destDir, which must not already exist. Mirrors createArchive's
// shape in reverse. expectedUncompressedBytes should be
// Manifest.UncompressedSizeBytes (0 for a manifest written before that
// field existed, in which case a generous multiplier against the
// compressed archive's own size is used instead) -- see the
// fallbackDecompressionMultiplier and decompressionSlackBytes doc
// comments for why. Either way, extraction aborts with an error rather
// than filling the destination disk if actual decompressed output
// exceeds the resulting cap. If onWrite is non-nil, it's invoked with
// the running cumulative bytes written across all files.
func extractArchive(srcPath, destDir, compression string, expectedUncompressedBytes int64, onWrite func(cumulativeBytes int64)) error {
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

	// Clamped rather than added blindly: expectedUncompressedBytes comes
	// from a manifest.json on disk, so a corrupt or tampered one carrying a
	// value near math.MaxInt64 would wrap the sum negative. Extraction would
	// still abort (the first entry immediately exceeds a negative budget),
	// but the error would quote a nonsensical negative byte count.
	maxBytes := int64(math.MaxInt64)
	if expectedUncompressedBytes <= math.MaxInt64-decompressionSlackBytes {
		maxBytes = expectedUncompressedBytes + decompressionSlackBytes
	}
	if expectedUncompressedBytes <= 0 {
		info, err := in.Stat()
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}
		maxBytes = info.Size() * fallbackDecompressionMultiplier
	}

	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return untarFrom(gz, destDir, maxBytes, onWrite)
	case "zstd":
		return untarFromZstd(in, destDir, maxBytes, onWrite)
	default:
		return fmt.Errorf("unknown compression %q", compression)
	}
}

// untarFromZstd streams in through the external zstd binary's decompressor
// and untars the result into destDir -- the reverse of tarToZstd
// (archive.go). Manifest.Compression already records that this archive
// was compressed with zstd, so there's no fallback-detection logic here:
// if zstd isn't on PATH, restoring this archive fails outright. maxBytes
// is the decompression cap, forwarded to untarFrom.
func untarFromZstd(in io.Reader, destDir string, maxBytes int64, onWrite func(cumulativeBytes int64)) error {
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

	untarErr := untarFrom(stdout, destDir, maxBytes, onWrite)
	// Drain any remaining stdout to unblock zstd if untarFrom exited early
	// (corrupt tar, path-traversal rejection, decompression cap hit)
	// before fully reading its output. This prevents a deadlock where
	// zstd blocks on a full pipe buffer.
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
// staging directory. Aborts once the running total of regular-file bytes
// written would exceed maxBytes, rather than letting a pathological
// archive fill the destination disk (see extractArchive's doc comment for
// how maxBytes is derived). If onWrite is non-nil, it's invoked as file
// bytes are written, with the running cumulative total across all files.
func untarFrom(r io.Reader, destDir string, maxBytes int64, onWrite func(cumulativeBytes int64)) error {
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
			n, limitHit, copyErr := copyCapped(out, tr, maxBytes-cumulative)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
			cumulative += n
			if limitHit {
				return fmt.Errorf("archive decompressed past %d bytes while extracting %q -- aborting to avoid filling the destination disk (archive may be corrupt or the manifest's recorded size doesn't match its actual contents)", maxBytes, hdr.Name)
			}
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
