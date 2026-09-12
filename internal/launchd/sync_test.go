package launchd

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

// indexOfCall returns the index of the first entry in calls equal to
// want, or -1 if not found. Used to assert relative ordering between
// different Installer methods via FakeInstaller.Calls.
func indexOfCall(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

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
	if len(inst.WriteCalls) != 0 {
		t.Errorf("WriteCalls = %v, want none for an unscheduled VM", inst.WriteCalls)
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
	if len(inst.BootoutCalls) != 0 {
		t.Errorf("BootoutCalls after two Syncs = %v, want none -- already in sync means no Bootstrap/Bootout at all on the second call", inst.BootoutCalls)
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

	// Cross-method ordering: within this second Sync call, the stale
	// job must be booted out before the new plist is bootstrapped -- the
	// per-method slices above can't show this, only the shared Calls log
	// can, since it's the same label/path appearing in both.
	label := "com.tim.snapback.dev"
	plistPath := "/fake/LaunchAgents/" + label + ".plist"
	bootoutIdx := indexOfCall(inst.Calls, "bootout:"+label)
	// The second "bootstrap:<path>" entry is the update's -- the first
	// one belongs to the initial install from the first Sync call.
	firstBootstrapIdx := indexOfCall(inst.Calls, "bootstrap:"+plistPath)
	secondBootstrapIdx := -1
	for i := firstBootstrapIdx + 1; i < len(inst.Calls); i++ {
		if inst.Calls[i] == "bootstrap:"+plistPath {
			secondBootstrapIdx = i
			break
		}
	}
	if bootoutIdx == -1 || secondBootstrapIdx == -1 {
		t.Fatalf("Calls = %v, want both a bootout and a second bootstrap entry for %q", inst.Calls, label)
	}
	if bootoutIdx > secondBootstrapIdx {
		t.Errorf("Calls = %v, want bootout:%s (index %d) before the update's bootstrap:%s (index %d)", inst.Calls, label, bootoutIdx, plistPath, secondBootstrapIdx)
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

	// Cross-method ordering: Bootout must precede Remove for the same
	// label -- deleting the plist file before unloading the job would
	// leave launchd holding a reference to a file that no longer exists.
	label := "com.tim.snapback.dev"
	bootoutIdx := indexOfCall(inst.Calls, "bootout:"+label)
	removeIdx := indexOfCall(inst.Calls, "remove:"+label)
	if bootoutIdx == -1 || removeIdx == -1 {
		t.Fatalf("Calls = %v, want both a bootout and a remove entry for %q", inst.Calls, label)
	}
	if bootoutIdx > removeIdx {
		t.Errorf("Calls = %v, want bootout:%s (index %d) before remove:%s (index %d)", inst.Calls, label, bootoutIdx, label, removeIdx)
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
	if len(inst.WriteCalls) != 0 {
		t.Errorf("WriteCalls = %v, want none -- collision must be caught before any write", inst.WriteCalls)
	}
	if len(inst.BootstrapCalls) != 0 {
		t.Errorf("BootstrapCalls = %v, want none -- collision must be caught before any write", inst.BootstrapCalls)
	}
	if len(inst.BootoutCalls) != 0 {
		t.Errorf("BootoutCalls = %v, want none -- collision must be caught before any write", inst.BootoutCalls)
	}
	if len(inst.RemoveCalls) != 0 {
		t.Errorf("RemoveCalls = %v, want none -- collision must be caught before any write", inst.RemoveCalls)
	}
}

func TestSync_MixedWorkload_InstallsOneAndRemovesAnother(t *testing.T) {
	inst := NewFakeInstaller()
	// "old" is already installed and about to be dropped from the config
	// entirely; "new" doesn't exist yet.
	if _, err := Sync(inst, []config.VM{{Name: "old", VMX: "/vms/old.vmx", Schedule: "daily"}}, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms := []config.VM{{Name: "new", VMX: "/vms/new.vmx", Schedule: "daily"}}
	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	if len(result.Installed) != 1 || result.Installed[0] != "new" {
		t.Errorf("result.Installed = %v, want [\"new\"]", result.Installed)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "com.tim.snapback.old" {
		t.Errorf("result.Removed = %v, want [\"com.tim.snapback.old\"]", result.Removed)
	}

	// The newly-scheduled VM must not be swept up by the removal loop.
	for _, l := range inst.BootoutCalls {
		if l == "com.tim.snapback.new" {
			t.Errorf("BootoutCalls = %v, the newly-installed VM's label must never be booted out", inst.BootoutCalls)
		}
	}
	for _, l := range inst.RemoveCalls {
		if l == "com.tim.snapback.new" {
			t.Errorf("RemoveCalls = %v, the newly-installed VM's label must never be removed", inst.RemoveCalls)
		}
	}

	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 1 || labels[0] != "com.tim.snapback.new" {
		t.Errorf("List() = %v, want only [\"com.tim.snapback.new\"] left installed", labels)
	}
}

func TestSync_ErrorMidLoop_PreservesPartialResult(t *testing.T) {
	inst := NewFakeInstaller()
	inst.BootstrapErr = errors.New("simulated bootstrap failure")
	inst.BootstrapFailAt = 2 // the first VM's Bootstrap succeeds, the second's fails
	vms := []config.VM{
		{Name: "first", VMX: "/vms/first.vmx", Schedule: "daily"},
		{Name: "second", VMX: "/vms/second.vmx", Schedule: "daily"},
	}

	result, err := Sync(inst, vms, "/bin/snapback")
	if err == nil {
		t.Fatal("Sync() error = nil, want the second VM's Bootstrap failure surfaced")
	}
	if len(result.Installed) != 1 || result.Installed[0] != "first" {
		t.Errorf("result.Installed = %v, want [\"first\"] preserved despite the second VM's failure", result.Installed)
	}
	if len(inst.BootstrapCalls) != 2 {
		t.Errorf("BootstrapCalls = %v, want both attempted (first succeeds, second fails)", inst.BootstrapCalls)
	}
}
