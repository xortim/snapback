// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newVMAddCmdWithDeps/newVMRemoveCmdWithDeps directly
// and inject fake dependencies -- no real config file, VM scan, or
// terminal needed.
package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/tui"
)

// newTestRootForVM builds the real root command with a "vm" subcommand
// (add + remove) wired to fake deps -- see swapSubcommand for why it's
// built on NewRootCmd().
func newTestRootForVM(t *testing.T, deps vmDeps) *cobra.Command {
	t.Helper()
	vmCmd := &cobra.Command{Use: "vm"}
	vmCmd.AddCommand(newVMAddCmdWithDeps(deps), newVMRemoveCmdWithDeps(deps))
	return swapSubcommand(t, "vm", vmCmd)
}

// fakeVMDeps returns a vmDeps whose writeFile captures its argument into
// written, and whose loadConfig/discoverVMs/addVMs are controlled by the
// caller.
func fakeVMDeps(cfg *config.Config, candidates []discoveredVM, added []config.VM, written *[]byte, writtenPath *string) vmDeps {
	return vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return cfg, nil },
		marshal:     config.Marshal,
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return candidates, nil },
		writeFile: func(path string, data []byte) error {
			*writtenPath = path
			*written = data
			return nil
		},
		isTerminal: func(io.Writer) bool { return false },
		addVMs: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) ([]config.VM, error) {
			return added, nil
		},
	}
}

func TestVMAddCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	deps := vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return nil, errBoom },
		discoverVMs: func([]string) ([]discoveredVM, error) { t.Fatal("discoverVMs should not be called"); return nil, nil },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestVMAddCmd_DiscoverVMsError_IsWrapped(t *testing.T) {
	deps := vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, errBoom },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "discover VMs") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"discover VMs\" context", err, errBoom)
	}
}

func TestVMAddCmd_FiltersOutAlreadyConfiguredCandidates(t *testing.T) {
	var gotCandidates []tui.VMCandidate
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "existing", VMX: "/vms/existing.vmx"}}}, nil
		},
		searchDirs: func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) {
			return []discoveredVM{
				{Name: "existing", VMX: "/vms/existing.vmx"},
				{Name: "new-vm", VMX: "/vms/new-vm.vmx"},
			}, nil
		},
		isTerminal: func(io.Writer) bool { return false },
		addVMs: func(_ context.Context, _ io.Reader, _ io.Writer, _ bool, candidates []tui.VMCandidate) ([]config.VM, error) {
			gotCandidates = candidates
			return nil, nil
		},
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(gotCandidates) != 1 || gotCandidates[0].Name != "new-vm" {
		t.Errorf("candidates passed to addVMs = %+v, want only the not-yet-configured %q", gotCandidates, "new-vm")
	}
}

func TestVMAddCmd_SearchDirFlag_AppendedAfterDefaults(t *testing.T) {
	var gotSearchDirs []string
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		searchDirs: func() []string { return []string{"/default/a"} },
		discoverVMs: func(dirs []string) ([]discoveredVM, error) {
			gotSearchDirs = dirs
			return nil, nil
		},
		isTerminal: func(io.Writer) bool { return false },
		addVMs: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) ([]config.VM, error) {
			return nil, nil
		},
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml", "--search-dir", "/extra/one"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := []string{"/default/a", "/extra/one"}
	if len(gotSearchDirs) != len(want) || gotSearchDirs[0] != want[0] || gotSearchDirs[1] != want[1] {
		t.Errorf("search dirs passed to discoverVMs = %v, want %v", gotSearchDirs, want)
	}
}

func TestVMAddCmd_NoneAdded_PrintsMessageWithoutWriting(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeVMDeps(&config.Config{Destination: "/dest"}, nil, nil, &written, &writtenPath)

	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "no VMs added") {
		t.Errorf("stdout = %q, want a \"no VMs added\" message", out.String())
	}
	if written != nil {
		t.Error("writeFile was called, want no write when nothing was added")
	}
}

