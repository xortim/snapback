package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
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

	result, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)
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

	// Independently recompute the archive's checksum and size rather than
	// trusting the values Run() reports about its own output, and confirm
	// manifest.json on disk actually matches result.Manifest.
	archiveBytes, err := os.ReadFile(result.ArchivePath)
	if err != nil {
		t.Fatalf("ReadFile(ArchivePath) error = %v", err)
	}
	sum := sha256.Sum256(archiveBytes)
	wantSHA256 := hex.EncodeToString(sum[:])
	if result.Manifest.SHA256 != wantSHA256 {
		t.Errorf("Manifest.SHA256 = %q, want independently computed %q", result.Manifest.SHA256, wantSHA256)
	}

	archiveInfo, err := os.Stat(result.ArchivePath)
	if err != nil {
		t.Fatalf("Stat(ArchivePath) error = %v", err)
	}
	if result.Manifest.SizeBytes != archiveInfo.Size() {
		t.Errorf("Manifest.SizeBytes = %d, want %d (os.Stat)", result.Manifest.SizeBytes, archiveInfo.Size())
	}

	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatalf("ReadFile(ManifestPath) error = %v", err)
	}
	var onDiskManifest backup.Manifest
	if err := json.Unmarshal(manifestBytes, &onDiskManifest); err != nil {
		t.Fatalf("json.Unmarshal(manifest.json) error = %v", err)
	}
	if onDiskManifest.VMName != result.Manifest.VMName ||
		onDiskManifest.GuestOS != result.Manifest.GuestOS ||
		onDiskManifest.SizeBytes != result.Manifest.SizeBytes ||
		onDiskManifest.Comment != result.Manifest.Comment ||
		!onDiskManifest.Timestamp.Equal(result.Manifest.Timestamp) ||
		onDiskManifest.ToolsState != result.Manifest.ToolsState ||
		onDiskManifest.SHA256 != result.Manifest.SHA256 ||
		onDiskManifest.Compression != result.Manifest.Compression {
		t.Errorf("manifest.json on disk = %+v, want %+v", onDiskManifest, result.Manifest)
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

// vmxDeletingController wraps vm.FakeVMController and deletes the vmx
// file as a side effect of a successful Snapshot call. This is a
// deterministic, root-proof way to detect an ordering regression: it
// does not depend on permission bits (which root bypasses in some CI
// environments -- see TestRun_CopyDirPartialFailure_CleansUpStagingDir
// above), only on whether readGuestOS is called before or after
// ctrl.Snapshot.
type vmxDeletingController struct {
	*vm.FakeVMController
}

func (c *vmxDeletingController) Snapshot(vmxPath, name string) error {
	if err := c.FakeVMController.Snapshot(vmxPath, name); err != nil {
		return err
	}
	return os.Remove(vmxPath)
}

func TestRun_ReadsGuestOSBeforeSnapshot(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	ctrl := &vmxDeletingController{FakeVMController: fake}

	result, err := backup.Run(t.Context(), ctrl, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil; guestOS must be read from the vmx file before Snapshot() runs (this test's fake Snapshot deletes the vmx file afterward to catch ordering regressions)", err)
	}
	if result.Manifest.GuestOS != "ubuntu-64" {
		t.Errorf("Manifest.GuestOS = %q, want %q", result.Manifest.GuestOS, "ubuntu-64")
	}
}

// cancelingController wraps vm.FakeVMController and cancels ctx as a side
// effect of a successful CheckToolsState call, so a test can deterministically
// land ctx's cancellation in the window between CheckToolsState returning and
// Snapshot being called -- the exact window choreography.go:105-108 checks.
type cancelingController struct {
	*vm.FakeVMController
	cancel context.CancelFunc
}

func (c *cancelingController) CheckToolsState(vmxPath string) (vm.ToolsState, error) {
	state, err := c.FakeVMController.CheckToolsState(vmxPath)
	c.cancel()
	return state, err
}

func TestRun_ContextCanceledAfterCheckToolsState_StageBelowSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	ctx, cancel := context.WithCancel(t.Context())
	ctrl := &cancelingController{FakeVMController: fake, cancel: cancel}

	_, err := backup.Run(ctx, ctrl, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for context canceled after CheckToolsState")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// No snapshot exists yet at this point -- Stage must be below
	// Snapshotting, or a caller's orphan-cleanup pointer wrongly fires for
	// a snapshot that was never created.
	if runErr.Stage >= progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want < %v (canceled before Snapshot() ran, nothing to orphan)", runErr.Stage, progress.Snapshotting)
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; canceled before Snapshot() should have run", snapshots)
	}
}

func TestRun_NonRunningToolsState_RecordsCrashConsistent(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsNotInstalled

	result, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
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

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
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

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
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

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
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
	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
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

// TestRun_CreateArchiveError_RemovesOutputDir is a regression test for a bug
// where outputDir (Destination/<archiveID>) was created via os.MkdirAll but
// never removed if a later step (archive creation, rename, stat, checksum,
// or manifest write) failed. That left a phantom Destination/<archiveID>
// directory -- containing a stray archive.tmp or a complete archive with no
// manifest.json -- that would confuse `list`/`prune`/`status`, which scan
// Destination for backups. The failure here is forced by passing an unknown
// Compression value, which createArchive rejects after outputDir already
// exists on disk.
func TestRun_CreateArchiveError_RemovesOutputDir(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	fixedNow := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	destination := t.TempDir()
	archiveID := "myvm-" + fixedNow.Format("20060102T150405Z")
	outputDir := filepath.Join(destination, archiveID)

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: destination,
		StagingDir:  t.TempDir(),
		Compression: "bogus-unknown-compression",
		Now:         func() time.Time { return fixedNow },
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error from createArchive rejecting unknown compression")
	}

	if _, statErr := os.Stat(outputDir); !os.IsNotExist(statErr) {
		t.Errorf("outputDir %q still exists after failed Run() (statErr=%v), want removed", outputDir, statErr)
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

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)
	if err == nil {
		t.Fatal("Run() error = nil, want error from copyDir failing on the conflicting subdir entry")
	}

	if _, statErr := os.Stat(stagingRoot); !os.IsNotExist(statErr) {
		t.Errorf("staging root %q still exists after failed Run() (statErr=%v), want removed (os.Stat err = %v)", stagingRoot, statErr, statErr)
	}
}

func TestRun_EmptyVMName_ReturnsError(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for empty VMName")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; validation should fail before touching ctrl", snapshots)
	}
}

