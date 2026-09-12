package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	vmxContent := "guestOS = \"ubuntu-64\"\nscsi0:0.fileName = \"disk.vmdk\"\n"
	return buildFixtureArchiveVMX(t, destination, vmName, compression, vmxContent, true)
}

// buildFixtureArchiveVMX is buildFixtureArchive with the .vmx content and
// whether to write a disk.vmdk file made explicit -- lets tests build a
// fixture whose .vmx has no parseable disk device (writeDisk false), to
// exercise Restore's empty-disk-list guard.
func buildFixtureArchiveVMX(t *testing.T, destination, vmName, compression, vmxContent string, writeDisk bool) (archiveID string, m Manifest) {
	t.Helper()

	bundleName := vmName + ".vmwarevm"
	stagingRoot := t.TempDir()
	stagedBundle := filepath.Join(stagingRoot, bundleName)
	if err := os.MkdirAll(stagedBundle, 0o700); err != nil {
		t.Fatalf("mkdir staged bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedBundle, vmName+".vmx"), []byte(vmxContent), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if writeDisk {
		if err := os.WriteFile(filepath.Join(stagedBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
			t.Fatalf("write disk: %v", err)
		}
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

	ctrl := vm.NewFakeVMController()
	result, err := Restore(context.Background(), ctrl, progress.NoOpReporter{}, opts)
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
	if len(ctrl.DiskConsistencyCalls) != 1 {
		t.Fatalf("DiskConsistencyCalls = %v, want exactly 1 call", ctrl.DiskConsistencyCalls)
	}
	wantSuffix := filepath.Join("myvm.vmwarevm", "disk.vmdk")
	if !strings.HasSuffix(ctrl.DiskConsistencyCalls[0], wantSuffix) {
		t.Errorf("DiskConsistencyCalls[0] = %q, want suffix %q", ctrl.DiskConsistencyCalls[0], wantSuffix)
	}
}

// TestRestore_SetsUUIDActionCreate covers the fix for VMware Fusion's
// "this virtual machine may have been moved or copied" prompt: Restore
// always places a copy at a brand-new path, so uuid.action = "create" must
// be written into the restored .vmx to make Fusion silently answer "I
// Copied It" (fresh UUID + MAC) instead of asking, the first time the
// restored bundle is opened.
func TestRestore_SetsUUIDActionCreate(t *testing.T) {
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
	vmxContents, err := os.ReadFile(filepath.Join(result.TargetPath, "myvm.vmx"))
	if err != nil {
		t.Fatalf("read restored vmx: %v", err)
	}
	if !strings.Contains(string(vmxContents), `uuid.action = "create"`) {
		t.Errorf("restored vmx = %q, want it to contain uuid.action = \"create\"", vmxContents)
	}
}

// TestRestore_VMXPathSuccessPath_PlacesBesideSourceVM exercises the
// opts.VMXPath branch (as opposed to opts.TargetDir): when the caller
// knows the source VM's vmx path but not an explicit target directory
// (the common case -- restoring back beside the VM's original location
// resolved from config, without passing --dest), targetParent is derived
// as VMXPath's grandparent -- filepath.Dir(filepath.Dir(vmxPath)) --
// since a .vmx file always lives directly inside its .vmwarevm bundle
// directory, which itself lives directly inside the parent directory the
// restored copy should be placed beside.
func TestRestore_VMXPathSuccessPath_PlacesBesideSourceVM(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	parentDir := t.TempDir()
	vmxPath := filepath.Join(parentDir, "myvm.vmwarevm", "myvm.vmx")
	now := func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }

	opts := RestoreOptions{
		ArchiveID:   archiveID,
		Destination: destination,
		VMXPath:     vmxPath,
		Now:         now,
	}

	result, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	if err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}
	wantPath := filepath.Join(parentDir, "myvm - backup 2026-09-11.vmwarevm")
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

// TestRestore_VMXPathNotInBundle_ReturnsClearError exercises the opts.VMXPath
// branch when the configured vmx path isn't nested directly inside a
// ".vmwarevm" bundle directory -- config.Load only validates VMX is
// non-empty, so a hand-edited or legacy config entry like "/vms/dev2.vmx"
// must be rejected with a clear error instead of silently deriving
// targetParent as "/" via filepath.Dir(filepath.Dir(vmxPath)).
func TestRestore_VMXPathNotInBundle_ReturnsClearError(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	vmxPath := filepath.Join(t.TempDir(), "flatdir", "dev2.vmx")

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, VMXPath: vmxPath}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
	if !strings.Contains(restoreErr.Error(), ".vmwarevm") {
		t.Errorf("Error() = %q, want it to mention .vmwarevm", restoreErr.Error())
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

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); !os.IsNotExist(statErr) {
		t.Errorf("staging dir %s exists, want it never created on a checksum failure", stagingDir)
	}
}

// TestRestore_ExtractionFailure_CleansUpStagingDir corrupts the archive
// file's bytes in place after the fixture is built, then rewrites the
// manifest's checksum to match the corrupted bytes -- so verifyChecksum
// (now hashFile) passes (it hashes whatever is actually on disk) but
// extractArchive fails immediately, since the corrupted bytes aren't
// valid gzip. Before the fix, the staging dir extractArchive creates
// (MkdirAll happens before the gzip header is read) leaked forever on this
// path, since the cleanup defer was registered after the failing call --
// permanently blocking any retry of the same archive ID with
// extractArchive's "already exists" error.
func TestRestore_ExtractionFailure_CleansUpStagingDir(t *testing.T) {
	destination := t.TempDir()
	archiveID, m := buildFixtureArchive(t, destination, "myvm", "gzip")

	archivePath := filepath.Join(destination, archiveID, "archive.tar.gz")
	garbage := []byte("not a valid gzip stream")
	if err := os.WriteFile(archivePath, garbage, 0o644); err != nil {
		t.Fatalf("corrupt archive: %v", err)
	}
	sum, err := sha256File(archivePath)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	m.SHA256 = sum
	if err := writeManifest(filepath.Join(destination, archiveID, "manifest.json"), m); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	stagingParent := t.TempDir()
	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err = Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Extracting {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Extracting)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); !os.IsNotExist(statErr) {
		t.Errorf("staging dir %s exists, want it cleaned up on extraction failure so a retry doesn't hit \"already exists\"", stagingDir)
	}
}

