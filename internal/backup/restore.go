package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

// RestoreOptions configures a single restore.
type RestoreOptions struct {
	ArchiveID   string
	Archive     *Archive         // if set, used directly instead of resolving ArchiveID via FindArchive -- lets a caller that already resolved the archive (e.g. internal/cli, which needs it up front to infer --dest) pass it straight through instead of triggering a second archive-directory scan, which also closes the window where a second, independent resolution could observe a different (or since-deleted) archive than the first. ArchiveID is ignored when this is set.
	Destination string           // parent directory archives live under (cfg.Destination)
	TargetDir   string           // parent directory to place the restored bundle in; mutually exclusive with VMXPath
	VMXPath     string           // source VM's vmx path, if known; mutually exclusive with TargetDir
	StagingDir  string           // parent directory for the temporary extraction; os.TempDir() if empty
	Now         func() time.Time // defaults to time.Now if nil; drives the "backup yyyy-mm-dd" suffix
}

// RestoreResult describes a completed restore.
type RestoreResult struct {
	ArchiveID  string
	TargetPath string // the placed .vmwarevm's full path
	Manifest   Manifest
}

// Summary implements internal/tui's pipelineResult interface.
func (r *RestoreResult) Summary() string {
	return fmt.Sprintf("restore complete: %s", r.TargetPath)
}

// NextSteps implements internal/tui's pipelineResult interface. Restore
// never registers the restored bundle with Fusion or opens it (see ADR-004,
// docs/superpowers/specs/2026-09-11-restore-design.md -- Vimalin, a
// comparable third-party VMware backup tool, makes the same choice and
// leaves this entirely to the operator too) -- Fusion has no notion that
// this VM exists until it's opened at least once.
func (r *RestoreResult) NextSteps() string {
	return fmt.Sprintf("next: open %q in Finder (or run `open %q`) to add it to Fusion's VM library -- Fusion's \"Scan for Virtual Machines\" only finds bundles already sitting in its default library folders, so it won't help for a --dest outside those", r.TargetPath, r.TargetPath)
}

