package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

var errRestoreBoom = errors.New("boom")

// buildFixtureArchive writes a small fake .vmwarevm bundle, archives it
// with a real createArchive call (round-tripping the same code path Run
// uses, per ADR-004's testing note, rather than hand-crafting tar bytes),
// and writes a matching manifest.json under destination/<archiveID>/. It
// returns the archiveID and the Manifest actually written, so tests can
// mutate and rewrite it (e.g. to force a checksum mismatch).
func buildFixtureArchive(t *testing.T, destination, vmName, compression string) (archiveID string, m Manifest) {
	t.Helper()

	bundleName := vmName + ".vmwarevm"
	stagingRoot := t.TempDir()
	stagedBundle := filepath.Join(stagingRoot, bundleName)
	if err := os.MkdirAll(stagedBundle, 0o700); err != nil {
		t.Fatalf("mkdir staged bundle: %v", err)
	}
	vmxContent := "guestOS = \"ubuntu-64\"\nscsi0:0.fileName = \"disk.vmdk\"\n"
	if err := os.WriteFile(filepath.Join(stagedBundle, vmName+".vmx"), []byte(vmxContent), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	archiveID = vmName + "-20260911T120000Z"
	outputDir := filepath.Join(destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("mkdir output dir: %v", err)
	}
	ext := "tar.gz"
	if compression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	usedCompression, err := createArchive(stagingRoot, archivePath, compression, nil)
	if err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}

	m = Manifest{
		VMName:      vmName,
		GuestOS:     "ubuntu-64",
		SizeBytes:   info.Size(),
		Timestamp:   time.Now().UTC(),
		ToolsState:  vm.ToolsRunning,
		SHA256:      sum,
		Compression: usedCompression,
	}
	if err := writeManifest(filepath.Join(outputDir, "manifest.json"), m); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return archiveID, m
}

func TestRestore_HappyPath_PlacesRestoredBundle(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	targetParent := t.TempDir()

	opts := RestoreOptions{
		ArchiveID:   archiveID,
		Destination: destination,
		TargetDir:   targetParent,
		Now:         func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) },
	}

	result, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	if err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}
	wantPath := filepath.Join(targetParent, "myvm - backup 2026-09-11.vmwarevm")
	if result.TargetPath != wantPath {
		t.Errorf("TargetPath = %q, want %q", result.TargetPath, wantPath)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "myvm.vmx")); err != nil {
		t.Errorf("restored vmx missing at %s: %v", wantPath, err)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "disk.vmdk")); err != nil {
		t.Errorf("restored disk missing at %s: %v", wantPath, err)
	}
}

func TestRestore_ChecksumMismatch_FailsBeforeExtracting(t *testing.T) {
	destination := t.TempDir()
	archiveID, m := buildFixtureArchive(t, destination, "myvm", "gzip")
	m.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"[:64]
	if err := writeManifest(filepath.Join(destination, archiveID, "manifest.json"), m); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	stagingParent := t.TempDir()
	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); !os.IsNotExist(statErr) {
		t.Errorf("staging dir %s exists, want it never created on a checksum failure", stagingDir)
	}
}

func TestRestore_DiskConsistencyFailure_PreservesStagingDir(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	stagingParent := t.TempDir()
	ctrl := vm.NewFakeVMController()
	ctrl.DiskConsistencyErr = errRestoreBoom

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), ctrl, progress.NoOpReporter{}, opts)

	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.CheckingDiskConsistency {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.CheckingDiskConsistency)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); statErr != nil {
		t.Errorf("staging dir %s missing, want it preserved for inspection: %v", stagingDir, statErr)
	}
}

func TestRestore_TargetCollision_AppendsNumericSuffix(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	targetParent := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }

	if err := os.MkdirAll(filepath.Join(targetParent, "myvm - backup 2026-09-11.vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create collision target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetParent, "myvm - backup 2026-09-11 (2).vmwarevm"), 0o700); err != nil {
		t.Fatalf("pre-create second collision target: %v", err)
	}

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: targetParent, Now: now}
	result, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	if err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}
	want := filepath.Join(targetParent, "myvm - backup 2026-09-11 (3).vmwarevm")
	if result.TargetPath != want {
		t.Errorf("TargetPath = %q, want %q", result.TargetPath, want)
	}
}

func TestRestore_UnresolvableArchiveID_ReturnsErrorBeforeIO(t *testing.T) {
	destination := t.TempDir()
	opts := RestoreOptions{ArchiveID: "does-not-exist", Destination: destination, TargetDir: t.TempDir()}
	if _, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts); err == nil {
		t.Fatal("Restore() error = nil, want an error for an unresolvable archive ID")
	}
}

func TestRestore_MissingTargetDirAndVMXPath_ReturnsValidationError(t *testing.T) {
	opts := RestoreOptions{ArchiveID: "whatever", Destination: t.TempDir()}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
}

func TestRestore_BothTargetDirAndVMXPath_ReturnsValidationError(t *testing.T) {
	opts := RestoreOptions{ArchiveID: "whatever", Destination: t.TempDir(), TargetDir: "/a", VMXPath: "/b/myvm.vmx"}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	var restoreErr *RestoreError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RestoreError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
}

func TestResult_Summary(t *testing.T) {
	r := &Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}
	want := "backup complete: /dest/myvm-x/archive.tar.zst"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestRestoreResult_Summary(t *testing.T) {
	r := &RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}
	want := "restore complete: /vms/myvm - backup 2026-09-11.vmwarevm"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestRestoreError_FailedStage(t *testing.T) {
	err := &RestoreError{Stage: progress.Extracting, Err: errRestoreBoom}
	if got := err.FailedStage(); got != progress.Extracting {
		t.Errorf("FailedStage() = %v, want %v", got, progress.Extracting)
	}
	if !errors.Is(error(err), errRestoreBoom) {
		t.Error("errors.Is(err, errRestoreBoom) = false, want true")
	}
}
