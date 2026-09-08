package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// SnapshotPrefix names the snapshot Run takes during the choreography
// (SnapshotPrefix + timestamp) -- exported so internal/cli's cleanup
// command can recognize the same orphaned-snapshot naming pattern
// without duplicating the literal.
const SnapshotPrefix = "snapback-"

// Options configures a single backup run.
type Options struct {
	VMName      string
	VMXPath     string
	Comment     string
	Destination string           // parent directory the backup's output directory is created under
	StagingDir  string           // parent directory for the temporary staging copy; os.TempDir() if empty
	Compression string           // "zstd", "gzip", or "" (prefer zstd, fall back to gzip)
	Now         func() time.Time // defaults to time.Now if nil
}

// Result describes a completed backup.
type Result struct {
	ArchiveID    string
	OutputDir    string
	ArchivePath  string
	ManifestPath string
	Manifest     Manifest
}

// checkDisksConsistent reads every virtual disk device configured in
// vmxPath and runs ctrl.CheckDiskConsistency against each one's current
// top-level file, returning the first failure (wrapped with that disk's
// filename for context) or nil once every disk passes.
//
// Only meaningful -- and only safe to call -- while the VM is not
// running: a live vmware-vmx process holds an exclusive lock on its disk
// files, so the check can't open them at all (confirmed against a real
// running VM: it fails on lock contention, not a real consistency
// verdict). That's not a limitation worth working around, since a
// running VM's disk chain is already known-good by construction --
// vmware-vmx itself successfully opened the whole chain to get to
// "running" in the first place. Both of this function's call sites in
// Run gate on the same toolsState != vm.ToolsRunning check the rest of
// the choreography already uses to decide crash-consistent vs. quiesced.
func checkDisksConsistent(ctrl vm.Controller, vmxPath string) error {
	bundleDir := filepath.Dir(vmxPath)
	diskFiles, err := readDiskFiles(vmxPath)
	if err != nil {
		return fmt.Errorf("read vmx disk config: %w", err)
	}
	for _, diskFile := range diskFiles {
		if err := ctrl.CheckDiskConsistency(filepath.Join(bundleDir, diskFile)); err != nil {
			return fmt.Errorf("%s: %w", diskFile, err)
		}
	}
	return nil
}

// checkCtx returns a *RunError tagged with stage if ctx is done, or nil
// otherwise. Centralizes the ctx.Err() check Run performs at each stage
// boundary so the only thing that varies per call site is which Stage to
// tag -- see the design doc's discussion of this exact copy-paste risk.
func checkCtx(ctx context.Context, stage progress.Stage) *RunError {
	if err := ctx.Err(); err != nil {
		return &RunError{Stage: stage, Err: err}
	}
	return nil
}

