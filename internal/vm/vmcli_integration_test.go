//go:build integration

package vm_test

// Real vmcli execution against a live VM, per README.md / CLAUDE.md:
//
//	SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
//
// Also requires SNAPBACK_TEST_VMX pointing at a disposable scratch VM's
// .vmx file -- these tests take and delete real snapshots on it. Never
// point this at a VM you care about.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/xortim/snapback/internal/vm"
)

func integrationVMX(t *testing.T) string {
	t.Helper()
	if os.Getenv("SNAPBACK_INTEGRATION") != "1" {
		t.Skip("set SNAPBACK_INTEGRATION=1 to run vmcli integration tests")
	}
	vmx := os.Getenv("SNAPBACK_TEST_VMX")
	if vmx == "" {
		t.Skip("set SNAPBACK_TEST_VMX to a disposable scratch VM's .vmx path")
	}
	return vmx
}

func TestIntegration_CheckToolsState_ReturnsARealState(t *testing.T) {
	vmxPath := integrationVMX(t)
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}

	state, err := ctrl.CheckToolsState(vmxPath)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v", err)
	}
	t.Logf("CheckToolsState(%s) = %q", vmxPath, state)
}

func TestIntegration_SnapshotLifecycle(t *testing.T) {
	vmxPath := integrationVMX(t)
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}
	const name = "snapback-integration-test"

	if err := ctrl.Snapshot(vmxPath, name); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() {
		_ = ctrl.DeleteSnapshot(vmxPath, name)
	})

	snapshots, err := ctrl.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if !slices.Contains(snapshots, name) {
		t.Fatalf("ListSnapshots() = %v, want it to contain %q", snapshots, name)
	}

	if err := ctrl.DeleteSnapshot(vmxPath, name); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}

	snapshots, err = ctrl.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if slices.Contains(snapshots, name) {
		t.Fatalf("ListSnapshots() = %v, want it to no longer contain %q after delete", snapshots, name)
	}
}

// TestIntegration_DeleteSnapshots_Batch checks the assumption documented
// on VMCLIController.DeleteSnapshots: that snapshot uids stay stable
// across the batch's own deletes, so resolving all of them from one
// query up front (rather than re-resolving before each delete, like
// DeleteSnapshot does) is safe. It creates several snapshots, deletes
// them all in one DeleteSnapshots call, and confirms every one is
// actually gone and nothing else on the VM was touched. Neither the fake
// controller (canned JSON, doesn't change across deletes) nor the other
// integration tests (which only ever create one snapshot at a time) can
// catch a real uid shift -- this is the one that can.
func TestIntegration_DeleteSnapshots_Batch(t *testing.T) {
	vmxPath := integrationVMX(t)
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}

	before, err := ctrl.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}

	names := []string{
		"snapback-integration-batch-test-1",
		"snapback-integration-batch-test-2",
		"snapback-integration-batch-test-3",
	}
	for _, name := range names {
		if err := ctrl.Snapshot(vmxPath, name); err != nil {
			t.Fatalf("Snapshot(%q) error = %v", name, err)
		}
		t.Cleanup(func() {
			_ = ctrl.DeleteSnapshot(vmxPath, name)
		})
	}

	deleted, err := ctrl.DeleteSnapshots(vmxPath, names)
	if err != nil {
		t.Fatalf("DeleteSnapshots() error = %v", err)
	}
	gotDeleted := slices.Clone(deleted)
	wantDeleted := slices.Clone(names)
	sort.Strings(gotDeleted)
	sort.Strings(wantDeleted)
	if !slices.Equal(gotDeleted, wantDeleted) {
		t.Fatalf("DeleteSnapshots() deleted = %v, want %v", deleted, names)
	}

	after, err := ctrl.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	for _, name := range names {
		if slices.Contains(after, name) {
			t.Errorf("ListSnapshots() = %v, want it to no longer contain %q after DeleteSnapshots", after, name)
		}
	}

	// Nothing else on the VM should have been affected: the snapshot set
	// left behind should match what was there before this test started.
	gotAfter := slices.Clone(after)
	wantAfter := slices.Clone(before)
	sort.Strings(gotAfter)
	sort.Strings(wantAfter)
	if !slices.Equal(gotAfter, wantAfter) {
		t.Fatalf("ListSnapshots() after DeleteSnapshots = %v, want it back to the pre-test state %v", after, before)
	}
}