// restoreCancelingController wraps vm.FakeVMController and cancels ctx as
// a side effect of a successful CheckDiskConsistency call -- mirrors
// choreography_test.go's cancelingController, but for Restore's one ctrl
// call, deterministically landing cancellation in the window between
// CheckingDiskConsistency succeeding and the Placing-stage checkCtx check.
type restoreCancelingController struct {
	*vm.FakeVMController
	cancel context.CancelFunc
}

func (c *restoreCancelingController) CheckDiskConsistency(diskPath string) error {
	err := c.FakeVMController.CheckDiskConsistency(diskPath)
	c.cancel()
	return err
}

// TestRestore_ContextCanceledAfterDiskCheck_PreservesStagingDir exercises
// the Placing-stage checkCtx call: ctx is canceled only after extraction
// and disk-consistency verification have both already succeeded, so the
// extracted copy is real, verified work -- it must be preserved for
// inspection like every other post-extraction failure, not silently
// discarded by the deferred cleanup just because the failure happened to
// be a cancellation instead of a returned error.
func TestRestore_ContextCanceledAfterDiskCheck_PreservesStagingDir(t *testing.T) {
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "gzip")
	stagingParent := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	ctrl := &restoreCancelingController{FakeVMController: vm.NewFakeVMController(), cancel: cancel}

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(ctx, ctrl, progress.NoOpReporter{}, opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore() error = %v, want it to wrap context.Canceled", err)
	}
	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Placing {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Placing)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); statErr != nil {
		t.Errorf("staging dir %s missing, want it preserved for inspection after a post-extraction cancellation: %v", stagingDir, statErr)
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

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.CheckingDiskConsistency {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.CheckingDiskConsistency)
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); statErr != nil {
		t.Errorf("staging dir %s missing, want it preserved for inspection: %v", stagingDir, statErr)
	}
}

func TestRestore_NoDisksFound_FailsAndPreservesStagingDir(t *testing.T) {
	destination := t.TempDir()
	vmxContent := "guestOS = \"ubuntu-64\"\n" // no scsiN:N.fileName device line
	archiveID, _ := buildFixtureArchiveVMX(t, destination, "myvm", "gzip", vmxContent, false)
	stagingParent := t.TempDir()

	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.CheckingDiskConsistency {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.CheckingDiskConsistency)
	}
	if !strings.Contains(restoreErr.Error(), "no virtual disks") {
		t.Errorf("Error() = %q, want it to mention \"no virtual disks\"", restoreErr.Error())
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archiveID)
	if _, statErr := os.Stat(stagingDir); statErr != nil {
		t.Errorf("staging dir %s missing, want it preserved for inspection: %v", stagingDir, statErr)
	}
}