func TestRun_PathTraversalVMName_ReturnsError(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "../escape",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for VMName containing path traversal")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; validation should fail before touching ctrl", snapshots)
	}
}

func TestRun_EmptyDestination_ReturnsError(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: "",
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for empty Destination")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; validation should fail before touching ctrl", snapshots)
	}
}

func TestRun_LockAlreadyHeld_ReturnsErrorWithoutTouchingVM(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}

	destination := t.TempDir()
	lock, err := backup.AcquireLock(destination, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: destination,
	}

	_, err = backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts)
	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run() error = %v, want a *RunError", err)
	}
	if runErr.Stage != progress.CheckingTools {
		t.Errorf("RunError.Stage = %v, want %v", runErr.Stage, progress.CheckingTools)
	}
	if !errors.Is(err, backup.ErrLocked) {
		t.Errorf("Run() error = %v, want it to wrap backup.ErrLocked", err)
	}
	snapshots, lerr := fake.ListSnapshots(vmxPath)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want no snapshot taken while the lock was held", snapshots)
	}
}

func TestRun_HappyPath_ReleasesLockForReacquisition(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "disk.vmdk"), []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	destination := t.TempDir()
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: destination,
		StagingDir:  t.TempDir(),
	}

	if _, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, opts); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	lock, err := backup.AcquireLock(destination, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after Run() error = %v, want nil (Run must release its lock)", err)
	}
	_ = lock.Release()
}

func TestRun_MissingVMXPath_ReturnsError(t *testing.T) {
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     filepath.Join(t.TempDir(), "does-not-exist.vmx"),
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for missing VMXPath")
	}
}

func TestRun_VMXPathIsDirectory_ReturnsError(t *testing.T) {
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	vmxAsDir := t.TempDir() // exists, but is a directory -- a misconfigured VMXPath

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxAsDir,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for VMXPath pointing at a directory")
	}
	if !strings.HasPrefix(err.Error(), "vmx path") {
		t.Errorf("Run() error = %q, want it prefixed with %q from pre-flight validation, not a low-level file-read error surfacing later", err.Error(), "vmx path")
	}

	snapshots, _ := fake.ListSnapshots(vmxAsDir)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; validation should fail before touching ctrl", snapshots)
	}
}

