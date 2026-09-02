package vm

import (
	"errors"
	"fmt"
)

// FakeVMController is an in-memory Controller for unit tests. It tracks
// snapshots per vmxPath and lets tests inject an error per method by
// setting the corresponding *Err field — the field stays set (sticky)
// until the test clears it, so every call to that method fails until then.
type FakeVMController struct {
	ToolsState    ToolsState
	ToolsStateErr error
	SnapshotErr   error

	ListSnapshotsErr  error
	DeleteSnapshotErr error

	snapshots map[string][]string
}

// NewFakeVMController returns a FakeVMController with no snapshots and
// ToolsState defaulted to ToolsUnknown.
func NewFakeVMController() *FakeVMController {
	return &FakeVMController{
		ToolsState: ToolsUnknown,
		snapshots:  make(map[string][]string),
	}
}

func (f *FakeVMController) CheckToolsState(vmxPath string) (ToolsState, error) {
	if f.ToolsStateErr != nil {
		return "", f.ToolsStateErr
	}
	return f.ToolsState, nil
}

func (f *FakeVMController) Snapshot(vmxPath, name string) error {
	if f.SnapshotErr != nil {
		return f.SnapshotErr
	}
	f.snapshots[vmxPath] = append(f.snapshots[vmxPath], name)
	return nil
}

func (f *FakeVMController) ListSnapshots(vmxPath string) ([]string, error) {
	if f.ListSnapshotsErr != nil {
		return nil, f.ListSnapshotsErr
	}
	out := make([]string, len(f.snapshots[vmxPath]))
	copy(out, f.snapshots[vmxPath])
	return out, nil
}

func (f *FakeVMController) DeleteSnapshot(vmxPath, name string) error {
	if f.DeleteSnapshotErr != nil {
		return f.DeleteSnapshotErr
	}
	existing := f.snapshots[vmxPath]
	for i, s := range existing {
		if s == name {
			f.snapshots[vmxPath] = append(existing[:i], existing[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("snapshot %q not found for %q", name, vmxPath)
}

// DeleteSnapshots removes each snapshot in names, honoring
// DeleteSnapshotErr the same way DeleteSnapshot does. It doesn't
// short-circuit on the first failure -- callers rely on getting every
// name it could remove even when some fail.
func (f *FakeVMController) DeleteSnapshots(vmxPath string, names []string) (deleted []string, err error) {
	var errs []error
	for _, name := range names {
		if delErr := f.DeleteSnapshot(vmxPath, name); delErr != nil {
			errs = append(errs, fmt.Errorf("remove snapshot %q: %w", name, delErr))
			continue
		}
		deleted = append(deleted, name)
	}
	return deleted, errors.Join(errs...)
}
