package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractArchive_GzipRoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	var calls []int64
	if err := extractArchive(archivePath, destDir, "gzip", 0, func(cumulative int64) { calls = append(calls, cumulative) }); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}

	gotA, err := os.ReadFile(filepath.Join(destDir, "a.txt"))
	if err != nil || string(gotA) != "hello" {
		t.Errorf("a.txt = %q, %v, want %q, nil", gotA, err, "hello")
	}
	gotB, err := os.ReadFile(filepath.Join(destDir, "sub", "b.txt"))
	if err != nil || string(gotB) != "world" {
		t.Errorf("sub/b.txt = %q, %v, want %q, nil", gotB, err, "world")
	}
	if len(calls) == 0 {
		t.Error("onWrite was never called, want at least one call")
	}
}

func TestExtractArchive_ZstdRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.zst")
	if _, err := createArchive(srcDir, archivePath, "zstd", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "zstd", 0, nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("a.txt = %q, %v, want %q, nil", got, err, "hello")
	}
}

func TestExtractArchive_DestDirAlreadyExists_ReturnsError(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := t.TempDir() // already exists
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a pre-existing destDir")
	}
}

func TestExtractArchive_CorruptedArchive_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not a valid gzip stream"), 0o644); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a corrupted archive")
	}
}

func TestExtractArchive_UnknownCompression_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.out")
	if err := os.WriteFile(archivePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "bogus", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for unknown compression")
	}
}

func TestExtractArchive_PathTraversalEntry_IsRejected(t *testing.T) {
	// Hand-craft a tar.gz with a "../escape.txt" entry -- extractArchive
	// must reject this rather than write outside destDir.
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.txt", Size: int64(len(content)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want a path-traversal rejection")
	}
	if _, err := os.Stat(filepath.Join(destParent, "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("escape.txt exists outside destDir, want it never written")
	}
	_ = bytes.NewReader // keep bytes imported for future assertions in this file
}

func TestExtractArchive_PathTraversalEntryEndingInLck_IsStillRejected(t *testing.T) {
	// A traversal entry that happens to end in ".lck" must still be
	// rejected by the traversal guard, not silently dropped by the
	// Fusion-lock-directory skip (the two checks must not race).
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.lck", Size: int64(len(content)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want a path-traversal rejection")
	}
	if _, err := os.Stat(filepath.Join(destParent, "escape.lck")); !os.IsNotExist(err) {
		t.Errorf("escape.lck exists outside destDir, want it never written")
	}
}

