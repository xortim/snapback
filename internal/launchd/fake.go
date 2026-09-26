package launchd

import (
	"bytes"
	"sort"
	"strings"
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
	ReadErr      error
	IsLoadedErr  error

	// BootstrapFailAt, if non-zero, restricts BootstrapErr to only the
	// call'th call to Bootstrap (1-indexed) -- every other call succeeds.
	// Lets a test exercise Sync's error handling partway through a loop
	// of multiple VMs (BootstrapErr alone is sticky and would fail every
	// call once set, never letting an earlier VM in the same Sync
	// succeed).
	BootstrapFailAt int

	// WriteCalls/BootstrapCalls/BootoutCalls/RemoveCalls/ReadCalls/
	// IsLoadedCalls record every call to that method, in order, so tests
	// can assert not just that something happened but exactly what and
	// how many times. Each is appended to before its method's configured
	// *Err (if any) is returned, matching
	// vm.FakeVMController.CheckDiskConsistency's record-then-return
	// convention -- so a failed call still shows up here for a test to
	// assert against.
	WriteCalls     []string // labels passed to Write
	BootstrapCalls []string // plistPaths passed to Bootstrap
	BootoutCalls   []string // labels passed to Bootout
	RemoveCalls    []string // labels passed to Remove
	ReadCalls      []string // labels passed to Read
	IsLoadedCalls  []string // labels passed to IsLoaded

	// Calls is a single ordered log across every method above (e.g.
	// "write:<label>", "bootstrap:<path>", "bootout:<label>",
	// "remove:<label>", "read:<label>", "isloaded:<label>"), for tests
	// that need to assert relative ordering between different methods --
	// the per-method slices above can't show, for example, that a
	// Bootout happened before a Bootstrap.
	Calls []string

	plists map[string][]byte // label -> last-written plist content
	loaded map[string]bool   // label -> currently bootstrapped, per Bootstrap/Bootout
}

// NewFakeInstaller returns a FakeInstaller with nothing installed.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{plists: make(map[string][]byte), loaded: make(map[string]bool)}
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

// fakePlistPathPrefix/fakePlistPathSuffix bracket the synthetic path
// Write returns above -- Bootstrap only receives that path (mirroring
// the real Installer, whose Bootstrap likewise takes a path, not a
// label), so it recovers the label by trimming them back off, purely
// for this fake's own loaded-state bookkeeping.
const (
	fakePlistPathPrefix = "/fake/LaunchAgents/"
	fakePlistPathSuffix = ".plist"
)

func labelFromFakePlistPath(plistPath string) string {
	return strings.TrimSuffix(strings.TrimPrefix(plistPath, fakePlistPathPrefix), fakePlistPathSuffix)
}

func (f *FakeInstaller) Bootstrap(plistPath string) error {
	f.BootstrapCalls = append(f.BootstrapCalls, plistPath)
	f.Calls = append(f.Calls, "bootstrap:"+plistPath)
	if f.BootstrapErr != nil && (f.BootstrapFailAt == 0 || len(f.BootstrapCalls) == f.BootstrapFailAt) {
		return f.BootstrapErr
	}
	f.loaded[labelFromFakePlistPath(plistPath)] = true
	return nil
}

func (f *FakeInstaller) Bootout(label string) error {
	f.BootoutCalls = append(f.BootoutCalls, label)
	f.Calls = append(f.Calls, "bootout:"+label)
	if f.BootoutErr != nil {
		return f.BootoutErr
	}
	f.loaded[label] = false
	return nil
}

func (f *FakeInstaller) Remove(label string) error {
	f.RemoveCalls = append(f.RemoveCalls, label)
	f.Calls = append(f.Calls, "remove:"+label)
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	delete(f.plists, label)
	delete(f.loaded, label)
	return nil
}

func (f *FakeInstaller) Read(label string) ([]byte, bool, error) {
	f.ReadCalls = append(f.ReadCalls, label)
	f.Calls = append(f.Calls, "read:"+label)
	if f.ReadErr != nil {
		return nil, false, f.ReadErr
	}
	data, ok := f.plists[label]
	return data, ok, nil
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

func (f *FakeInstaller) IsLoaded(label string) (bool, error) {
	f.IsLoadedCalls = append(f.IsLoadedCalls, label)
	f.Calls = append(f.Calls, "isloaded:"+label)
	if f.IsLoadedErr != nil {
		return false, f.IsLoadedErr
	}
	return f.loaded[label], nil
}
