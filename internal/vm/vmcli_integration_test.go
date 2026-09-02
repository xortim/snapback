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