func TestVMAddCmd_AppendsAddedVMsAndWrites(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "existing", VMX: "/vms/existing.vmx"}}}
	added := []config.VM{{Name: "new-vm", VMX: "/vms/new-vm.vmx"}}
	deps := fakeVMDeps(cfg, []discoveredVM{{Name: "new-vm", VMX: "/vms/new-vm.vmx"}}, added, &written, &writtenPath)

	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	if !strings.Contains(string(written), "name: existing") || !strings.Contains(string(written), "name: new-vm") {
		t.Errorf("written config = %q, want both the pre-existing and newly added VM", written)
	}
	if !strings.Contains(out.String(), "added 1 VM(s)") {
		t.Errorf("stdout = %q, want a confirmation naming how many VMs were added", out.String())
	}
}

func TestVMAddCmd_ManualEntryDuplicatingExistingName_ReturnsError(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "existing", VMX: "/vms/existing.vmx"}}}
	// addVMs (a fake here) returns a manually-entered VM that collides
	// with the name already in cfg.VMs -- discovery-based filtering can't
	// catch this since it only ever sees discovered candidates, not
	// manual entries, so runVMAdd's own config.ValidateVMs on the merged
	// list is what must catch it.
	added := []config.VM{{Name: "existing", VMX: "/vms/other-path.vmx"}}
	deps := fakeVMDeps(cfg, nil, added, &written, &writtenPath)

	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Execute() error = %v, want it to mention \"invalid VM selection\" and \"duplicate\"", err)
	}
	if written != nil {
		t.Error("writeFile was called, want no write when the merged VM list is invalid")
	}
}

func TestVMAddCmd_AddVMsError_IsPropagatedUnwrapped(t *testing.T) {
	deps := vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		isTerminal:  func(io.Writer) bool { return false },
		addVMs: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) ([]config.VM, error) {
			return nil, errBoom
		},
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to be (or wrap) %v", err, errBoom)
	}
}

func TestVMRemoveCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	deps := vmDeps{loadConfig: func(string) (*config.Config, error) { return nil, errBoom }}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "myvm", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestVMRemoveCmd_UnknownName_ReturnsError(t *testing.T) {
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "other-vm"}}}, nil
		},
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "myvm", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "myvm") {
		t.Fatalf("Execute() error = %v, want an error naming the unconfigured VM %q", err, "myvm")
	}
}

func TestVMRemoveCmd_RemovesNamedVMAndWrites(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: "/dest",
				VMs:         []config.VM{{Name: "keep-me", VMX: "/vms/keep.vmx"}, {Name: "remove-me", VMX: "/vms/remove.vmx"}},
			}, nil
		},
		marshal: config.Marshal,
		writeFile: func(path string, data []byte) error {
			writtenPath = path
			written = data
			return nil
		},
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "remove-me", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	if strings.Contains(string(written), "remove-me") {
		t.Errorf("written config = %q, want \"remove-me\" gone", written)
	}
	if !strings.Contains(string(written), "keep-me") {
		t.Errorf("written config = %q, want \"keep-me\" to remain", written)
	}
	if !strings.Contains(out.String(), `removed "remove-me"`) {
		t.Errorf("stdout = %q, want a confirmation naming the removed VM", out.String())
	}
}

func TestVMRemoveCmd_RequiresExactlyOneArg(t *testing.T) {
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
	}
	for _, args := range [][]string{
		{"vm", "remove", "--config", "/cfg/config.yaml"},
		{"vm", "remove", "one", "two", "--config", "/cfg/config.yaml"},
	} {
		root := newTestRootForVM(t, deps)
		root.SetArgs(args)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})

		if err := root.Execute(); err == nil {
			t.Errorf("Execute() with args %v error = nil, want an error for the wrong number of positional args", args)
		}
	}
}