func TestRun_CompressionAuto_PrefersZstdWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}

	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	result, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if result.Manifest.Compression != "zstd" {
		t.Errorf("Manifest.Compression = %q, want %q", result.Manifest.Compression, "zstd")
	}
	if !strings.HasSuffix(result.ArchivePath, ".tar.zst") {
		t.Errorf("ArchivePath = %q, want it to end with %q", result.ArchivePath, ".tar.zst")
	}
}

func TestRun_OutputPermissions_MatchStagingHardening(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	result, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	outputInfo, err := os.Stat(result.OutputDir)
	if err != nil {
		t.Fatalf("Stat(OutputDir) error = %v", err)
	}
	if perm := outputInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("OutputDir perm = %o, want %o (matching copyDir's staging-directory hardening)", perm, 0o700)
	}

	archiveInfo, err := os.Stat(result.ArchivePath)
	if err != nil {
		t.Fatalf("Stat(ArchivePath) error = %v", err)
	}
	if perm := archiveInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("ArchivePath perm = %o, want %o", perm, 0o600)
	}

	manifestInfo, err := os.Stat(result.ManifestPath)
	if err != nil {
		t.Fatalf("Stat(ManifestPath) error = %v", err)
	}
	if perm := manifestInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("ManifestPath perm = %o, want %o", perm, 0o600)
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

type fakeReporter struct {
	events []progress.Event
}

func (r *fakeReporter) Report(e progress.Event) {
	r.events = append(r.events, e)
}

func TestRun_HappyPath_ReportsStagesInOrder(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	reporter := &fakeReporter{}

	_, err := backup.Run(t.Context(), fake, reporter, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	wantOrder := []progress.Stage{
		progress.CheckingTools,
		progress.Snapshotting,
		progress.Copying,
		progress.Merging,
		progress.Compressing,
		progress.Checksumming,
		progress.Done,
	}
	var gotOrder []progress.Stage
	seen := map[progress.Stage]bool{}
	for _, e := range reporter.events {
		if !seen[e.Stage] {
			seen[e.Stage] = true
			gotOrder = append(gotOrder, e.Stage)
		}
	}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("distinct stages reported = %v, want %v", gotOrder, wantOrder)
	}
	for i, stage := range wantOrder {
		if gotOrder[i] != stage {
			t.Errorf("stage[%d] = %v, want %v (full order: %v)", i, gotOrder[i], stage, gotOrder)
		}
	}
}

func TestRun_CanceledContext_AbortsBeforeAnyCtrlCall(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := backup.Run(ctx, fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error for a pre-canceled context")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	if runErr.Stage != progress.CheckingTools {
		t.Errorf("runErr.Stage = %v, want %v (canceled before any ctrl call)", runErr.Stage, progress.CheckingTools)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, want true")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; a pre-canceled context must abort before touching ctrl", snapshots)
	}
}

func TestRun_SnapshotError_RunErrorStageBelowSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = errBoom

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// Snapshot itself failed, so no snapshot was ever created -- Stage
	// must NOT be >= Snapshotting, or a caller would wrongly point the
	// user at `snapback cleanup` for a snapshot that doesn't exist.
	if runErr.Stage >= progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want < %v (Snapshot itself failed, nothing to orphan)", runErr.Stage, progress.Snapshotting)
	}
}

func TestRun_SnapshotError_OrphanPossible_RunErrorStageAtOrAboveSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = fmt.Errorf("cleanup failed: %w", vm.ErrOrphanPossible)

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// Cleanup couldn't be confirmed, so a snapshot may actually remain --
	// Stage must be >= Snapshotting so a caller's orphan check fires and
	// points the user at `snapback cleanup`.
	if runErr.Stage < progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want >= %v (cleanup could not be confirmed)", runErr.Stage, progress.Snapshotting)
	}
}

func TestRun_DeleteSnapshotError_RunErrorStageAtOrAboveSnapshotting(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	_, err := backup.Run(t.Context(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	var runErr *backup.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	// The snapshot was taken successfully and DeleteSnapshot (the merge)
	// is what failed -- a snapshot may still exist on the source VM, so
	// Stage must be >= Snapshotting to trigger the cleanup pointer.
	if runErr.Stage < progress.Snapshotting {
		t.Errorf("runErr.Stage = %v, want >= %v (snapshot exists, merge failed)", runErr.Stage, progress.Snapshotting)
	}
}

func TestRun_NilReporter_DoesNotPanic(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := backup.Run(t.Context(), fake, nil, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (a nil reporter should default to a no-op, not panic)", err)
	}
}