func TestExtractArchive_RegularFileSetuidBit_IsMaskedOnExtraction(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "setuid.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "setuid.bin", Size: int64(len(content)), Mode: 0o4755, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}
	info, err := os.Stat(filepath.Join(destDir, "setuid.bin"))
	if err != nil {
		t.Fatalf("stat extracted file: %v", err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Errorf("extracted file mode = %v, want setuid bit stripped", info.Mode())
	}
}

func TestExtractArchive_ExcludesFusionLockDirectories(t *testing.T) {
	// An archive created before createArchive started excluding Fusion's
	// "<file>.lck" lock directories -- extraction must still drop them so
	// old archives restore cleanly (see hasLockDirComponent's doc comment).
	archivePath := filepath.Join(t.TempDir(), "with-lock-dir.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name     string
		typeflag byte
		content  string
	}{
		{"disk.vmdk", tar.TypeReg, "disk contents"},
		{"disk.vmdk.lck/", tar.TypeDir, ""},
		{"disk.vmdk.lck/M12345.lck", tar.TypeReg, "stale lock"},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Size: int64(len(e.content)), Mode: 0o644, Typeflag: e.typeflag}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if e.content != "" {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("write tar content %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "disk.vmdk")); err != nil {
		t.Errorf("disk.vmdk missing, want it extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "disk.vmdk.lck")); !os.IsNotExist(err) {
		t.Errorf("disk.vmdk.lck exists (err=%v), want it excluded from extraction", err)
	}
}

func TestExtractArchive_PlainFileEndingInLck_IsNotExcluded(t *testing.T) {
	// A regular file that isn't a Fusion lock directory (or nested under
	// one) but merely has a name ending in ".lck" is something
	// createArchive would include -- extraction must not drop it just
	// because of the suffix, matching createArchive's directory-only
	// exclusion.
	archivePath := filepath.Join(t.TempDir(), "with-lck-file.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("not a lock directory")
	if err := tw.WriteHeader(&tar.Header{Name: "notes.lck", Size: int64(len(content)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "notes.lck"))
	if err != nil || string(got) != "not a lock directory" {
		t.Errorf("notes.lck = %q, %v, want %q, nil", got, err, "not a lock directory")
	}
}

func TestExtractArchive_SymlinkAbsoluteTarget_IsRejected(t *testing.T) {
	// Hand-craft a tar.gz with a symlink whose target is absolute (e.g. /etc/passwd).
	// extractArchive must reject this rather than create it.
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "link.txt", Linkname: "/etc/passwd", Mode: 0o644, Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want rejection of absolute symlink target")
	}
	if _, err := os.Stat(filepath.Join(destDir, "link.txt")); !os.IsNotExist(err) {
		t.Errorf("link.txt was created, want it never written")
	}
}

func TestExtractArchive_SymlinkRelativeEscapeTarget_IsRejected(t *testing.T) {
	// Hand-craft a tar.gz with a symlink whose relative target uses ../ to escape.
	// extractArchive must reject this rather than create it.
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "subdir/link.txt", Linkname: "../../escape.txt", Mode: 0o644, Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want rejection of relative-escape symlink target")
	}
	if _, err := os.Stat(filepath.Join(destParent, "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("escape.txt exists outside destDir, want it never written")
	}
}

func TestExtractArchive_ZstdCorruptedArchive_NoDeadlock(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}

	// Create a zstd-compressed archive with several MB of content, then corrupt
	// a byte partway through to trigger a truncated/garbled tar body. Extract
	// should return within a timeout, not deadlock.
	srcDir := t.TempDir()
	// Write 5MB of data to exceed typical pipe buffer (~64KB).
	if err := os.WriteFile(filepath.Join(srcDir, "large.bin"), make([]byte, 5*1024*1024), 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.zst")
	if _, err := createArchive(srcDir, archivePath, "zstd", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	// Corrupt a byte partway through the compressed file.
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(data) > 0 {
		// Flip a bit at ~30% into the file to corrupt the compressed stream.
		corruptIdx := len(data) / 3
		data[corruptIdx] ^= 0xFF
	}
	if err := os.WriteFile(archivePath, data, 0o644); err != nil {
		t.Fatalf("write corrupted archive: %v", err)
	}

	// Extract in a goroutine with a timeout to detect deadlock.
	destDir := filepath.Join(t.TempDir(), "extracted")
	done := make(chan error, 1)
	go func() {
		done <- extractArchive(archivePath, destDir, "zstd", 0, nil)
	}()

	select {
	case err := <-done:
		// Expected to get an error from the corrupted archive.
		if err == nil {
			t.Error("extractArchive() error = nil, want an error for corrupted zstd archive")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("extractArchive() deadlocked on corrupted zstd archive (>10s timeout)")
	}
}

func TestCopyCapped_UnderBudget_CopiesEverythingNoLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 1024)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if limitHit {
		t.Error("copyCapped() limitHit = true, want false (well under budget)")
	}
	if written != 11 || dst.String() != "hello world" {
		t.Errorf("copyCapped() = (%d, %q), want (11, %q)", written, dst.String(), "hello world")
	}
}

func TestCopyCapped_ExceedsBudget_ReportsLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello world")) // 11 bytes
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 5)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if !limitHit {
		t.Error("copyCapped() limitHit = false, want true (source exceeds budget)")
	}
	if written != 6 {
		t.Errorf("copyCapped() written = %d, want 6 (budget + 1 byte to detect overflow)", written)
	}
}

func TestCopyCapped_ExactlyAtBudget_NoLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello")) // exactly 5 bytes
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 5)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if limitHit {
		t.Error("copyCapped() limitHit = true, want false (source exactly fills budget, no more)")
	}
	if written != 5 || dst.String() != "hello" {
		t.Errorf("copyCapped() = (%d, %q), want (5, %q)", written, dst.String(), "hello")
	}
}

