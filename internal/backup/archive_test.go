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
	"strings"
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

func TestCreateArchive_ZstdFailureIncludesStderr(t *testing.T) {
	// Put a fake "zstd" binary at the front of PATH that always fails and
	// writes a distinctive message to stderr, so we can assert that
	// message surfaces in the error returned by createArchive rather than
	// being silently discarded.
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "zstd")
	script := "#!/bin/sh\necho 'zstd: forced test failure detail xyz123' >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture script needs to be executable
		t.Fatalf("write fake zstd script: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	_, err := createArchive(srcDir, destPath, "zstd")
	if err == nil {
		t.Fatal("createArchive() error = nil, want error from failing zstd")
	}
	const wantSubstr = "forced test failure detail xyz123"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("createArchive() error = %q, want it to contain fake zstd's stderr output %q", err.Error(), wantSubstr)
	}
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
