package vm

import (
	"errors"
	"strings"
	"testing"
)

var _ Controller = (*VMCLIController)(nil)

// fakeRun records every invocation and dispatches to a caller-supplied
// handler keyed by the vmx path + module/verb, so each test only has to
// describe the calls it cares about.
type fakeRun struct {
	calls   [][]string
	handler func(args []string) (stdout, stderr []byte, err error)
}

func (f *fakeRun) run(args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	return f.handler(args)
}

const testVMX = "/vms/example.vmwarevm/example.vmx"

func TestVMCLIController_CheckToolsState_RunningMapsToToolsRunning(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"running":true,"runningStatus":"running","installType":"ovt"}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.CheckToolsState(testVMX)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != ToolsRunning {
		t.Errorf("CheckToolsState() = %q, want %q", got, ToolsRunning)
	}
	if len(fr.calls) != 1 || fr.calls[0][0] != testVMX || fr.calls[0][1] != "Tools" || fr.calls[0][2] != "Query" {
		t.Errorf("run() called with %v, want [%s Tools Query ...]", fr.calls, testVMX)
	}
}

func TestVMCLIController_CheckToolsState_InstalledNotRunningMapsToToolsInstalled(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"running":false,"runningStatus":"notRunning","installType":"ovt"}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.CheckToolsState(testVMX)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != ToolsInstalled {
		t.Errorf("CheckToolsState() = %q, want %q", got, ToolsInstalled)
	}
}

func TestVMCLIController_CheckToolsState_NoInstallTypeMapsToToolsUnknown(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"running":false,"runningStatus":"notRunning","installType":""}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.CheckToolsState(testVMX)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != ToolsUnknown {
		t.Errorf("CheckToolsState() = %q, want %q", got, ToolsUnknown)
	}
}

func TestVMCLIController_CheckToolsState_NoneInstallTypeMapsToToolsUnknown(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"running":false,"runningStatus":"notRunning","installType":"none"}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.CheckToolsState(testVMX)
	if err != nil {
		t.Fatalf("CheckToolsState() error = %v, want nil", err)
	}
	if got != ToolsUnknown {
		t.Errorf("CheckToolsState() = %q, want %q (\"none\" is a known no-tools sentinel, not an installed value)", got, ToolsUnknown)
	}
}

func TestVMCLIController_CheckToolsState_CommandErrorReturnsStderrMessage(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return nil, []byte("vmcli: VMX : 'x.vmx' does not exist!"), errors.New("exit status 255")
	}}
	c := &VMCLIController{run: fr.run}

	_, err := c.CheckToolsState(testVMX)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("CheckToolsState() error = %v, want it to contain the stderr message", err)
	}
}

func TestVMCLIController_Snapshot_Success(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte("Progress: 100% (100 out of 100)"), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	if err := c.Snapshot(testVMX, "snapback-1"); err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("run() called %d times, want 1 (Take only, no cleanup query on success)", len(fr.calls))
	}
	call := fr.calls[0]
	if call[0] != testVMX || call[1] != "Snapshot" || call[2] != "Take" || call[len(call)-1] != "snapback-1" {
		t.Errorf("run() called with %v, want Take of snapback-1", call)
	}
}

func TestVMCLIController_Snapshot_FailureWithNoPartialSnapshot_ReturnsErrorWithoutDeleting(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		if len(args) >= 3 && args[1] == "Snapshot" && args[2] == "Take" {
			return nil, []byte("vmcli: snapshot failed"), errors.New("exit status 255")
		}
		if len(args) >= 3 && args[1] == "Snapshot" && args[2] == "query" {
			return []byte(`{"currentUID":0,"helperUID":0,"snapshots":[]}`), nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.Snapshot(testVMX, "snapback-1")
	if err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Errorf("Snapshot() error = %v, want it to contain the underlying failure", err)
	}
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			t.Errorf("run() called Delete %v, want no cleanup delete when no partial snapshot exists", call)
		}
	}
}

func TestVMCLIController_Snapshot_FailureWithPartialSnapshot_CleansUpBeforeReturningError(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "Take":
			return nil, []byte("vmcli: snapshot failed partway"), errors.New("exit status 255")
		case args[1] == "Snapshot" && args[2] == "query":
			return []byte(`{"currentUID":7,"helperUID":0,"snapshots":[{"displayName":"snapback-1","parentUID":0,"uid":7}]}`), nil, nil
		case args[1] == "Snapshot" && args[2] == "Delete":
			return nil, nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.Snapshot(testVMX, "snapback-1")
	if err == nil || !strings.Contains(err.Error(), "snapshot failed partway") {
		t.Errorf("Snapshot() error = %v, want it to contain the underlying failure", err)
	}
	var deleted bool
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			deleted = true
			if call[3] != "7" {
				t.Errorf("Delete called with uid %v, want 7", call[3])
			}
		}
	}
	if !deleted {
		t.Error("Snapshot() did not clean up the partial snapshot before returning")
	}
}

