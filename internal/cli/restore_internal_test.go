// Package cli (internal test package, not cli_test) so this file can call
// newRestoreCmdWithDeps directly and inject a fake config loader and
// vm.Controller -- mirrors run_internal_test.go.
package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

func newTestRestoreRoot(t *testing.T, deps restoreDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "restore", newRestoreCmdWithDeps(deps))
}

// writeFixtureArchive writes a real backup archive (via backup.Run against
// a fake controller) to destination, for restore CLI tests that need a
// resolvable archive-id or --vm/--latest match. Returns the archive ID.
func writeFixtureArchive(t *testing.T, destination, vmName string) (archiveID string) {
	t.Helper()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	result, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName:      vmName,
		VMXPath:     vmxPath,
		Destination: destination,
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}
	return result.ArchiveID
}

func TestRestoreCmd_NoSelector_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error when neither archive-id nor --vm/--latest is given")
	}
}

func TestRestoreCmd_BothSelectors_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", "myvm-x", "--vm", "myvm", "--latest"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error when both an archive-id and --vm/--latest are given")
	}
}

func TestRestoreCmd_VMWithoutLatest_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", "--vm", "myvm"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for --vm without --latest")
	}
}

func TestRestoreCmd_LatestWithoutVM_ReturnsUsageError(t *testing.T) {
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig:    func(string) (*config.Config, error) { return &config.Config{Destination: t.TempDir()}, nil },
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", "--latest"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for --latest without --vm")
	}
}

func TestRestoreCmd_MissingVMInConfig_RequiresDest(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil // no VMs configured
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", archiveID})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when the archive's VM isn't in config and --dest is absent")
	}
	if !strings.Contains(err.Error(), "myvm") {
		t.Errorf("Execute() error = %v, want it to name the missing VM", err)
	}
}

func TestRestoreCmd_HappyPath_ArchiveID_PrintsTargetPath(t *testing.T) {
	destination := t.TempDir()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	result, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName: "myvm", VMXPath: vmxPath, Destination: destination, Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination, VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	root.SetArgs([]string{"restore", result.ArchiveID})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want it to report restore completion", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
}

func TestRestoreCmd_HappyPath_VMLatest_PrintsTargetPath(t *testing.T) {
	destination := t.TempDir()
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	if _, err := backup.Run(context.Background(), fake, progress.NoOpReporter{}, backup.Options{
		VMName: "myvm", VMXPath: vmxPath, Destination: destination, Compression: "gzip",
	}); err != nil {
		t.Fatalf("backup.Run() (fixture setup) error = %v", err)
	}

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination, VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	root.SetArgs([]string{"restore", "--vm", "myvm", "--latest"})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want it to report restore completion", out.String())
	}
}

func TestRestoreCmd_DestOverride_SkipsConfigLookup(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")
	dest := t.TempDir()

	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil // no VMs configured -- --dest must make this unnecessary
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
	})
	root.SetArgs([]string{"restore", archiveID, "--dest", dest})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), dest) {
		t.Errorf("stdout = %q, want the restored path under --dest %q", out.String(), dest)
	}
}

func TestRestoreCmd_InteractiveTerminal_UsesRestoreInteractiveAndSkipsPlainPrint(t *testing.T) {
	destination := t.TempDir()
	archiveID := writeFixtureArchive(t, destination, "myvm")
	dest := t.TempDir()

	var calledWithLabel string
	root := newTestRestoreRoot(t, restoreDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: destination}, nil
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
		isTerminal:    func(io.Writer) bool { return true },
		restoreInteractive: func(out io.Writer, label string, cancel context.CancelFunc, restoreFn func(progress.Reporter) (*backup.RestoreResult, error)) (*backup.RestoreResult, error) {
			calledWithLabel = label
			return restoreFn(progress.NoOpReporter{})
		},
	})
	root.SetArgs([]string{"restore", archiveID, "--dest", dest})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if calledWithLabel != archiveID {
		t.Errorf("restoreInteractive called with label = %q, want %q", calledWithLabel, archiveID)
	}
	if strings.Contains(out.String(), "restore complete:") {
		t.Errorf("stdout = %q, want no plain \"restore complete\" line -- the interactive renderer owns that", out.String())
	}
}
