package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreTargetName_NoCollision(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11.vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_OneCollision_AppendsSuffix(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision: %v", err)
	}
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11 (2).vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_TwoCollisions_AppendsNextSuffix(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision 1: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11 (2).vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision 2: %v", err)
	}
	got, err := restoreTargetName(parent, "myvm", now)
	if err != nil {
		t.Fatalf("restoreTargetName() error = %v, want nil", err)
	}
	want := "myvm - backup 2026-09-11 (3).vmwarevm"
	if got != want {
		t.Errorf("restoreTargetName() = %q, want %q", got, want)
	}
}

func TestRestoreTargetName_ExhaustsAttempts_ReturnsError(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(parent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create base: %v", err)
	}
	for n := 2; n <= 100; n++ {
		name := filepath.Join(parent, filepath_sprintf(n))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatalf("pre-create (%d): %v", n, err)
		}
	}
	_, err := restoreTargetName(parent, "myvm", now)
	if err == nil {
		t.Fatal("restoreTargetName() error = nil, want an error once every attempt up to 100 collides")
	}
}

func filepath_sprintf(n int) string {
	return "myvm - backup 2026-09-11 (" + itoa(n) + ").vmwarevm"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestFindArchive_ResolvesByExactID(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm"})

	archive, err := FindArchive(destination, "myvm-20260101T000000Z")
	if err != nil {
		t.Fatalf("FindArchive() error = %v, want nil", err)
	}
	if archive.ArchiveID != "myvm-20260101T000000Z" {
		t.Errorf("ArchiveID = %q, want %q", archive.ArchiveID, "myvm-20260101T000000Z")
	}
}

func TestFindArchive_UnresolvableID_ReturnsError(t *testing.T) {
	destination := t.TempDir()
	if _, err := FindArchive(destination, "does-not-exist"); err == nil {
		t.Fatal("FindArchive() error = nil, want an error for an unresolvable archive ID")
	}
}

func TestLatestArchiveForVM_ReturnsNewest(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "myvm-20260101T000000Z", Manifest{VMName: "myvm", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	writeTestManifest(t, destination, "myvm-20260601T000000Z", Manifest{VMName: "myvm", Timestamp: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	writeTestManifest(t, destination, "other-20260901T000000Z", Manifest{VMName: "other", Timestamp: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})

	archive, err := LatestArchiveForVM(destination, "myvm")
	if err != nil {
		t.Fatalf("LatestArchiveForVM() error = %v, want nil", err)
	}
	if archive.ArchiveID != "myvm-20260601T000000Z" {
		t.Errorf("ArchiveID = %q, want the newer myvm archive", archive.ArchiveID)
	}
}

func TestLatestArchiveForVM_NoArchivesForVM_ReturnsError(t *testing.T) {
	destination := t.TempDir()
	writeTestManifest(t, destination, "other-20260101T000000Z", Manifest{VMName: "other"})

	if _, err := LatestArchiveForVM(destination, "myvm"); err == nil {
		t.Fatal("LatestArchiveForVM() error = nil, want an error when no archive matches the VM name")
	}
}