func TestExtractArchive_DecompressionExceedsExpectedSize_ReturnsError(t *testing.T) {
	// Simulate the exact bomb scenario: a checksum-valid archive whose
	// actual decompressed content is far larger than what the manifest
	// (or here, the expectedUncompressedBytes the caller passes straight
	// through from Manifest.UncompressedSizeBytes) says it should be.
	srcDir := t.TempDir()
	// 256KB of a repeating byte -- highly compressible, so the archive on
	// disk stays tiny, but large relative to the tiny expected size below.
	payload := bytes.Repeat([]byte{0x42}, 256*1024)
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	// expectedUncompressedBytes=100 -- far below the 256KB payload, well
	// beyond decompressionSlackBytes (64KB) tolerance too.
	err := extractArchive(archivePath, destDir, "gzip", 100, nil)
	if err == nil {
		t.Fatal("extractArchive() error = nil, want an error for exceeding expected uncompressed size")
	}
	// The cap must abort mid-copy, not after writing the whole 256KB
	// payload -- that's the whole point of checking incrementally rather
	// than comparing sizes afterward. os.ReadDir is non-recursive, which
	// suffices for this fixture's single flat file.
	if _, statErr := os.Stat(destDir); statErr == nil {
		entries, readErr := os.ReadDir(destDir)
		if readErr != nil {
			t.Fatalf("ReadDir(destDir): %v", readErr)
		}
		var total int64
		for _, e := range entries {
			info, infoErr := e.Info()
			if infoErr != nil {
				t.Fatalf("Info(%s): %v", e.Name(), infoErr)
			}
			total += info.Size()
		}
		// 100 (the expected size passed above) + the slack + one tar block
		// of margin, covering the single-byte overshoot copyCapped
		// deliberately permits to detect the overrun.
		const wantMax = 100 + decompressionSlackBytes + 512
		if total > wantMax {
			t.Errorf("extraction wrote %d bytes to %s, want <= %d -- the cap must abort mid-copy, not after writing the full %d-byte payload", total, destDir, wantMax, len(payload))
		}
	}
}

func TestExtractArchive_LegitimateHighCompressionRatio_NotFalselyCapped(t *testing.T) {
	// A large all-zero file compresses extremely well (a VM disk's sparse
	// regions do too) -- when expectedUncompressedBytes reflects the real
	// size (as Manifest.UncompressedSizeBytes would), this must NOT be
	// mistaken for a bomb despite the huge compression ratio.
	srcDir := t.TempDir()
	payload := make([]byte, 2*1024*1024) // 2MB of zeros
	if err := os.WriteFile(filepath.Join(srcDir, "sparse.vmdk"), payload, 0o644); err != nil {
		t.Fatalf("write sparse.vmdk: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	// The real uncompressed size (2MB of payload; tar adds negligible
	// header overhead well within decompressionSlackBytes).
	if err := extractArchive(archivePath, destDir, "gzip", int64(len(payload)), nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil (legitimate high-ratio archive within expected size)", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "sparse.vmdk"))
	if err != nil || len(got) != len(payload) {
		t.Errorf("sparse.vmdk length = %d, err=%v, want %d, nil", len(got), err, len(payload))
	}
}

func TestExtractArchive_FallbackCap_NoExpectedSize_StillCatchesBomb(t *testing.T) {
	// expectedUncompressedBytes=0 simulates restoring an archive whose
	// manifest predates UncompressedSizeBytes -- extractArchive must fall
	// back to fallbackDecompressionMultiplier against the compressed
	// archive's own on-disk size, and that fallback cap must still be
	// enforced (not skipped just because there's no ground truth).
	srcDir := t.TempDir()
	// A repeating byte through Go's default-level gzip converges to a
	// compression ratio of only ~509:1 (verified empirically), not the
	// "far below" ratio a naive reading of fallbackDecompressionMultiplier
	// might suggest -- deflate's match length is capped at 258 bytes, so
	// there's a hard ceiling regardless of payload size. 1MB isn't enough
	// margin above that ceiling for the self-check below to reliably pass
	// (it doesn't, ratio ~489:1 at that size); 64MB clears it with ~2%
	// headroom, which is as good as this fixture shape gets.
	payload := bytes.Repeat([]byte{0x7A}, 64*1024*1024)
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size()*fallbackDecompressionMultiplier >= int64(len(payload)) {
		t.Fatalf("test fixture invalid: compressed size %d * %d = %d, want it below payload size %d (adjust payload size if this fires)",
			info.Size(), fallbackDecompressionMultiplier, info.Size()*fallbackDecompressionMultiplier, len(payload))
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error from the fallback cap")
	}
}
