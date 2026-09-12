package launchd

import (
	"bytes"
	"sort"
)

// FakeInstaller is an in-memory Installer for unit tests, mirroring
// vm.FakeVMController's role: Sync's choreography is written and tested
// against this, never against LaunchctlInstaller directly.
type FakeInstaller struct {
	WriteErr     error
	BootstrapErr error
	BootoutErr   error
	RemoveErr    error
	ListErr      error

	// BootstrapFailAt, if non-zero, restricts BootstrapErr to only the
	// call'th call to Bootstrap (1-indexed) -- every other call succeeds.
	// Lets a test exercise Sync's error handling partway through a loop
	// of multiple VMs (BootstrapErr alone is sticky and would fail every
	// call once set, never letting an earlier VM in the same Sync
	// succeed).
	BootstrapFailAt int

	// WriteCalls/BootstrapCalls/BootoutCalls/RemoveCalls record every
	// call to that method, in order, so tests can assert not just that
	// something happened but exactly what and how many times. Each is
	// appended to before its method's configured *Err (if any) is
	// returned, matching vm.FakeVMController.CheckDiskConsistency's
	// record-then-return convention -- so a failed call still shows up
	// here for a test to assert against.
	WriteCalls     []string // labels passed to Write
	BootstrapCalls []string // plistPaths passed to Bootstrap
	BootoutCalls   []string // labels passed to Bootout
	RemoveCalls    []string // labels passed to Remove

	// Calls is a single ordered log across all four methods above (e.g.
	// "write:<label>", "bootstrap:<path>", "bootout:<label>",
	// "remove:<label>"), for tests that need to assert relative
	// ordering between different methods -- the per-method slices above
	// can't show, for example, that a Bootout happened before a
	// Bootstrap.
	Calls []string

	plists map[string][]byte // label -> last-written plist content
}

// NewFakeInstaller returns a FakeInstaller with nothing installed.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{plists: make(map[string][]byte)}
}

func (f *FakeInstaller) Write(agent Agent) (string, bool, error) {
	f.WriteCalls = append(f.WriteCalls, agent.Label)
	f.Calls = append(f.Calls, "write:"+agent.Label)
	if f.WriteErr != nil {
		return "", false, f.WriteErr
	}
	data, err := renderPlist(agent)
	if err != nil {
		return "", false, err
	}
	existing, ok := f.plists[agent.Label]
	changed := !ok || !bytes.Equal(existing, data)
	f.plists[agent.Label] = data
	return "/fake/LaunchAgents/" + agent.Label + ".plist", changed, nil
}

func (f *FakeInstaller) Bootstrap(plistPath string) error {
	f.BootstrapCalls = append(f.BootstrapCalls, plistPath)
	f.Calls = append(f.Calls, "bootstrap:"+plistPath)
	if f.BootstrapErr != nil && (f.BootstrapFailAt == 0 || len(f.BootstrapCalls) == f.BootstrapFailAt) {
		return f.BootstrapErr
	}
	return nil
}

func (f *FakeInstaller) Bootout(label string) error {
	f.BootoutCalls = append(f.BootoutCalls, label)
	f.Calls = append(f.Calls, "bootout:"+label)
	if f.BootoutErr != nil {
		return f.BootoutErr
	}
	return nil
}

func (f *FakeInstaller) Remove(label string) error {
	f.RemoveCalls = append(f.RemoveCalls, label)
	f.Calls = append(f.Calls, "remove:"+label)
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	delete(f.plists, label)
	return nil
}

func (f *FakeInstaller) List() ([]string, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	labels := make([]string, 0, len(f.plists))
	for l := range f.plists {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels, nil
}