// Restore verifies a backup archive against its manifest checksum,
// extracts it, confirms the restored disk chain is consistent, and places
// it as a new, non-destructively-named .vmwarevm bundle -- never
// overwriting the source. See ADR-004
// (docs/superpowers/specs/2026-09-11-restore-design.md) for the full
// design. Restore never touches the source VM -- ctrl is used only for
// CheckDiskConsistency against the restored copy.
//
// Errors are returned as *RunError (shared with Run -- both pipelines tag
// failures with the same progress.Stage vocabulary, so one error type
// suffices for both, see checkCtx's doc comment).
func Restore(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts RestoreOptions) (*RestoreResult, error) {
	if reporter == nil {
		reporter = progress.NoOpReporter{}
	}

	if opts.TargetDir == "" && opts.VMXPath == "" {
		return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("one of TargetDir or VMXPath is required")}
	}
	if opts.TargetDir != "" && opts.VMXPath != "" {
		return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("TargetDir and VMXPath are mutually exclusive")}
	}

	targetParent := opts.TargetDir
	if targetParent == "" {
		vmxDir := filepath.Dir(opts.VMXPath)
		if filepath.Ext(vmxDir) != ".vmwarevm" {
			return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("VMX path %q is not inside a .vmwarevm bundle directory -- pass --dest to restore without relying on the configured VM's layout", opts.VMXPath)}
		}
		targetParent = filepath.Dir(vmxDir)
	}
	if info, statErr := os.Stat(targetParent); statErr != nil {
		return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("restore target parent directory %s: %w", targetParent, statErr)}
	} else if !info.IsDir() {
		return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("restore target parent %s is not a directory", targetParent)}
	}

	if runErr := checkCtx(ctx, progress.Verifying); runErr != nil {
		return nil, runErr
	}

	var archive Archive
	if opts.Archive != nil {
		archive = *opts.Archive
	} else {
		a, err := FindArchive(opts.Destination, opts.ArchiveID)
		if err != nil {
			return nil, &RunError{Stage: progress.Verifying, Err: err}
		}
		archive = a
	}

	archivePath := filepath.Join(opts.Destination, archive.ArchiveID, "archive."+archiveExt(archive.Manifest.Compression))

	reporter.Report(progress.Event{Stage: progress.Verifying, Message: "verifying archive checksum"})
	got, err := hashFile(archivePath, throttledPercentReporter(reporter, progress.Verifying, archive.Manifest.SizeBytes))
	if err != nil {
		return nil, &RunError{Stage: progress.Verifying, Err: err}
	}
	if got != archive.Manifest.SHA256 {
		return nil, &RunError{Stage: progress.Verifying, Err: fmt.Errorf("checksum mismatch for %s: got %s, want %s", archivePath, got, archive.Manifest.SHA256)}
	}

	if runErr := checkCtx(ctx, progress.Extracting); runErr != nil {
		return nil, runErr
	}

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+archive.ArchiveID)

	// keepStaging starts false so a failed extraction (nothing worth
	// inspecting yet) is cleaned up automatically, letting a retry of the
	// same archive ID reuse this same directory name instead of tripping
	// extractArchive's "already exists" guard forever. It flips to true the
	// moment extraction succeeds and stays true for every return from here
	// on -- a real extracted copy is worth preserving for inspection on any
	// later failure (disk-consistency, placement, or cancellation) -- and
	// is only reset to false right before the final, fully-successful
	// return. This mirrors Run's keepStaging/succeeded pattern
	// (choreography.go) but as a single flip instead of one manual
	// keepStaging = true per failure branch, which is what let two of those
	// branches (the ctx-cancellation checks below) forget it in the first
	// place.
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	reporter.Report(progress.Event{Stage: progress.Extracting, Message: "extracting archive"})
	// archive.Manifest.SizeBytes is the compressed size, an approximation
	// for extraction's (uncompressed) total -- same clamped-at-1 tolerance
	// percentOf already documents for Run's own Compressing stage.
	onWrite := throttledPercentReporter(reporter, progress.Extracting, archive.Manifest.SizeBytes)
	if err := extractArchive(archivePath, stagingDir, archive.Manifest.Compression, onWrite); err != nil {
		return nil, &RunError{Stage: progress.Extracting, Err: err}
	}
	keepStaging = true

	bundleBase, bundleDir, err := singleTopLevelEntry(stagingDir)
	if err != nil {
		return nil, &RunError{Stage: progress.Extracting, Err: err}
	}

	if runErr := checkCtx(ctx, progress.CheckingDiskConsistency); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.CheckingDiskConsistency, Message: "checking restored disk consistency"})
	vmxPath, err := findVMX(bundleDir)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	diskFiles, err := readDiskFiles(vmxPath)
	if err != nil {
		return nil, &RunError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	// A disk path that's absolute in the restored vmx refers to a disk
	// stored outside the .vmwarevm bundle on the *source* machine (Fusion
	// permits this) -- it was never part of the archive, so bundleDir here
	// (the extracted copy) doesn't contain it. checkDisksConsistent would
	// otherwise resolve that path as-is and silently check the original
	// live disk instead of anything actually restored -- reporting a
	// passing consistency check that verified the wrong file entirely.
	for _, diskFile := range diskFiles {
		if filepath.IsAbs(diskFile) {
			return nil, &RunError{Stage: progress.CheckingDiskConsistency, Err: fmt.Errorf("restored VM references disk %q stored outside the .vmwarevm bundle -- this disk was not part of the archive and cannot be verified or restored; the extracted copy at %s has been preserved for inspection", diskFile, stagingDir)}
		}
	}
	if err := checkDisksConsistent(ctrl, bundleDir, diskFiles); err != nil {
		return nil, &RunError{Stage: progress.CheckingDiskConsistency, Err: fmt.Errorf("restored disk consistency check failed -- the archive itself may be damaged; the extracted copy at %s has been preserved for inspection: %w", stagingDir, err)}
	}

	if runErr := checkCtx(ctx, progress.Placing); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Placing, Message: "placing restored bundle"})

	// Fusion decides whether a VM "was moved or copied" by hashing its
	// current path against uuid.location in the .vmx -- which never
	// matches here, since every restore lands at a brand-new path. Left
	// alone, that prompts the operator the first time the restored bundle
	// is opened. Restore always produces a copy -- the source is left
	// untouched at its original location (see this function's doc comment)
	// -- so "I Copied It" (a fresh BIOS UUID and MAC address) is the only
	// correct answer, never "I Moved It" (keep the source's identity),
	// which would leave two VMs claiming the same UUID/MAC if the source
	// is ever powered on at the same time. uuid.action = "create" tells
	// Fusion to silently apply that answer instead of asking.
	if err := setVMXKey(vmxPath, "uuid.action", "create"); err != nil {
		return nil, &RunError{Stage: progress.Placing, Err: fmt.Errorf("set restored VM identity: %w", err)}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	targetName, err := restoreTargetName(targetParent, bundleBase, now())
	if err != nil {
		return nil, &RunError{Stage: progress.Placing, Err: err}
	}
	targetPath := filepath.Join(targetParent, targetName)

	if err := placeBundle(bundleDir, targetPath); err != nil {
		return nil, &RunError{Stage: progress.Placing, Err: err}
	}

	keepStaging = false
	reporter.Report(progress.Event{Stage: progress.Done, Message: "restore complete"})
	return &RestoreResult{ArchiveID: archive.ArchiveID, TargetPath: targetPath, Manifest: archive.Manifest}, nil
}

