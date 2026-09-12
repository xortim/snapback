package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// lookZstd is overridable in tests to force the gzip fallback path without
// depending on whether zstd happens to be installed on the machine running
// the tests.
var lookZstd = func() (string, error) { return exec.LookPath("zstd") }

// archiveExt returns the file extension for an archive compressed with the
// given method (as recorded in Manifest.Compression / returned by
// createArchive) -- shared by Run and Restore so both name the archive
// file the same way.
func archiveExt(compression string) string {
	if compression == "zstd" {
		return "tar.zst"
	}
	return "tar.gz"
}

// createArchive tars srcDir's contents and compresses the result to
// destPath. requested is "zstd", "gzip", or "" (prefer zstd, fall back to
// gzip if the zstd binary isn't on PATH). Returns which compression was
// actually used. If onRead is non-nil, it is invoked as bytes are read
// from srcDir's files, with the running cumulative total across all
// files.
func createArchive(srcDir, destPath, requested string, onRead func(cumulativeBytes int64)) (string, error) {
	useZstd := false
	switch requested {
	case "gzip":
		useZstd = false
	case "zstd", "":
		_, err := lookZstd()
		useZstd = err == nil
	default:
		return "", fmt.Errorf("unknown compression %q", requested)
	}

	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	if useZstd {
		if err := tarToZstd(srcDir, out, onRead); err != nil {
			_ = out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("close archive: %w", err)
		}
		return "zstd", nil
	}

	gz := gzip.NewWriter(out)
	if err := tarTo(srcDir, gz, onRead); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("close gzip writer: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}
	return "gzip", nil
}

// tarToZstd streams a tar of srcDir through the external zstd binary,
// writing the compressed result to out.
func tarToZstd(srcDir string, out io.Writer, onRead func(cumulativeBytes int64)) error {
	cmd := exec.Command("zstd", "-q")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("zstd stdin pipe: %w", err)
	}
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	tarErr := tarTo(srcDir, stdin, onRead)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	// Check waitErr first: if zstd itself exited with an error, that's the
	// root cause and its stderr is the only place that explains why -- a
	// tarErr in this situation is just a symptom (zstd closed the pipe
	// early, so the in-progress write into it failed with a broken pipe).
	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	if tarErr != nil {
		return tarErr
	}
	if closeErr != nil {
		return fmt.Errorf("close zstd stdin: %w", closeErr)
	}
	return nil
}

// tarTo writes a tar stream of srcDir's contents to w. The root directory
// itself is not included as an entry, only its contents (with paths
// relative to srcDir) — the caller controls wrapping by choosing what
// srcDir points at. If onRead is non-nil, it is invoked as file bytes are
// read, with the running cumulative total across all files.
func tarTo(srcDir string, w io.Writer, onRead func(cumulativeBytes int64)) error {
	tw := tar.NewWriter(w)
	var cumulative int64

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Fusion creates a "<file>.lck" directory (holding a per-process
		// "M<pid>.lck" file) next to any .vmx or .vmdk currently open.
		// srcDir here is the staged copy made by copyDir from the live
		// bundle while the VM was still running, mid-copy (see Run's doc
		// comment) and before the snapshot merge -- so a lock directory
		// can still be present in this staged copy even though tarTo
		// itself only ever walks it afterward, on the static, already-
		// copied bundle. That lock state is process-specific and
		// meaningless once copied elsewhere -- worse, restoring it
		// verbatim leaves a stale lock directory that makes
		// vmware-vdiskmanager -e mistake the restored copy for one another
		// process already has open, failing the post-extraction disk
		// consistency check over nothing (confirmed real case, 2026-09-11).
		// Skip it entirely rather than archive dead weight that actively
		// breaks restore.
		if d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".lck") {
			return fs.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(tw, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			cumulative += n
			if onRead != nil {
				onRead(cumulative)
			}
		}
		return nil
	})

	closeErr := tw.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}
