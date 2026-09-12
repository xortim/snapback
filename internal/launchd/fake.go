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

	// BootstrapCalls/BootoutCalls/RemoveCalls record every call, in
	// order, so tests can assert not just that something happened but
	// exactly what and how many times.
	BootstrapCalls []string // plistPaths passed to Bootstrap
	BootoutCalls   []string // labels passed to Bootout
	RemoveCalls    []string // labels passed to Remove

	plists map[string][]byte // label -> last-written plist content
}

// NewFakeInstaller returns a FakeInstaller with nothing installed.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{plists: make(map[string][]byte)}
}

func (f *FakeInstaller) Write(agent Agent) (string, bool, error) {
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
	if f.BootstrapErr != nil {
		return f.BootstrapErr
	}
	f.BootstrapCalls = append(f.BootstrapCalls, plistPath)
	return nil
}

func (f *FakeInstaller) Bootout(label string) error {
	if f.BootoutErr != nil {
		return f.BootoutErr
	}
	f.BootoutCalls = append(f.BootoutCalls, label)
	return nil
}

func (f *FakeInstaller) Remove(label string) error {
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.RemoveCalls = append(f.RemoveCalls, label)
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
