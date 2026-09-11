package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	if err := extractArchive(archivePath, destDir, "gzip", func(cumulative int64) { calls = append(calls, cumulative) }); err != nil {
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
	if err := extractArchive(archivePath, destDir, "zstd", nil); err != nil {
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
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a pre-existing destDir")
	}
}

func TestExtractArchive_CorruptedArchive_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not a valid gzip stream"), 0o644); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error for a corrupted archive")
	}
}

func TestExtractArchive_UnknownCompression_ReturnsError(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.out")
	if err := os.WriteFile(archivePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "bogus", nil); err == nil {
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
	if err := extractArchive(archivePath, destDir, "gzip", nil); err == nil {
		t.Fatal("extractArchive() error = nil, want a path-traversal rejection")
	}
	if _, err := os.Stat(filepath.Join(destParent, "escape.txt")); !os.IsNotExist(err) {
		t.Errorf("escape.txt exists outside destDir, want it never written")
	}
	_ = bytes.NewReader // keep bytes imported for future assertions in this file
}
