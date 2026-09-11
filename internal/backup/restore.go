package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
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

// RestoreError mirrors RunError: which stage was active when the restore
// failed.
type RestoreError struct {
	Stage progress.Stage
	Err   error
}

func (e *RestoreError) Error() string               { return e.Err.Error() }
func (e *RestoreError) Unwrap() error               { return e.Err }
func (e *RestoreError) FailedStage() progress.Stage { return e.Stage }

func checkRestoreCtx(ctx context.Context, stage progress.Stage) *RestoreError {
	if err := ctx.Err(); err != nil {
		return &RestoreError{Stage: stage, Err: err}
	}
	return nil
}

// Restore verifies a backup archive against its manifest checksum,
// extracts it, confirms the restored disk chain is consistent, and places
// it as a new, non-destructively-named .vmwarevm bundle -- never
// overwriting the source. See ADR-004
// (docs/superpowers/specs/2026-09-11-restore-design.md) for the full
// design. Restore never touches the source VM -- ctrl is used only for
// CheckDiskConsistency against the restored copy.
func Restore(ctx context.Context, ctrl vm.Controller, reporter progress.Reporter, opts RestoreOptions) (*RestoreResult, error) {
	if reporter == nil {
		reporter = progress.NoOpReporter{}
	}

	if opts.TargetDir == "" && opts.VMXPath == "" {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("one of TargetDir or VMXPath is required")}
	}
	if opts.TargetDir != "" && opts.VMXPath != "" {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("TargetDir and VMXPath are mutually exclusive")}
	}

	targetParent := opts.TargetDir
	if targetParent == "" {
		targetParent = filepath.Dir(filepath.Dir(opts.VMXPath))
	}
	if info, statErr := os.Stat(targetParent); statErr != nil {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("restore target parent directory %s: %w", targetParent, statErr)}
	} else if !info.IsDir() {
		return nil, &RestoreError{Stage: progress.Verifying, Err: fmt.Errorf("restore target parent %s is not a directory", targetParent)}
	}

	if runErr := checkRestoreCtx(ctx, progress.Verifying); runErr != nil {
		return nil, runErr
	}

	archive, err := FindArchive(opts.Destination, opts.ArchiveID)
	if err != nil {
		return nil, &RestoreError{Stage: progress.Verifying, Err: err}
	}

	ext := "tar.gz"
	if archive.Manifest.Compression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(opts.Destination, archive.ArchiveID, "archive."+ext)

	reporter.Report(progress.Event{Stage: progress.Verifying, Message: "verifying archive checksum"})
	if err := verifyChecksum(archivePath, archive.Manifest.SHA256, throttledPercentReporter(reporter, progress.Verifying, archive.Manifest.SizeBytes)); err != nil {
		return nil, &RestoreError{Stage: progress.Verifying, Err: err}
	}

	if runErr := checkRestoreCtx(ctx, progress.Extracting); runErr != nil {
		return nil, runErr
	}

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingDir := filepath.Join(stagingParent, "snapback-restore-"+opts.ArchiveID)

	reporter.Report(progress.Event{Stage: progress.Extracting, Message: "extracting archive"})
	// archive.Manifest.SizeBytes is the compressed size, an approximation
	// for extraction's (uncompressed) total -- same clamped-at-1 tolerance
	// percentOf already documents for Run's own Compressing stage.
	onWrite := throttledPercentReporter(reporter, progress.Extracting, archive.Manifest.SizeBytes)
	if err := extractArchive(archivePath, stagingDir, archive.Manifest.Compression, onWrite); err != nil {
		return nil, &RestoreError{Stage: progress.Extracting, Err: err}
	}

	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	bundleBase, bundleDir, err := singleTopLevelEntry(stagingDir)
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.Extracting, Err: err}
	}

	if runErr := checkRestoreCtx(ctx, progress.CheckingDiskConsistency); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.CheckingDiskConsistency, Message: "checking restored disk consistency"})
	vmxPath, err := findVMX(bundleDir)
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	diskFiles, err := readDiskFiles(vmxPath)
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: err}
	}
	if len(diskFiles) == 0 {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: fmt.Errorf("no virtual disks found in restored %s -- cannot verify the archive's disk chain; the extracted copy at %s has been preserved for inspection", vmxPath, stagingDir)}
	}
	if err := checkDisksConsistent(ctrl, bundleDir, diskFiles); err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.CheckingDiskConsistency, Err: fmt.Errorf("restored disk consistency check failed -- the archive itself may be damaged; the extracted copy at %s has been preserved for inspection: %w", stagingDir, err)}
	}

	if runErr := checkRestoreCtx(ctx, progress.Placing); runErr != nil {
		return nil, runErr
	}
	reporter.Report(progress.Event{Stage: progress.Placing, Message: "placing restored bundle"})

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	targetName, err := restoreTargetName(targetParent, bundleBase, now())
	if err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.Placing, Err: err}
	}
	targetPath := filepath.Join(targetParent, targetName)

	if err := placeBundle(bundleDir, targetPath); err != nil {
		keepStaging = true
		return nil, &RestoreError{Stage: progress.Placing, Err: err}
	}

	reporter.Report(progress.Event{Stage: progress.Done, Message: "restore complete"})
	return &RestoreResult{ArchiveID: archive.ArchiveID, TargetPath: targetPath, Manifest: archive.Manifest}, nil
}

// countingHasher wraps a hash.Hash, invoking onWrite with the running
// cumulative byte count as it's written to -- lets verifyChecksum drive
// Verifying's Percent the same per-chunk way tarTo/copyDir do for their
// own stages.
type countingHasher struct {
	h          hash.Hash
	onWrite    func(cumulative int64)
	cumulative int64
}

func (c *countingHasher) Write(p []byte) (int, error) {
	n, err := c.h.Write(p)
	c.cumulative += int64(n)
	if c.onWrite != nil {
		c.onWrite(c.cumulative)
	}
	return n, err
}

// verifyChecksum streams path through SHA-256, comparing the result
// against want (lowercase hex). If onRead is non-nil, it's invoked with
// the running cumulative bytes read.
func verifyChecksum(path, want string, onRead func(cumulative int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	ch := &countingHasher{h: sha256.New(), onWrite: onRead}
	if _, err := io.Copy(ch, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	got := fmt.Sprintf("%x", ch.h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
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
