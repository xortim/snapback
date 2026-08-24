package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/vm"
)

var errBoom = errors.New("boom")

func TestRun_HappyPath_ProducesArchiveAndManifest(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	fixedNow := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stagingDir := t.TempDir()
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Comment:     "test backup",
		Destination: t.TempDir(),
		StagingDir:  stagingDir,
		Compression: "gzip",
		Now:         func() time.Time { return fixedNow },
	}

	result, err := backup.Run(fake, opts)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if result.Manifest.VMName != "myvm" {
		t.Errorf("Manifest.VMName = %q, want %q", result.Manifest.VMName, "myvm")
	}
	if result.Manifest.GuestOS != "ubuntu-64" {
		t.Errorf("Manifest.GuestOS = %q, want %q", result.Manifest.GuestOS, "ubuntu-64")
	}
	if result.Manifest.Comment != "test backup" {
		t.Errorf("Manifest.Comment = %q, want %q", result.Manifest.Comment, "test backup")
	}
	if result.Manifest.ToolsState != vm.ToolsRunning {
		t.Errorf("Manifest.ToolsState = %q, want %q", result.Manifest.ToolsState, vm.ToolsRunning)
	}
	if result.Manifest.Compression != "gzip" {
		t.Errorf("Manifest.Compression = %q, want %q", result.Manifest.Compression, "gzip")
	}
	if result.Manifest.SHA256 == "" {
		t.Error("Manifest.SHA256 is empty, want a checksum")
	}
	if result.Manifest.SizeBytes == 0 {
		t.Error("Manifest.SizeBytes = 0, want > 0")
	}
	if !result.Manifest.Timestamp.Equal(fixedNow) {
		t.Errorf("Manifest.Timestamp = %v, want %v", result.Manifest.Timestamp, fixedNow)
	}

	if filepath.Ext(result.ArchivePath) != ".gz" {
		t.Errorf("ArchivePath = %q, want a .tar.gz path", result.ArchivePath)
	}
	if _, err := os.Stat(result.ArchivePath); err != nil {
		t.Errorf("archive file missing: %v", err)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Errorf("manifest file missing: %v", err)
	}

	// Archive should extract back to the original bundle layout:
	// myvm.vmwarevm/{myvm.vmx,disk.vmdk}
	f, err := os.Open(result.ArchivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	found := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		found[hdr.Name] = buf.String()
	}
	if found["myvm.vmwarevm/myvm.vmx"] != "guestOS = \"ubuntu-64\"\n" {
		t.Errorf("archive entry myvm.vmwarevm/myvm.vmx = %q, missing or wrong", found["myvm.vmwarevm/myvm.vmx"])
	}
	if found["myvm.vmwarevm/disk.vmdk"] != "fake disk contents" {
		t.Errorf("archive entry myvm.vmwarevm/disk.vmdk = %q, missing or wrong", found["myvm.vmwarevm/disk.vmdk"])
	}

	snapshots, err := fake.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after a successful run (snapshot merged back)", snapshots)
	}

	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("ReadDir(StagingDir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("StagingDir contains %v, want empty after a successful run", entries)
	}
}

func TestRun_NonRunningToolsState_RecordsCrashConsistent(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsNotInstalled

	result, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if result.Manifest.ToolsState != vm.ToolsNotInstalled {
		t.Errorf("Manifest.ToolsState = %q, want %q (crash-consistent, recorded not skipped)", result.Manifest.ToolsState, vm.ToolsNotInstalled)
	}
}

func TestRun_CheckToolsStateError_AbortsBeforeSnapshot(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsStateErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; no snapshot should have been taken", snapshots)
	}
}

func TestRun_SnapshotError_Aborts(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestRun_ListSnapshotsError_Aborts(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.ListSnapshotsErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestRun_DeleteSnapshotError_StillCleansUpStaging(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	stagingDir := t.TempDir()
	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  stagingDir,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	entries, rdErr := os.ReadDir(stagingDir)
	if rdErr != nil {
		t.Fatalf("ReadDir(stagingDir) error = %v", rdErr)
	}
	if len(entries) != 0 {
		t.Errorf("stagingDir contains %v, want empty even after a failed run", entries)
	}
}

// TestRun_CopyDirPartialFailure_CleansUpStagingDir is a regression test for
// a bug where the staging-directory cleanup defer was registered AFTER the
// copyDir call it was meant to guard. copyDir creates (and starts
// populating) stagingRoot before it can fail partway through, so if
// copyDir errors, the defer must already be armed or a partially-copied
// staging directory (potentially containing VM disk data) is orphaned on
// the host filesystem forever.
//
// The failure is forced deterministically, without relying on permission
// checks (which root bypasses in some CI environments): a regular file is
// pre-planted at the exact path copyDir will try to os.MkdirAll for a
// subdirectory entry ("subdir"). MkdirAll always errors when a
// non-directory file already exists at that path -- that's an ENOTDIR
// condition, not a permission check, so it fails the same way whether the
// test runs as root or not. "disk.vmdk" and "myvm.vmx" sort before
// "subdir" lexically, so copyDir's walk copies both of them successfully
// before hitting the conflict, reproducing the "partially-populated
// staging directory" the review finding described.
func TestRun_CopyDirPartialFailure_CleansUpStagingDir(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(srcBundle, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "subdir", "inner.txt"), []byte("inner"), 0o644); err != nil {
		t.Fatalf("write inner file: %v", err)
	}

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	fixedNow := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stagingDir := t.TempDir()

	// Pre-compute the exact staging root Run() will use -- same formula as
	// choreography.go: archiveID = "<vmname>-<ts>", stagingRoot =
	// "<StagingDir>/snapback-staging-<archiveID>" -- then plant a regular
	// file at the path copyDir will try to MkdirAll for "subdir".
	ts := fixedNow.Format("20060102T150405Z")
	archiveID := "myvm-" + ts
	stagingRoot := filepath.Join(stagingDir, "snapback-staging-"+archiveID)
	stagedBundle := filepath.Join(stagingRoot, "myvm.vmwarevm")
	conflictPath := filepath.Join(stagedBundle, "subdir")
	if err := os.MkdirAll(stagedBundle, 0o755); err != nil {
		t.Fatalf("mkdir staged bundle: %v", err)
	}
	if err := os.WriteFile(conflictPath, []byte("conflict"), 0o644); err != nil {
		t.Fatalf("write conflict file: %v", err)
	}

	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  stagingDir,
		Compression: "gzip",
		Now:         func() time.Time { return fixedNow },
	}

	_, err := backup.Run(fake, opts)
	if err == nil {
		t.Fatal("Run() error = nil, want error from copyDir failing on the conflicting subdir entry")
	}

	if _, statErr := os.Stat(stagingRoot); !os.IsNotExist(statErr) {
		t.Errorf("staging root %q still exists after failed Run() (statErr=%v), want removed (os.Stat err = %v)", stagingRoot, statErr, statErr)
	}
}

func writeMinimalVMX(t *testing.T) string {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	path := filepath.Join(bundle, "myvm.vmx")
	if err := os.WriteFile(path, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	return path
}
