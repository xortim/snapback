// Package vm isolates VMware Fusion snapshot/VM control behind an
// interface, so the backup choreography logic never shells out directly.
package vm

import "errors"

// ToolsState is the guest VMware Tools state, as reported by the backing
// CLI (vmcli or vmrun). Only ToolsRunning means the guest filesystem can
// be quiesced before a snapshot commits.
type ToolsState string

const (
	ToolsInstalled    ToolsState = "installed"
	ToolsRunning      ToolsState = "running"
	ToolsNotInstalled ToolsState = "notInstalled"
	// ToolsUnknown is a real, confirmed return value (not hypothetical) —
	// what a Tools-less guest reports. Treat it the same as
	// ToolsNotInstalled: gate the quiesce step off, proceed
	// crash-consistent.
	ToolsUnknown ToolsState = "unknown"
)

// ErrOrphanPossible wraps the error Snapshot returns when a Take failure's
// cleanup could not be confirmed: the post-failure snapshot lookup itself
// failed or was ambiguous, or the cleanup Delete call failed. In every
// other failure case Snapshot's contract (below) still holds -- no
// snapshot was left behind. internal/backup.Run checks for this with
// errors.Is and tags its RunError at Stage: Snapshotting (instead of
// below it) so the caller's orphan warning fires. Declared here, next to
// the contract it modifies, rather than in the vmcli-specific
// implementation file -- any future Controller implementation (e.g. the
// documented-but-unimplemented vmrun fallback) wraps this same sentinel.
var ErrOrphanPossible = errors.New("snapshot cleanup could not be confirmed; a partial snapshot may remain")

// Controller is the seam between the backup choreography and actual VM
// control. A real implementation shells out to vmcli/vmrun; a fake
// implementation backs unit tests with no Fusion install required.
type Controller interface {
	CheckToolsState(vmxPath string) (ToolsState, error)
	// Snapshot must not leave a snapshot behind on the source VM when it
	// returns a non-nil error. If snapshot creation partially succeeds and
	// then fails, the implementation is responsible for removing the
	// partial snapshot before returning the error. internal/backup.Run
	// relies on this: it treats a Snapshot error as proof no snapshot
	// exists, used to decide whether to warn the caller about a possible
	// orphaned snapback-<timestamp> snapshot -- unless that error wraps
	// ErrOrphanPossible (above), which means cleanup itself could not be
	// confirmed and a snapshot may actually remain.
	Snapshot(vmxPath, name string) error
	ListSnapshots(vmxPath string) ([]string, error)
	DeleteSnapshot(vmxPath, name string) error
	// DeleteSnapshots removes every snapshot in names from vmxPath. An
	// implementation can resolve all of them from a single snapshot
	// listing instead of one lookup per name (see vmcli.go's
	// implementation) -- internal/cli's cleanup command uses this instead
	// of looping DeleteSnapshot, since it already knows the exact set of
	// orphaned names to remove up front. It returns the subset of names
	// actually removed; a failure on one name is joined into err rather
	// than aborting the rest, so a caller still removes everything it can
	// and reports what it couldn't.
	DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error)

	// CheckDiskConsistency verifies one virtual disk's snapshot chain --
	// the descriptor at diskPath plus every parent it depends on, all the
	// way down to the base disk -- returning a descriptive error if any
	// link in that chain fails its own consistency check (VMware's
	// "needs repair" state). diskPath is an absolute path to a single
	// disk's top-level .vmdk descriptor file, not a vmxPath; a VM can have
	// more than one virtual disk device, so internal/backup calls this
	// once per disk it finds configured in the .vmx.
	//
	// Added after a real incident (see docs/design.md's "Risks &
	// Gotchas"): `vmcli Snapshot Delete` reported success while merging a
	// snapshot on a powered-off VM whose disk chain had a latent defect,
	// leaving the source VM unable to power on afterward with no error
	// ever surfaced. internal/backup.Run calls this both before taking a
	// new snapshot (so a chain that's already broken is caught before
	// piling another snapshot on top of it) and again right after the
	// merge (so a merge that silently failed to apply is caught before
	// the backup is reported as a success).
	CheckDiskConsistency(diskPath string) error
}
