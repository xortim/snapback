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

// nthCall returns the index of the n'th (1-indexed) entry in calls equal
// to want, or -1 if there aren't that many. Needed because both the
// install and update paths now emit a bootout+bootstrap pair for the
// same label, so "the first one" is no longer the one under test.
func nthCall(calls []string, want string, n int) int {
	seen := 0
	for i, c := range calls {
		if c == want {
			seen++
			if seen == n {
				return i
			}
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

	// Even a fresh install boots out first: "no plist on disk" doesn't
	// prove the label isn't still loaded in launchd's session (a
	// hand-deleted plist leaves it loaded), and Bootstrap fails against
	// an already-loaded label. Bootout is idempotent when nothing is
	// loaded, so this is free on the normal path.
	label := "com.tim.snapback.dev"
	if len(inst.BootoutCalls) != 1 || inst.BootoutCalls[0] != label {
		t.Errorf("BootoutCalls = %v, want [%q] -- install must bootout before bootstrap", inst.BootoutCalls, label)
	}
	bootoutIdx := indexOfCall(inst.Calls, "bootout:"+label)
	bootstrapIdx := indexOfCall(inst.Calls, "bootstrap:/fake/LaunchAgents/"+label+".plist")
	if bootoutIdx == -1 || bootstrapIdx == -1 || bootoutIdx > bootstrapIdx {
		t.Errorf("Calls = %v, want bootout before bootstrap on the install path", inst.Calls)
	}
	// ...but never a Remove -- that's the removal loop's business only.
	if len(inst.RemoveCalls) != 0 {
		t.Errorf("RemoveCalls = %v, want none for a freshly-installed VM", inst.RemoveCalls)
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
	// Snapshot the first Sync's calls; the assertion below is that the
	// *second* Sync adds nothing to them, not that the log is empty (the
	// install itself legitimately boots out before bootstrapping).
	callsAfterFirst := len(inst.Calls)
	bootstrapsAfterFirst := len(inst.BootstrapCalls)
	bootoutsAfterFirst := len(inst.BootoutCalls)

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if !result.IsEmpty() {
		t.Errorf("second Sync() result = %+v, want empty (already in sync)", result)
	}
	if len(inst.BootstrapCalls) != bootstrapsAfterFirst {
		t.Errorf("BootstrapCalls after two Syncs = %v, want no new call from the second Sync", inst.BootstrapCalls)
	}
	if len(inst.BootoutCalls) != bootoutsAfterFirst {
		t.Errorf("BootoutCalls after two Syncs = %v, want no new call from the second Sync", inst.BootoutCalls)
	}
	// Nothing at all beyond the Write the change-detection needs.
	if got := inst.Calls[callsAfterFirst:]; len(got) != 1 || got[0] != "write:com.tim.snapback.dev" {
		t.Errorf("second Sync() made calls %v, want only the change-detecting Write", got)
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
	// 2 bootouts and 2 bootstraps: one pair from the first Sync's install
	// (which boots out defensively too), one pair from this update.
	if len(inst.BootoutCalls) != 2 {
		t.Errorf("BootoutCalls = %v, want 2 (the install's defensive bootout + the stale plist's)", inst.BootoutCalls)
	}
	if len(inst.BootstrapCalls) != 2 {
		t.Errorf("BootstrapCalls = %v, want 2 (initial install + the update)", inst.BootstrapCalls)
	}

	// Cross-method ordering: within this second Sync call, the stale
	// job must be booted out before the new plist is bootstrapped -- the
	// per-method slices above can't show this, only the shared Calls log
	// can, since it's the same label/path appearing in both. Both entries
	// must be the *second* occurrence; the first pair belongs to the
	// initial install from the first Sync call.
	label := "com.tim.snapback.dev"
	plistPath := "/fake/LaunchAgents/" + label + ".plist"
	secondBootoutIdx := nthCall(inst.Calls, "bootout:"+label, 2)
	secondBootstrapIdx := nthCall(inst.Calls, "bootstrap:"+plistPath, 2)
	if secondBootoutIdx == -1 || secondBootstrapIdx == -1 {
		t.Fatalf("Calls = %v, want a second bootout and a second bootstrap entry for %q", inst.Calls, label)
	}
	if secondBootoutIdx > secondBootstrapIdx {
		t.Errorf("Calls = %v, want the update's bootout:%s (index %d) before its bootstrap:%s (index %d)", inst.Calls, label, secondBootoutIdx, plistPath, secondBootstrapIdx)
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
	// Its label does get booted out once -- that's the install path's
	// defensive bootout, which must come *before* its bootstrap; a
	// bootout after the bootstrap would mean the removal loop had
	// unloaded the job it just installed.
	newLabel := "com.tim.snapback.new"
	newBootoutIdx := indexOfCall(inst.Calls, "bootout:"+newLabel)
	newBootstrapIdx := indexOfCall(inst.Calls, "bootstrap:/fake/LaunchAgents/"+newLabel+".plist")
	if newBootstrapIdx == -1 {
		t.Fatalf("Calls = %v, want the new VM bootstrapped", inst.Calls)
	}
	if newBootoutIdx > newBootstrapIdx {
		t.Errorf("Calls = %v, the newly-installed VM was booted out after being bootstrapped", inst.Calls)
	}
	if nthCall(inst.Calls, "bootout:"+newLabel, 2) != -1 {
		t.Errorf("Calls = %v, the newly-installed VM's label was booted out more than once", inst.Calls)
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
