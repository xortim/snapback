package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateArchive_GzipWhenZstdUnavailable(t *testing.T) {
	restore := lookZstd
	lookZstd = func() (string, error) { return "", errors.New("not found") }
	defer func() { lookZstd = restore }()

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "gzip" {
		t.Errorf("createArchive() compression = %q, want %q", used, "gzip")
	}
	assertTarGzContains(t, destPath, "file.txt", "hello")
}

func TestCreateArchive_ExplicitGzipIgnoresZstdAvailability(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "gzip")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "gzip" {
		t.Errorf("createArchive() compression = %q, want %q", used, "gzip")
	}
}

func TestCreateArchive_UsesZstdWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "zstd")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "zstd" {
		t.Errorf("createArchive() compression = %q, want %q", used, "zstd")
	}

	decompressed, err := exec.Command("zstd", "-d", "-c", destPath).Output()
	if err != nil {
		t.Fatalf("zstd -d error = %v", err)
	}
	assertTarContains(t, bytes.NewReader(decompressed), "file.txt", "hello")
}

func TestCreateArchive_UnknownCompressionReturnsError(t *testing.T) {
	srcDir := t.TempDir()
	destPath := filepath.Join(t.TempDir(), "archive.out")

	_, err := createArchive(srcDir, destPath, "bogus")
	if err == nil {
		t.Fatal("createArchive() error = nil, want error for unknown compression")
	}
}

func assertTarGzContains(t *testing.T, path, wantName, wantContent string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gz.Close() }()

	assertTarContains(t, gz, wantName, wantContent)
}

func assertTarContains(t *testing.T, r io.Reader, wantName, wantContent string) {
	t.Helper()
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("tar entry %q not found (reader error: %v)", wantName, err)
		}
		if hdr.Name != wantName {
			continue
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatalf("read tar entry %q: %v", wantName, err)
		}
		if buf.String() != wantContent {
			t.Errorf("tar entry %q content = %q, want %q", wantName, buf.String(), wantContent)
		}
		return
	}
}
