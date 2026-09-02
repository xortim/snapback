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
}
