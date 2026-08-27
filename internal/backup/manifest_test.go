package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

func TestWriteManifest_WritesReadableJSON(t *testing.T) {
	m := Manifest{
		VMName:      "dev-ubuntu",
		GuestOS:     "ubuntu-64",
		SizeBytes:   1024,
		Comment:     "nightly auto-backup",
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ToolsState:  vm.ToolsRunning,
		SHA256:      "deadbeef",
		Compression: "zstd",
	}
	path := filepath.Join(t.TempDir(), "manifest.json")

	if err := writeManifest(path, m); err != nil {
		t.Fatalf("writeManifest() error = %v, want nil", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}
	if got.VMName != m.VMName || got.GuestOS != m.GuestOS || got.SizeBytes != m.SizeBytes ||
		got.Comment != m.Comment || !got.Timestamp.Equal(m.Timestamp) || got.ToolsState != m.ToolsState ||
		got.SHA256 != m.SHA256 || got.Compression != m.Compression {
		t.Errorf("round-tripped manifest = %+v, want %+v", got, m)
	}
}

func TestSHA256File_MatchesKnownDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File() error = %v, want nil", err)
	}
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Errorf("sha256File() = %q, want %q", got, want)
	}
}

func TestSHA256File_MissingFileReturnsError(t *testing.T) {
	_, err := sha256File(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("sha256File() error = nil, want error for missing file")
	}
}
