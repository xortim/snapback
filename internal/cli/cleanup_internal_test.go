// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newCleanupCmdWithDeps directly and inject a fake
// config loader and vm.Controller -- no real config file or Fusion
// install needed.
package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

func TestCleanupCmd_MissingVMFlag_ReturnsError(t *testing.T) {
	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig:    func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		newController: func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil },
	}))
	root.SetArgs([]string{"cleanup"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing required --vm flag")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout = %q, want usage text for a flag-misuse error", out.String())
	}
}

func TestCleanupCmd_UnknownVMName_ReturnsError(t *testing.T) {
	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "other-vm"}}}, nil
		},
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	}))
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for an unconfigured VM name")
	}
	if !strings.Contains(err.Error(), "myvm") {
		t.Errorf("Execute() error = %v, want it to mention the requested VM name", err)
	}
}

func TestCleanupCmd_DependencyError_IsWrappedAndSuppressesUsage(t *testing.T) {
	tests := []struct {
		name string
		deps func(t *testing.T) cleanupDeps
	}{
		{
			name: "config load error",
			deps: func(t *testing.T) cleanupDeps {
				return cleanupDeps{
					loadConfig: func(path string) (*config.Config, error) { return nil, errBoom },
					newController: func() (vm.Controller, error) {
						t.Fatal("newController should not be called")
						return nil, nil
					},
				}
			},
		},
		{
			name: "controller connect error",
			deps: func(t *testing.T) cleanupDeps {
				return cleanupDeps{
					loadConfig: func(path string) (*config.Config, error) {
						return &config.Config{VMs: []config.VM{{Name: "myvm"}}}, nil
					},
					newController: func() (vm.Controller, error) { return nil, errBoom },
				}
			},
		},
		{
			name: "list snapshots error",
			deps: func(t *testing.T) cleanupDeps {
				fake := vm.NewFakeVMController()
				fake.ListSnapshotsErr = errBoom
				return cleanupDeps{
					loadConfig: func(path string) (*config.Config, error) {
						return &config.Config{VMs: []config.VM{{Name: "myvm", VMX: "/vms/myvm.vmwarevm/myvm.vmx"}}}, nil
					},
					newController: func() (vm.Controller, error) { return fake, nil },
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(tt.deps(t)))
			root.SetArgs([]string{"cleanup", "--vm", "myvm"})
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want a wrapped error")
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
			}
			if strings.Contains(out.String(), "Usage:") {
				t.Errorf("stdout = %q, want no usage text for an operational error", out.String())
			}
		})
	}
}

func TestCleanupCmd_NoOrphans_ReportsNoneFound(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "no orphaned snapshots found") {
		t.Errorf("stdout = %q, want a no-orphans message", out.String())
	}
}

func TestCleanupCmd_RemovesOnlySnapbackPrefixedSnapshots(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := fake.Snapshot(vmx, "manual-checkpoint"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "snapback-20260101T000000Z") {
		t.Errorf("stdout = %q, want it to report the removed orphan", out.String())
	}

	remaining, err := fake.ListSnapshots(vmx)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "manual-checkpoint" {
		t.Errorf("ListSnapshots() = %v, want only the non-snapback snapshot to remain", remaining)
	}
}

func TestCleanupCmd_DeleteFailure_ReturnsWrappedError(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	fake.DeleteSnapshotErr = errBoom

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped delete-snapshot error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
	}
	if !strings.Contains(err.Error(), "snapback-20260101T000000Z") {
		t.Errorf("Execute() error = %v, want it to name the snapshot that failed to delete", err)
	}
}

func TestCleanupCmd_LockHeld_SkipsWithoutErrorAndDoesNotDelete(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: dest, VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil (a held lock is skipped, not an error)", err)
	}
	if !strings.Contains(out.String(), "in progress") {
		t.Errorf("stdout = %q, want a message about a backup in progress", out.String())
	}

	remaining, lerr := fake.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 1 {
		t.Errorf("ListSnapshots() = %v, want the snapshot untouched while the lock was held", remaining)
	}
}

func TestCleanupCmd_MultipleOrphans_DeletesInOneBatchCall(t *testing.T) {
	const vmx = "/vms/myvm.vmwarevm/myvm.vmx"
	fake := vm.NewFakeVMController()
	if err := fake.Snapshot(vmx, "snapback-20260101T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := fake.Snapshot(vmx, "snapback-20260102T000000Z"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	root := swapSubcommand(t, "cleanup", newCleanupCmdWithDeps(cleanupDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{Destination: t.TempDir(), VMs: []config.VM{{Name: "myvm", VMX: vmx}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	}))
	var out bytes.Buffer
	root.SetArgs([]string{"cleanup", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if strings.Count(out.String(), "removed orphaned snapshot") != 2 {
		t.Errorf("stdout = %q, want both orphans reported removed", out.String())
	}
	remaining, lerr := fake.ListSnapshots(vmx)
	if lerr != nil {
		t.Fatalf("ListSnapshots() error = %v", lerr)
	}
	if len(remaining) != 0 {
		t.Errorf("ListSnapshots() = %v, want both orphans removed", remaining)
	}
}