// Run executes the snapshot -> copy -> merge -> archive choreography
// described in docs/design.md ("Backup choreography") against ctrl,
// reporting progress.Events to reporter at each stage. The source VM is
// never paused: ctrl.Snapshot freezes the disk state, the bundle is
// copied while the VM keeps running against a fresh delta, and
// ctrl.DeleteSnapshot merges that delta back once the copy is safely
// staged.
//
// For a VM that isn't running (toolsState != vm.ToolsRunning), Run also
// verifies the disk chain's consistency via ctrl.CheckDiskConsistency
// both before Snapshot (catching a chain that's already damaged, before
// piling a new snapshot on top of it) and again right after DeleteSnapshot
// (catching a merge vmcli reported as successful but didn't actually
// apply). Both checks are skipped for a running VM: its disk files are
// held open by the live vmware-vmx process, so the check can't run at
// all, and doesn't need to -- the chain is already known-good by the
// fact that it's running. See checkDisksConsistent's doc comment.
//
// ctx is checked between stages, not during an in-flight ctrl call --
// vm.Controller's methods don't take a context, so a call already in
// progress runs to completion regardless of cancellation (see ADR-003,
// docs/superpowers/specs/2026-08-27-run-progress-context-design.md).
//
// Every error Run returns is a *RunError carrying the Stage active when
// it occurred, so a caller can tell whether a snapshot may have been
// left orphaned on the source VM (Stage >= progress.Snapshotting) --
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback here. Once DeleteSnapshot succeeds
// (Stage: Merging and later), no snapshot actually remains even though
// Stage is still >= Snapshotting -- the caller's cleanup pointer becomes
// a harmless no-op in that case rather than a precise signal, which is
// an accepted simplification (see ADR-003's Risks section).
func Run(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts Options) (*Result, error) {
	if reporter == nil {
		reporter = progress.NoOpReporter{}
	}

	// Stage: CheckingTools -- pre-flight validation, readGuestOS, and
	// ctrl.CheckToolsState. No ctrl call has happened yet, so any error
	// here means there is no snapshot to orphan.
	if runErr := checkCtx(ctx, progress.CheckingTools); runErr != nil {
		return nil, runErr
	}
	if opts.VMName == "" || filepath.Base(opts.VMName) != opts.VMName {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("invalid VMName %q", opts.VMName)}
	}
	if opts.Destination == "" {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("destination is required")}
	}

	lock, err := AcquireLock(opts.Destination, opts.VMName)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("acquire backup lock: %w", err)}
	}
	defer func() { _ = lock.Release() }()

	vmxInfo, err := os.Stat(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("vmx path: %w", err)}
	}
	if vmxInfo.IsDir() {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("vmx path %q is a directory, want a regular file", opts.VMXPath)}
	}

	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("read guest OS: %w", err)}
	}

	bundleDir := filepath.Dir(opts.VMXPath)
	totalBytes, err := dirSize(bundleDir)
	if err != nil {
		// Tagged CheckingTools, not Copying -- dirSize runs here, before
		// ctrl.CheckToolsState and well before ctrl.Snapshot, so no ctrl
		// call has happened yet and there is nothing to orphan. Same
		// reasoning as the pre-Snapshot ctx.Err() check below. ADR-003
		// (docs/superpowers/specs/2026-08-27-run-progress-context-design.md)
		// originally said Copying, written before readGuestOS/dirSize were
		// moved ahead of ctrl.Snapshot to shrink the orphaned-snapshot
		// window (commit 6e2d19f); an earlier fix implemented that stale
		// text literally, which this corrects.
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("measure bundle size: %w", err)}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	startTime := now().UTC()
	ts := startTime.Format("20060102T150405Z")
	archiveID := fmt.Sprintf("%s-%s", opts.VMName, ts)
	snapshotName := SnapshotPrefix + ts

	reporter.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking VMware Tools state"})
	toolsState, err := ctrl.CheckToolsState(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("check tools state: %w", err)}
	}

	// Preflight disk consistency check -- powered-off VMs only (see
	// checkDisksConsistent's doc comment for why running VMs skip this).
	// Added after a real incident (docs/design.md's "Risks & Gotchas"):
	// this VM's disk chain already had a latent defect before this run
	// even started, and nothing caught it until the VM failed to power on
	// afterward. Catching it here, before a new snapshot is piled on top
	// of an already-broken chain, is strictly better than catching it
	// only after the fact.
	if toolsState != vm.ToolsRunning {
		reporter.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking disk consistency"})
		if err := checkDisksConsistent(ctrl, opts.VMXPath); err != nil {
			return nil, &RunError{Stage: progress.CheckingTools, Err: fmt.Errorf("disk consistency check: %w", err)}
		}
	}

	// Checked here, before Snapshot() runs, but tagged CheckingTools (not
	// Snapshotting) -- no snapshot exists yet, so a caller's Stage >=
	// progress.Snapshotting orphan check must not fire for one. Same
	// reasoning as the real Snapshot() failure handled a few lines below.
	if runErr := checkCtx(ctx, progress.CheckingTools); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot " + snapshotName})
	if err := ctrl.Snapshot(opts.VMXPath, snapshotName); err != nil {
		// Snapshot() itself failed. Ordinarily that means no snapshot was
		// ever created, so this is tagged below progress.Snapshotting -- a
		// caller's Stage >= progress.Snapshotting orphan check must not
		// wrongly point at `snapback cleanup` for a snapshot that doesn't
		// exist. But if the implementation couldn't confirm cleanup
		// succeeded (vm.ErrOrphanPossible), a partial snapshot may actually
		// remain, so this is tagged at Snapshotting instead so that check
		// does fire.
		stage := progress.CheckingTools
		if errors.Is(err, vm.ErrOrphanPossible) {
			stage = progress.Snapshotting
		}
		return nil, &RunError{Stage: stage, Err: fmt.Errorf("snapshot: %w", err)}
	}

	snapshots, err := ctrl.ListSnapshots(opts.VMXPath)
	if err != nil {
		return nil, &RunError{Stage: progress.Snapshotting, Err: fmt.Errorf("list snapshots: %w", err)}
	}
	if !slices.Contains(snapshots, snapshotName) {
		return nil, &RunError{Stage: progress.Snapshotting, Err: fmt.Errorf("snapshot %q not found after creation", snapshotName)}
	}

	// Stage: Copying
	if runErr := checkCtx(ctx, progress.Copying); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"})
	hostSync()

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingRoot := filepath.Join(stagingParent, "snapback-staging-"+archiveID)
	stagedBundle := filepath.Join(stagingRoot, filepath.Base(bundleDir))
	defer func() { _ = os.RemoveAll(stagingRoot) }()
	onCopy := func(cumulativeBytes int64) {
		reporter.Report(progress.Event{Stage: progress.Copying, Percent: percentOf(cumulativeBytes, totalBytes)})
	}
	if err := copyDir(bundleDir, stagedBundle, onCopy); err != nil {
		return nil, &RunError{Stage: progress.Copying, Err: fmt.Errorf("copy bundle: %w", err)}
	}

	// Stage: Merging
	if runErr := checkCtx(ctx, progress.Merging); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"})
	if err := ctrl.DeleteSnapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, &RunError{Stage: progress.Merging, Err: fmt.Errorf("delete snapshot: %w", err)}
	}

	// Post-merge disk consistency check -- same toolsState gate and same
	// real incident as the preflight check above, but this is the one
	// that would actually have caught it: DeleteSnapshot reported success
	// while the merge silently failed to apply, and nothing downstream of
	// it re-verified that before archiving the result and reporting the
	// backup complete. A failure here does not mean the staged copy is
	// bad -- copyDir ran before the merge, against the still-frozen
	// pre-merge snapshot -- but the source VM itself may now be damaged,
	// which is exactly what must not be reported as a quiet success.
	if toolsState != vm.ToolsRunning {
		reporter.Report(progress.Event{Stage: progress.Merging, Message: "verifying disk consistency after merge"})
		if err := checkDisksConsistent(ctrl, opts.VMXPath); err != nil {
			return nil, &RunError{Stage: progress.Merging, Err: fmt.Errorf("post-merge disk consistency check failed -- the merge may not have completed correctly and the source VM's snapshot chain could be damaged; inspect it (e.g. `vmware-vdiskmanager -e`) before trusting it or backing it up again: %w", err)}
		}
	}

	// Stage: Compressing
	outputDir := filepath.Join(opts.Destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("create output dir: %w", err)}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(outputDir)
		}
	}()

	if runErr := checkCtx(ctx, progress.Compressing); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Compressing, Message: "compressing archive"})
	tempArchivePath := filepath.Join(outputDir, "archive.tmp")
	onRead := func(cumulativeBytes int64) {
		reporter.Report(progress.Event{Stage: progress.Compressing, Percent: percentOf(cumulativeBytes, totalBytes)})
	}
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression, onRead)
	if err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("create archive: %w", err)}
	}
	ext := "tar.gz"
	if usedCompression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	if err := os.Rename(tempArchivePath, archivePath); err != nil {
		return nil, &RunError{Stage: progress.Compressing, Err: fmt.Errorf("rename archive: %w", err)}
	}

	// Stage: Checksumming
	if runErr := checkCtx(ctx, progress.Checksumming); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Checksumming, Message: "checksumming archive"})
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: fmt.Errorf("stat archive: %w", err)}
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: err}
	}

	manifest := Manifest{
		VMName:      opts.VMName,
		GuestOS:     guestOS,
		SizeBytes:   info.Size(),
		Comment:     opts.Comment,
		Timestamp:   startTime,
		ToolsState:  toolsState,
		SHA256:      sum,
		Compression: usedCompression,
	}
	manifestPath := filepath.Join(outputDir, "manifest.json")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, &RunError{Stage: progress.Checksumming, Err: err}
	}

	succeeded = true
	reporter.Report(progress.Event{Stage: progress.Done, Message: "backup complete"})
	return &Result{
		ArchiveID:    archiveID,
		OutputDir:    outputDir,
		ArchivePath:  archivePath,
		ManifestPath: manifestPath,
		Manifest:     manifest,
	}, nil
}

// percentOf returns cumulative/total as a fraction in [0,1], or 0 if
// total is 0 (an empty bundle -- avoids a division by zero). Clamped at
// 1.0: totalBytes is measured before ctrl.Snapshot() runs, but the
// snapshot adds delta/.vmsn files to the bundle that the later Copying
// stage also counts, so cumulative can exceed total on a real VM.
func percentOf(cumulative, total int64) float64 {
	if total == 0 {
		return 0
	}
	if cumulative >= total {
		return 1
	}
	return float64(cumulative) / float64(total)
}

// hostSync flushes the host page cache before copy.go reads the frozen
// disk files. Best-effort only: the snapshot already guarantees the
// source files are frozen at the VMware layer, so a failure here is not
// fatal — it just removes a class of doubt per docs/design.md.
func hostSync() {
	_ = exec.Command("sync").Run()
}
