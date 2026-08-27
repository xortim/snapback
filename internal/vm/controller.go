// Package vm isolates VMware Fusion snapshot/VM control behind an
// interface, so the backup choreography logic never shells out directly.
package vm

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
	// orphaned snapback-<timestamp> snapshot.
	Snapshot(vmxPath, name string) error
	ListSnapshots(vmxPath string) ([]string, error)
	DeleteSnapshot(vmxPath, name string) error
}