func TestVMCLIController_ListSnapshots_ParsesDisplayNames(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"currentUID":2,"helperUID":0,"snapshots":[
			{"displayName":"snapback-1","parentUID":0,"uid":1},
			{"displayName":"snapback-2","parentUID":1,"uid":2}
		]}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.ListSnapshots(testVMX)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v, want nil", err)
	}
	want := []string{"snapback-1", "snapback-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ListSnapshots() = %v, want %v", got, want)
	}
}

func TestVMCLIController_ListSnapshots_EmptyList(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"currentUID":0,"helperUID":0,"snapshots":[]}`), nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	got, err := c.ListSnapshots(testVMX)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty", got)
	}
}

func TestVMCLIController_DeleteSnapshot_LooksUpUIDAndDeletes(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		switch {
		case args[1] == "Snapshot" && args[2] == "query":
			return []byte(`{"currentUID":3,"helperUID":0,"snapshots":[{"displayName":"snapback-1","parentUID":0,"uid":3}]}`), nil, nil
		case args[1] == "Snapshot" && args[2] == "Delete":
			return nil, nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	if err := c.DeleteSnapshot(testVMX, "snapback-1"); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v, want nil", err)
	}
	var found bool
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			found = true
			if call[3] != "3" {
				t.Errorf("Delete called with uid %v, want 3", call[3])
			}
		}
	}
	if !found {
		t.Error("DeleteSnapshot() never called Delete")
	}
}

func TestVMCLIController_DeleteSnapshot_NotFoundReturnsErrorWithoutCallingDelete(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		if args[1] == "Snapshot" && args[2] == "query" {
			return []byte(`{"currentUID":0,"helperUID":0,"snapshots":[]}`), nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.DeleteSnapshot(testVMX, "does-not-exist")
	if err == nil {
		t.Fatal("DeleteSnapshot() error = nil, want not-found error")
	}
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			t.Errorf("run() called Delete %v, want no delete for a not-found snapshot", call)
		}
	}
}

func TestVMCLIController_DeleteSnapshot_AmbiguousNameReturnsErrorWithoutDeleting(t *testing.T) {
	fr := &fakeRun{handler: func(args []string) ([]byte, []byte, error) {
		if args[1] == "Snapshot" && args[2] == "query" {
			return []byte(`{"currentUID":5,"helperUID":0,"snapshots":[
				{"displayName":"snapback-1","parentUID":0,"uid":3},
				{"displayName":"snapback-1","parentUID":3,"uid":5}
			]}`), nil, nil
		}
		t.Fatalf("unexpected call: %v", args)
		return nil, nil, nil
	}}
	c := &VMCLIController{run: fr.run}

	err := c.DeleteSnapshot(testVMX, "snapback-1")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("DeleteSnapshot() error = %v, want it to report the ambiguous name", err)
	}
	for _, call := range fr.calls {
		if call[1] == "Snapshot" && call[2] == "Delete" {
			t.Errorf("run() called Delete %v, want no delete when the target name is ambiguous", call)
		}
	}
}

func TestFindVMCLI_EnvOverrideTakesPrecedence(t *testing.T) {
	t.Setenv("SNAPBACK_VMCLI_PATH", "/custom/vmcli")

	got, err := findVMCLI()
	if err != nil {
		t.Fatalf("findVMCLI() error = %v, want nil", err)
	}
	if got != "/custom/vmcli" {
		t.Errorf("findVMCLI() = %q, want %q", got, "/custom/vmcli")
	}
}

func TestFindVMCLI_NotFoundReturnsError(t *testing.T) {
	t.Setenv("SNAPBACK_VMCLI_PATH", "")
	t.Setenv("PATH", t.TempDir())
	orig := vmcliCandidatePaths
	vmcliCandidatePaths = []string{"/nonexistent/vmcli-probe-path"}
	defer func() { vmcliCandidatePaths = orig }()

	_, err := findVMCLI()
	if err == nil {
		t.Fatal("findVMCLI() error = nil, want not-found error")
	}
}
