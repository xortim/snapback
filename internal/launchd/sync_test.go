package launchd

import (
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestSync_InstallsNewlyScheduledVM(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(result.Installed) != 1 || result.Installed[0] != "dev" {
		t.Errorf("result.Installed = %v, want [\"dev\"]", result.Installed)
	}
	if len(inst.BootstrapCalls) != 1 {
		t.Errorf("BootstrapCalls = %v, want exactly one call", inst.BootstrapCalls)
	}
}

func TestSync_UnscheduledVM_NeverWritten(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}} // Schedule == ""

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !result.IsEmpty() {
		t.Errorf("result = %+v, want empty", result)
	}
	if len(inst.BootstrapCalls) != 0 {
		t.Errorf("BootstrapCalls = %v, want none for an unscheduled VM", inst.BootstrapCalls)
	}
}

func TestSync_AlreadyInSync_IsANoOp(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if !result.IsEmpty() {
		t.Errorf("second Sync() result = %+v, want empty (already in sync)", result)
	}
	if len(inst.BootstrapCalls) != 1 {
		t.Errorf("BootstrapCalls after two Syncs = %v, want still exactly one (from the first Sync only)", inst.BootstrapCalls)
	}
}

func TestSync_ScheduleChanged_UpdatesAndRebootstraps(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms[0].Schedule = "weekly"
	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "dev" {
		t.Errorf("result.Updated = %v, want [\"dev\"]", result.Updated)
	}
	if len(inst.BootoutCalls) != 1 {
		t.Errorf("BootoutCalls = %v, want the stale plist booted out before re-bootstrapping", inst.BootoutCalls)
	}
	if len(inst.BootstrapCalls) != 2 {
		t.Errorf("BootstrapCalls = %v, want 2 (initial install + the update)", inst.BootstrapCalls)
	}
}

func TestSync_ScheduleCleared_RemovesPlist(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms[0].Schedule = ""
	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "com.tim.snapback.dev" {
		t.Errorf("result.Removed = %v, want [\"com.tim.snapback.dev\"]", result.Removed)
	}
	if len(inst.RemoveCalls) != 1 {
		t.Errorf("RemoveCalls = %v, want exactly one", inst.RemoveCalls)
	}
}

func TestSync_VMNoLongerInList_RemovesPlist(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	result, err := Sync(inst, nil, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Removed) != 1 {
		t.Errorf("result.Removed = %v, want the now-gone VM's plist removed", result.Removed)
	}
}

func TestSync_CollidingNames_ErrorsBeforeWritingAnything(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{
		{Name: "My VM!", VMX: "/vms/a.vmx", Schedule: "daily"},
		{Name: "My VM?", VMX: "/vms/b.vmx", Schedule: "weekly"},
	}
	_, err := Sync(inst, vms, "/bin/snapback")
	if err == nil {
		t.Fatal("Sync() error = nil, want the collision rejected")
	}
	if len(inst.BootstrapCalls) != 0 {
		t.Errorf("BootstrapCalls = %v, want none -- collision must be caught before any write", inst.BootstrapCalls)
	}
}
