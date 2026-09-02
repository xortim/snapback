package vm_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/vm"
)

var _ vm.Controller = (*vm.FakeVMController)(nil)

func TestFakeVMController_CheckToolsState_DefaultsToUnknown(t *testing.T) {
	f := vm.NewFakeVMController()

	got, err := f.CheckToolsState("/vms/example.vmwarevm/example.vmx")
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != vm.ToolsUnknown {
		t.Errorf("CheckToolsState() = %q, want %q", got, vm.ToolsUnknown)
	}
}

func TestFakeVMController_CheckToolsState_ReturnsConfiguredState(t *testing.T) {
	f := vm.NewFakeVMController()
	f.ToolsState = vm.ToolsRunning

	got, err := f.CheckToolsState("/vms/example.vmwarevm/example.vmx")
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != vm.ToolsRunning {
		t.Errorf("CheckToolsState() = %q, want %q", got, vm.ToolsRunning)
	}
}

func TestFakeVMController_CheckToolsState_ReturnsInjectedError(t *testing.T) {
	f := vm.NewFakeVMController()
	wantErr := errors.New("tools query failed")
	f.ToolsStateErr = wantErr

	_, err := f.CheckToolsState("/vms/example.vmwarevm/example.vmx")
	if !errors.Is(err, wantErr) {
		t.Errorf("CheckToolsState() error = %v, want %v", err, wantErr)
	}
}

func TestFakeVMController_Snapshot_AddsToListSnapshots(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"

	if err := f.Snapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}

	got, err := f.ListSnapshots(vmx)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v, want nil", err)
	}
	want := []string{"snapback-1"}
	if !equalStrings(got, want) {
		t.Errorf("ListSnapshots() = %v, want %v", got, want)
	}
}

func TestFakeVMController_ListSnapshots_EmptyForUnknownVMX(t *testing.T) {
	f := vm.NewFakeVMController()

	got, err := f.ListSnapshots("/vms/never-snapshotted.vmwarevm/x.vmx")
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty", got)
	}
}

func TestFakeVMController_Snapshot_ReturnsInjectedErrorAndDoesNotMutate(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"
	wantErr := errors.New("snapshot failed")
	f.SnapshotErr = wantErr

	err := f.Snapshot(vmx, "snapback-1")
	if !errors.Is(err, wantErr) {
		t.Errorf("Snapshot() error = %v, want %v", err, wantErr)
	}

	got, _ := f.ListSnapshots(vmx)
	if len(got) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after failed Snapshot", got)
	}
}

func TestFakeVMController_ListSnapshots_ReturnsInjectedError(t *testing.T) {
	f := vm.NewFakeVMController()
	wantErr := errors.New("list failed")
	f.ListSnapshotsErr = wantErr

	_, err := f.ListSnapshots("/vms/example.vmwarevm/example.vmx")
	if !errors.Is(err, wantErr) {
		t.Errorf("ListSnapshots() error = %v, want %v", err, wantErr)
	}
}

func TestFakeVMController_DeleteSnapshot_RemovesFromListSnapshots(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"
	if err := f.Snapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}

	if err := f.DeleteSnapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v, want nil", err)
	}

	got, _ := f.ListSnapshots(vmx)
	if len(got) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after DeleteSnapshot", got)
	}
}

func TestFakeVMController_DeleteSnapshot_UnknownNameReturnsError(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"

	err := f.DeleteSnapshot(vmx, "does-not-exist")
	if err == nil {
		t.Fatal("DeleteSnapshot() error = nil, want not-found error")
	}
}

func TestFakeVMController_DeleteSnapshot_ReturnsInjectedErrorAndDoesNotMutate(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"
	if err := f.Snapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}
	wantErr := errors.New("delete failed")
	f.DeleteSnapshotErr = wantErr

	err := f.DeleteSnapshot(vmx, "snapback-1")
	if !errors.Is(err, wantErr) {
		t.Errorf("DeleteSnapshot() error = %v, want %v", err, wantErr)
	}

	got, _ := f.ListSnapshots(vmx)
	want := []string{"snapback-1"}
	if !equalStrings(got, want) {
		t.Errorf("ListSnapshots() = %v, want %v (delete should not have mutated state)", got, want)
	}
}

func TestFakeVMController_DeleteSnapshots_RemovesEachAndReportsMissing(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmx = "/vms/example.vmwarevm/example.vmx"
	if err := f.Snapshot(vmx, "snapback-1"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	deleted, err := f.DeleteSnapshots(vmx, []string{"snapback-1", "does-not-exist"})
	if len(deleted) != 1 || deleted[0] != "snapback-1" {
		t.Errorf("DeleteSnapshots() deleted = %v, want only snapback-1", deleted)
	}
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("DeleteSnapshots() error = %v, want it to name the missing snapshot", err)
	}
	remaining, lerr := f.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after DeleteSnapshots", remaining)
	}
}

func TestFakeVMController_TracksSnapshotsPerVMXIndependently(t *testing.T) {
	f := vm.NewFakeVMController()
	const vmxA = "/vms/a.vmwarevm/a.vmx"
	const vmxB = "/vms/b.vmwarevm/b.vmx"

	if err := f.Snapshot(vmxA, "snapback-1"); err != nil {
		t.Fatalf("Snapshot(vmxA) error = %v, want nil", err)
	}

	gotA, _ := f.ListSnapshots(vmxA)
	gotB, _ := f.ListSnapshots(vmxB)
	if !equalStrings(gotA, []string{"snapback-1"}) {
		t.Errorf("ListSnapshots(vmxA) = %v, want [snapback-1]", gotA)
	}
	if len(gotB) != 0 {
		t.Errorf("ListSnapshots(vmxB) = %v, want empty", gotB)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
