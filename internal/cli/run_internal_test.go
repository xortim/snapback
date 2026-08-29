// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newRunCmdWithDeps directly and inject a fake config
// loader and vm.Controller -- no real config file or Fusion install needed.
package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

var errBoom = errors.New("boom")

// newTestRoot builds a minimal parent command carrying the persistent
// --config flag NewRootCmd() normally registers, with the run subcommand
// (built from deps) attached -- mirrors how run.go's runVM reads --config
// via cmd.Flags().GetString, without needing every other real subcommand.
func newTestRoot(deps runDeps) *cobra.Command {
	root := &cobra.Command{Use: "snapback"}
	root.PersistentFlags().String("config", "unused.yaml", "path to config file")
	root.AddCommand(newRunCmdWithDeps(deps))
	return root
}

func writeVMBundle(t *testing.T) (vmxPath string) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath = filepath.Join(bundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "disk.vmdk"), []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	return vmxPath
}

func TestRunCmd_MissingVMFlag_ReturnsError(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig:    func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		newController: func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"run"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing required --vm flag")
	}
}

func TestRunCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) { return nil, errBoom },
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped config load error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
	}
}

func TestRunCmd_UnknownVMName_ReturnsError(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "other-vm"}}}, nil
		},
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	root.SetArgs([]string{"run", "--vm", "myvm"})
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

func TestRunCmd_ControllerConnectError_IsWrapped(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		newController: func() (vm.Controller, error) { return nil, errBoom },
	})
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped controller connect error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
	}
}

func TestRunCmd_HappyPath_PrintsArchivePath(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath, CommentTemplate: "nightly"}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "backup complete:") {
		t.Errorf("stdout = %q, want it to report backup completion", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
}

func TestRunCmd_MergeFailure_WarnsAboutPossibleOrphanOnStderr(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom // fails at Stage: Merging, which is >= Snapshotting

	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want the wrapped delete-snapshot failure")
	}
	if !strings.Contains(errOut.String(), "may remain") {
		t.Errorf("stderr = %q, want an orphaned-snapshot warning", errOut.String())
	}
}

func TestRunCmd_ConfigLoadError_DoesNotDuplicatePath(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	root.SetArgs([]string{"run", "--vm", "myvm", "--config", missingPath})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a config load error")
	}
	if got := strings.Count(err.Error(), missingPath); got != 1 {
		t.Errorf("Execute() error = %q, want the config path to appear exactly once, got %d occurrences", err.Error(), got)
	}
}
