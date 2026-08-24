package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

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

// Run executes the snapshot -> copy -> merge -> archive choreography
// described in docs/design.md ("Backup choreography") against ctrl. The
// source VM is never paused: ctrl.Snapshot freezes the disk state, the
// bundle is copied while the VM keeps running against a fresh delta, and
// ctrl.DeleteSnapshot merges that delta back once the copy is safely
// staged.
//
// On error, no cleanup beyond removing the staging directory is
// attempted — a snapshot left behind by a failed run is intentionally
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback here.
func Run(ctrl vm.Controller, opts Options) (*Result, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	startTime := now().UTC()
	ts := startTime.Format("20060102T150405Z")
	archiveID := fmt.Sprintf("%s-%s", opts.VMName, ts)
	snapshotName := "snapback-" + ts

	toolsState, err := ctrl.CheckToolsState(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("check tools state: %w", err)
	}

	if err := ctrl.Snapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	snapshots, err := ctrl.ListSnapshots(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	if !slices.Contains(snapshots, snapshotName) {
		return nil, fmt.Errorf("snapshot %q not found after creation", snapshotName)
	}

	hostSync()

	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("read guest OS: %w", err)
	}

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingRoot := filepath.Join(stagingParent, "snapback-staging-"+archiveID)
	bundleDir := filepath.Dir(opts.VMXPath)
	stagedBundle := filepath.Join(stagingRoot, filepath.Base(bundleDir))
	defer func() { _ = os.RemoveAll(stagingRoot) }()
	if err := copyDir(bundleDir, stagedBundle); err != nil {
		return nil, fmt.Errorf("copy bundle: %w", err)
	}

	if err := ctrl.DeleteSnapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, fmt.Errorf("delete snapshot: %w", err)
	}

	outputDir := filepath.Join(opts.Destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	tempArchivePath := filepath.Join(outputDir, "archive.tmp")
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	ext := "tar.gz"
	if usedCompression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	if err := os.Rename(tempArchivePath, archivePath); err != nil {
		return nil, fmt.Errorf("rename archive: %w", err)
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	return &Result{
		ArchiveID:    archiveID,
		OutputDir:    outputDir,
		ArchivePath:  archivePath,
		ManifestPath: manifestPath,
		Manifest:     manifest,
	}, nil
}

// hostSync flushes the host page cache before copy.go reads the frozen
// disk files. Best-effort only: the snapshot already guarantees the
// source files are frozen at the VMware layer, so a failure here is not
// fatal — it just removes a class of doubt per docs/design.md.
func hostSync() {
	_ = exec.Command("sync").Run()
}
