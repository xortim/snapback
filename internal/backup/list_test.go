package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

func writeTestManifest(t *testing.T, destination, archiveID string, m Manifest) {
	t.Helper()
	dir := filepath.Join(destination, archiveID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestListArchives_ReturnsManifestsNewestFirst(t *testing.T) {
	destination := t.TempDir()
	older := Manifest{VMName: "myvm", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), SizeBytes: 100}
	newer := Manifest{VMName: "myvm", Timestamp: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), SizeBytes: 200}
	writeTestManifest(t, destination, "myvm-20260101T000000Z", older)
	writeTestManifest(t, destination, "myvm-20260601T000000Z", newer)

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 2 {
		t.Fatalf("len(archives) = %d, want 2", len(archives))
	}
	if archives[0].ArchiveID != "myvm-20260601T000000Z" || archives[1].ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("archives = %+v, want newest first", archives)
	}
}

func TestSortArchives_TiesBreakOnArchiveID(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	archives := []Archive{
		{ArchiveID: "zeta-20260101T000000Z", Manifest: Manifest{VMName: "zeta", Timestamp: ts}},
		{ArchiveID: "alpha-20260101T000000Z", Manifest: Manifest{VMName: "alpha", Timestamp: ts}},
	}

	sortArchives(archives)

	if archives[0].ArchiveID != "alpha-20260101T000000Z" || archives[1].ArchiveID != "zeta-20260101T000000Z" {
		t.Errorf("archives = [%s, %s], want alpha before zeta for equal timestamps", archives[0].ArchiveID, archives[1].ArchiveID)
	}
}

func TestListArchives_SkipsDirectoriesWithoutManifest(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	// Simulates a run that died before Checksumming wrote manifest.json --
	// see choreography.go's succeeded/defer cleanup comment.
	orphan := filepath.Join(destination, "myvm-20260202T000000Z")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (orphan dir skipped)", len(archives))
	}
	if archives[0].ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("archives[0].ArchiveID = %q, want %q", archives[0].ArchiveID, "myvm-20260101T000000Z")
	}
}

func TestListArchives_IgnoresNonDirectoryEntries(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	if err := os.WriteFile(filepath.Join(destination, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 1 {
		t.Errorf("len(archives) = %d, want 1 (stray file ignored)", len(archives))
	}
}

func TestListArchives_MissingDestinationReturnsEmptyNotError(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "does-not-exist-yet")

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil for a destination that doesn't exist yet", err)
	}
	if len(archives) != 0 {
		t.Errorf("len(archives) = %d, want 0", len(archives))
	}
}

func TestListArchives_EmptyDestinationReturnsError(t *testing.T) {
	if _, err := ListArchives(""); err == nil {
		t.Fatal("ListArchives(\"\") error = nil, want an error for an unconfigured destination")
	}
}

func TestListArchives_SkipsMalformedManifest_ReturnsOtherArchives(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	badDir := filepath.Join(destination, "myvm-20260202T000000Z")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil (malformed manifest skipped, not fatal)", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (only the well-formed archive)", len(archives))
	}
	if archives[0].ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("archives[0].ArchiveID = %q, want %q", archives[0].ArchiveID, "myvm-20260101T000000Z")
	}
}

func TestListArchives_SkipsUnreadableManifest_ReturnsOtherArchives(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	// A manifest.json that's actually a directory can never be read as a
	// file -- this simulates a non-ErrNotExist os.ReadFile failure without
	// relying on platform-specific permission bits.
	badManifestPath := filepath.Join(destination, "myvm-20260202T000000Z", "manifest.json")
	if err := os.MkdirAll(badManifestPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil (unreadable manifest skipped, not fatal)", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (only the well-formed archive)", len(archives))
	}
}

func TestListArchives_PreservesManifestFields(t *testing.T) {
	destination := t.TempDir()
	want := Manifest{
		VMName:      "myvm",
		GuestOS:     "ubuntu-64",
		SizeBytes:   12345,
		Comment:     "nightly",
		Timestamp:   time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		ToolsState:  vm.ToolsRunning,
		SHA256:      "deadbeef",
		Compression: "zstd",
	}
	writeTestManifest(t, destination, "myvm-20260304T050607Z", want)

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1", len(archives))
	}
	got := archives[0].Manifest
	if got.VMName != want.VMName || got.GuestOS != want.GuestOS || got.SizeBytes != want.SizeBytes ||
		got.Comment != want.Comment || !got.Timestamp.Equal(want.Timestamp) || got.ToolsState != want.ToolsState ||
		got.SHA256 != want.SHA256 || got.Compression != want.Compression {
		t.Errorf("archives[0].Manifest = %+v, want %+v", got, want)
	}
}

func TestListArchives_FollowsSymlinkedArchiveDirectory(t *testing.T) {
	destination := t.TempDir()
	realParent := t.TempDir()
	writeTestManifest(t, realParent, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})
	linkPath := filepath.Join(destination, "myvm-20260101T000000Z")
	if err := os.Symlink(filepath.Join(realParent, "myvm-20260101T000000Z"), linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	archives, err := ListArchives(destination)
	if err != nil {
		t.Fatalf("ListArchives() error = %v, want nil", err)
	}
	if len(archives) != 1 {
		t.Fatalf("len(archives) = %d, want 1 (symlinked archive directory followed)", len(archives))
	}
}
