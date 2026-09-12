// Package cli (internal test package) so this file can call
// newScheduleSyncCmdWithDeps directly with fake deps -- no real config
// file or launchctl needed.
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
)

func newTestRootForSchedule(t *testing.T, deps scheduleDeps) *cobra.Command {
	t.Helper()
	scheduleCmd := &cobra.Command{Use: "schedule"}
	scheduleCmd.AddCommand(newScheduleSyncCmdWithDeps(deps))
	return swapSubcommand(t, "schedule", scheduleCmd)
}

func TestScheduleSyncCmd_NothingScheduled_PrintsNothingToDo(t *testing.T) {
	deps := scheduleDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("stdout = %q, want \"nothing to do\"", out.String())
	}
}

func TestScheduleSyncCmd_InstallsNewSchedule(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "installed: dev") {
		t.Errorf("stdout = %q, want \"installed: dev\"", out.String())
	}
}

func TestScheduleSyncCmd_Removed_PrintsVMNameNotRawLabel(t *testing.T) {
	// Pre-install "dev", then sync against a config that no longer has it.
	inst := launchd.NewFakeInstaller()
	if _, err := launchd.Sync(inst, []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}, "/bin/snapback"); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	deps := scheduleDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		newInstaller: func() (launchd.Installer, error) { return inst, nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "removed: dev\n") {
		t.Errorf("stdout = %q, want \"removed: dev\"", out.String())
	}
	if strings.Contains(out.String(), "com.tim.snapback.") {
		t.Errorf("stdout = %q, want the raw launchd label prefix stripped", out.String())
	}
}

func TestScheduleSyncCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) { return nil, errBoom },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestScheduleSyncCmd_CollisionError_IsPropagated(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{
				{Name: "My VM!", VMX: "/vms/a.vmx", Schedule: "daily"},
				{Name: "My VM?", VMX: "/vms/b.vmx", Schedule: "weekly"},
			}}, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want the name collision propagated")
	}
}