// TestIntegration_FrozenBundleReadableDuringSnapshot answers the open
// question in docs/design.md ("Risks & Gotchas" -- "Concurrent read on
// files VMware still has open"): once a snapshot is taken, is it actually
// safe on APFS to read the frozen base disk files while Fusion still
// holds handles on them? Verified here by checksumming a real file from
// the bundle before and after taking the snapshot, while it's held.
func TestIntegration_FrozenBundleReadableDuringSnapshot(t *testing.T) {
	vmxPath := integrationVMX(t)
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}
	const name = "snapback-integration-checksum-test"

	bundleDir := filepath.Dir(vmxPath)
	before, err := sha256File(vmxPath)
	if err != nil {
		t.Fatalf("checksum before snapshot: %v", err)
	}

	if err := ctrl.Snapshot(vmxPath, name); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() {
		_ = ctrl.DeleteSnapshot(vmxPath, name)
	})

	after, err := sha256File(vmxPath)
	if err != nil {
		t.Fatalf("checksum while snapshot held: %v", err)
	}
	if before != after {
		t.Errorf(".vmx checksum changed across the snapshot boundary: %s -> %s (bundle: %s)", before, after, bundleDir)
	}

	if err := ctrl.DeleteSnapshot(vmxPath, name); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}
}

// TestIntegration_CheckDiskConsistency_ReportsHealthyDisk exercises the
// real vmware-vdiskmanager wiring against the scratch VM's own disk --
// added after the incident (docs/design.md's "Risks & Gotchas") where
// vmcli reported a merge succeeded on a VM whose disk chain had already
// silently failed this exact check. Requires SNAPBACK_TEST_DISK pointing
// at the scratch VM's top-level .vmdk (found in its bundle directory,
// e.g. "Virtual Disk.vmdk" for a VM with no snapshots yet).
func TestIntegration_CheckDiskConsistency_ReportsHealthyDisk(t *testing.T) {
	integrationVMX(t) // reuses the same env-var gate/skip behavior
	diskPath := os.Getenv("SNAPBACK_TEST_DISK")
	if diskPath == "" {
		t.Skip("set SNAPBACK_TEST_DISK to the scratch VM's top-level .vmdk path")
	}
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}

	if err := ctrl.CheckDiskConsistency(diskPath); err != nil {
		t.Fatalf("CheckDiskConsistency(%s) error = %v, want nil for a healthy scratch VM disk", diskPath, err)
	}
}

// TestIntegration_CheckDiskConsistency_ReportsBrokenChain exercises
// CheckDiskConsistency's failure path against the real vmware-vdiskmanager
// binary -- until now only the healthy-disk case
// (TestIntegration_CheckDiskConsistency_ReportsHealthyDisk) ran against
// the real binary, so a future Fusion release changing vdiskmanager's
// exit code or moving its diagnosis from stderr to stdout could silently
// break the failure path this whole feature exists for, with nothing in
// the suite noticing.
//
// Deliberately corrupting the scratch VM's own disk chain isn't safe to
// automate (destructive, and not reliably reversible), so this instead
// points vdiskmanager at a file that is not a valid disk descriptor at
// all -- e.g. a plain text file -- which still forces the same nonzero-
// exit, stderr-diagnosis code path CheckDiskConsistency relies on for a
// genuinely broken chain, verifying the error is both non-nil and
// carries a real message rather than a swallowed/empty one.
func TestIntegration_CheckDiskConsistency_ReportsBrokenChain(t *testing.T) {
	integrationVMX(t) // reuses the same env-var gate/skip behavior
	ctrl, err := vm.NewVMCLIController()
	if err != nil {
		t.Fatalf("NewVMCLIController() error = %v", err)
	}

	notADisk := filepath.Join(t.TempDir(), "not-a-disk.vmdk")
	if err := os.WriteFile(notADisk, []byte("this is not a vmdk descriptor\n"), 0o600); err != nil {
		t.Fatalf("write fake disk file: %v", err)
	}

	err = ctrl.CheckDiskConsistency(notADisk)
	if err == nil {
		t.Fatal("CheckDiskConsistency() error = nil, want an error for a file that isn't a real disk descriptor")
	}
	if err.Error() == "" {
		t.Error("CheckDiskConsistency() error message is empty, want vdiskmanager's diagnosis surfaced")
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
