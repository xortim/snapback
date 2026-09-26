package launchd

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestCheckDrift_CleanConfig_ReportsNoDrift(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if !report.IsEmpty() {
		t.Errorf("report = %+v, want empty for a freshly-synced config", report)
	}
}

func TestCheckDrift_ScheduledButNotInstalled(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotInstalled) != 1 || report.NotInstalled[0] != "dev" {
		t.Errorf("report.NotInstalled = %v, want [\"dev\"]", report.NotInstalled)
	}
	if len(report.OutOfSync) != 0 || len(report.NotLoaded) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only NotInstalled populated", report)
	}
}

func TestCheckDrift_ContentDiffers_ReportsOutOfSync(t *testing.T) {
	inst := NewFakeInstaller()
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/old/bin/snapback"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	report, err := CheckDrift(inst, vms, "/new/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.OutOfSync) != 1 || report.OutOfSync[0] != "dev" {
		t.Errorf("report.OutOfSync = %v, want [\"dev\"]", report.OutOfSync)
	}
	if len(report.NotInstalled) != 0 || len(report.NotLoaded) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only OutOfSync populated", report)
	}
}

func TestCheckDrift_NotLoaded(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.dev"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotLoaded) != 1 || report.NotLoaded[0] != "dev" {
		t.Errorf("report.NotLoaded = %v, want [\"dev\"]", report.NotLoaded)
	}
	if len(report.NotInstalled) != 0 || len(report.OutOfSync) != 0 || len(report.Stale) != 0 {
		t.Errorf("report = %+v, want only NotLoaded populated", report)
	}
}

func TestCheckDrift_OrphanedLabel_ReportsStale(t *testing.T) {
	inst := NewFakeInstaller()
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.orphan", VMName: "orphan"}); err != nil {
		t.Fatalf("seed Write() error = %v", err)
	}

	report, err := CheckDrift(inst, nil, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.Stale) != 1 || report.Stale[0] != "com.tim.snapback.orphan" {
		t.Errorf("report.Stale = %v, want [\"com.tim.snapback.orphan\"]", report.Stale)
	}
}

func TestCheckDrift_MixedWorkload_OneOfEachCategory(t *testing.T) {
	inst := NewFakeInstaller()
	// "loaded": fully in sync.
	// "notinstalled": desired, nothing on disk.
	// "differs": on disk, wrong content.
	// "notloaded": on disk, right content, manually booted out.
	// "orphan": on disk, no longer desired.
	vms := []config.VM{
		{Name: "loaded", VMX: "/vms/loaded.vmx", Schedule: "daily"},
		{Name: "notinstalled", VMX: "/vms/notinstalled.vmx", Schedule: "daily"},
		{Name: "differs", VMX: "/vms/differs.vmx", Schedule: "daily"},
		{Name: "notloaded", VMX: "/vms/notloaded.vmx", Schedule: "daily"},
	}
	seedVMs := []config.VM{vms[0], vms[2], vms[3]}
	if _, err := Sync(inst, seedVMs, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.differs", VMName: "differs", BinaryPath: "/changed"}); err != nil {
		t.Fatalf("re-Write() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.notloaded"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	if _, _, err := inst.Write(Agent{Label: "com.tim.snapback.orphan", VMName: "orphan"}); err != nil {
		t.Fatalf("seed orphan Write() error = %v", err)
	}

	report, err := CheckDrift(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(report.NotInstalled) != 1 || report.NotInstalled[0] != "notinstalled" {
		t.Errorf("report.NotInstalled = %v, want [\"notinstalled\"]", report.NotInstalled)
	}
	if len(report.OutOfSync) != 1 || report.OutOfSync[0] != "differs" {
		t.Errorf("report.OutOfSync = %v, want [\"differs\"]", report.OutOfSync)
	}
	if len(report.NotLoaded) != 1 || report.NotLoaded[0] != "notloaded" {
		t.Errorf("report.NotLoaded = %v, want [\"notloaded\"]", report.NotLoaded)
	}
	if len(report.Stale) != 1 || report.Stale[0] != "com.tim.snapback.orphan" {
		t.Errorf("report.Stale = %v, want [\"com.tim.snapback.orphan\"]", report.Stale)
	}
}

func TestCheckDrift_NeverCallsMutatingMethods(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback", neverRunning); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}
	if err := inst.Bootout("com.tim.snapback.dev"); err != nil {
		t.Fatalf("simulated manual Bootout() error = %v", err)
	}
	writesBefore, bootstrapsBefore, bootoutsBefore, removesBefore :=
		len(inst.WriteCalls), len(inst.BootstrapCalls), len(inst.BootoutCalls), len(inst.RemoveCalls)

	if _, err := CheckDrift(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("CheckDrift() error = %v", err)
	}
	if len(inst.WriteCalls) != writesBefore || len(inst.BootstrapCalls) != bootstrapsBefore ||
		len(inst.BootoutCalls) != bootoutsBefore || len(inst.RemoveCalls) != removesBefore {
		t.Errorf("CheckDrift made a mutating call -- Write/Bootstrap/Bootout/Remove counts changed from %d/%d/%d/%d",
			writesBefore, bootstrapsBefore, bootoutsBefore, removesBefore)
	}
}

func TestCheckDrift_ListError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	boom := errors.New("simulated list failure")
	inst.ListErr = boom

	_, err := CheckDrift(inst, nil, "/bin/snapback")
	if !errors.Is(err, boom) {
		t.Errorf("CheckDrift() error = %v, want it to wrap %v", err, boom)
	}
}

func TestCheckDrift_ReadError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	inst.ReadErr = errors.New("simulated read failure")
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	if _, err := CheckDrift(inst, vms, "/bin/snapback"); err == nil {
		t.Error("CheckDrift() error = nil, want the Read failure surfaced")
	}
}