// singleTopLevelEntry returns the base name (with ".vmwarevm" stripped)
// and full path of stagingDir's one and only child -- the .vmwarevm
// bundle directory extractArchive just produced. createArchive's tarTo
// always wraps exactly one top-level entry (the staged bundle dir, see
// choreography.go's stagedBundle/stagingRoot split), so anything other
// than exactly one directory here means the archive is malformed.
func singleTopLevelEntry(stagingDir string) (bundleBase, bundleDir string, err error) {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return "", "", fmt.Errorf("read extracted staging dir: %w", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", "", fmt.Errorf("extracted archive has %d top-level entries, want exactly one directory", len(entries))
	}
	name := entries[0].Name()
	return strings.TrimSuffix(name, ".vmwarevm"), filepath.Join(stagingDir, name), nil
}

// findVMX returns the path of the single .vmx file directly inside
// bundleDir.
func findVMX(bundleDir string) (string, error) {
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return "", fmt.Errorf("read bundle dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".vmx") {
			return filepath.Join(bundleDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .vmx file found in %s", bundleDir)
}

// renameFile is os.Rename, overridable in tests to force placeBundle's
// cross-device fallback path without needing two actual filesystem
// volumes.
var renameFile = os.Rename

// placeBundle moves src to dst via rename, falling back to a recursive
// copy + remove on a cross-device rename error (EXDEV) -- StagingDir may
// not share a volume with dst's parent.
func placeBundle(src, dst string) error {
	err := renameFile(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !errors.Is(linkErr.Err, syscall.EXDEV) {
		return fmt.Errorf("place bundle: %w", err)
	}
	if err := copyDir(src, dst, nil); err != nil {
		_ = os.RemoveAll(dst) // partial copy -- don't leave a broken bundle at the target
		return fmt.Errorf("copy bundle cross-device: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("copy bundle cross-device: restored bundle was placed successfully at %s, but removing the temporary staging copy at %s failed (safe to delete manually): %w", dst, src, err)
	}
	return nil
}

// throttledPercentReporter returns an onRead/onWrite callback that reports
// a Percent event for stage only when the rounded percentage changes --
// io.Copy's internal ~32KB buffer would otherwise fire this once per
// chunk (hundreds of thousands of times for a multi-GB archive),
// throttling hashing/extraction against the TUI's render goroutine for no
// visual benefit.
func throttledPercentReporter(reporter progress.Reporter, stage progress.Stage, total int64) func(cumulative int64) {
	lastReported := -1
	return func(cumulative int64) {
		pct := percentOf(cumulative, total)
		rounded := int(pct*100 + 0.5)
		if rounded == lastReported {
			return
		}
		lastReported = rounded
		reporter.Report(progress.Event{Stage: stage, Percent: pct})
	}
}