func TestRestore_AbsoluteDiskPath_FailsInsteadOfCheckingWrongFile(t *testing.T) {
	destination := t.TempDir()
	vmxContent := "guestOS = \"ubuntu-64\"\nscsi0:0.fileName = \"/Volumes/external/myvm.vmdk\"\n"
	archiveID, _ := buildFixtureArchiveVMX(t, destination, "myvm", "gzip", vmxContent, false)
	stagingParent := t.TempDir()

	fake := vm.NewFakeVMController()
	opts := RestoreOptions{ArchiveID: archiveID, Destination: destination, TargetDir: t.TempDir(), StagingDir: stagingParent}
	_, err := Restore(context.Background(), fake, progress.NoOpReporter{}, opts)

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.CheckingDiskConsistency {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.CheckingDiskConsistency)
	}
	if !strings.Contains(restoreErr.Error(), "outside the .vmwarevm bundle") {
		t.Errorf("Error() = %q, want it to mention the disk is outside the bundle", restoreErr.Error())
	}
	if len(fake.DiskConsistencyCalls) != 0 {
		t.Errorf("CheckDiskConsistency called with %v, want no calls -- absolute path must be rejected before checking anything", fake.DiskConsistencyCalls)
	}
}

func TestRestore_TargetParentDoesNotExist_FailsBeforeArchiveLookup(t *testing.T) {
	destination := t.TempDir()
	missingParent := filepath.Join(t.TempDir(), "does-not-exist")

	opts := RestoreOptions{ArchiveID: "does-not-exist-either", Destination: destination, TargetDir: missingParent}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)

	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
	// If the target-parent check didn't fire first, the next thing to fail
	// would be FindArchive with a "no archive" style error rather than one
	// naming the missing target directory -- assert on the message to
	// prove ordering, not just the stage.
	if !strings.Contains(restoreErr.Error(), missingParent) {
		t.Errorf("Error() = %q, want it to name the missing target parent %q (proves the check ran before FindArchive)", restoreErr.Error(), missingParent)
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
	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
	}
	if restoreErr.Stage != progress.Verifying {
		t.Errorf("Stage = %v, want %v", restoreErr.Stage, progress.Verifying)
	}
}

func TestRestore_BothTargetDirAndVMXPath_ReturnsValidationError(t *testing.T) {
	opts := RestoreOptions{ArchiveID: "whatever", Destination: t.TempDir(), TargetDir: "/a", VMXPath: "/b/myvm.vmx"}
	_, err := Restore(context.Background(), vm.NewFakeVMController(), progress.NoOpReporter{}, opts)
	var restoreErr *RunError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("Restore() error = %v, want a *RunError", err)
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

func TestRestoreResult_NextSteps_MentionsTargetPath(t *testing.T) {
	r := &RestoreResult{TargetPath: "/vms/myvm - backup 2026-09-11.vmwarevm"}
	if got := r.NextSteps(); !strings.Contains(got, r.TargetPath) {
		t.Errorf("NextSteps() = %q, want it to mention %q", got, r.TargetPath)
	}
}

func TestResult_NextSteps_IsEmpty(t *testing.T) {
	r := &Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}
	if got := r.NextSteps(); got != "" {
		t.Errorf("NextSteps() = %q, want empty (a backup needs no follow-up)", got)
	}
}

func TestRestore_ZstdCompressedArchive_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}
	destination := t.TempDir()
	archiveID, _ := buildFixtureArchive(t, destination, "myvm", "zstd")
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
		t.Errorf("restored vmx missing: %v", err)
	}
}

func TestPlaceBundle_CrossDeviceFallback_CopiesAndRemovesSource(t *testing.T) {
	original := renameFile
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	defer func() { renameFile = original }()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "placed")

	if err := placeBundle(src, dst); err != nil {
		t.Fatalf("placeBundle() error = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("dst content = %q, %v, want %q, nil", got, err, "hello")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("src %s still exists after successful cross-device placement, want it removed", src)
	}
}

func TestPlaceBundle_CrossDeviceCopyFails_RemovesPartialTarget(t *testing.T) {
	original := renameFile
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	defer func() { renameFile = original }()

	// A src that doesn't exist makes copyDir fail immediately (its
	// filepath.WalkDir root stat fails), simulating a cross-device copy
	// that dies without depending on filesystem-permission quirks that
	// behave differently when tests run as root.
	src := filepath.Join(t.TempDir(), "does-not-exist")
	dst := filepath.Join(t.TempDir(), "placed")
	if err := os.MkdirAll(filepath.Join(dst, "partial"), 0o700); err != nil {
		t.Fatalf("pre-create partial dst: %v", err)
	}

	if err := placeBundle(src, dst); err == nil {
		t.Fatal("placeBundle() error = nil, want an error when copyDir fails")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("dst %s exists after a failed cross-device copy, want it removed", dst)
	}
}

func TestRestore_ErrorIsRunError_FailedStage(t *testing.T) {
	err := &RunError{Stage: progress.Extracting, Err: errRestoreBoom}
	if got := err.FailedStage(); got != progress.Extracting {
		t.Errorf("FailedStage() = %v, want %v", got, progress.Extracting)
	}
	if !errors.Is(error(err), errRestoreBoom) {
		t.Error("errors.Is(err, errRestoreBoom) = false, want true")
	}
}
