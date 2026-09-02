package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func makeVMwareVM(t *testing.T, dir, name string, withMatchingVMX bool) {
	t.Helper()
	bundle := filepath.Join(dir, name+".vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("failed to create bundle dir: %v", err)
	}
	if withMatchingVMX {
		if err := os.WriteFile(filepath.Join(bundle, name+".vmx"), []byte(""), 0o644); err != nil {
			t.Fatalf("failed to create .vmx: %v", err)
		}
	}
}

func TestDiscoverVMs_FindsBundleWithMatchingVMX(t *testing.T) {
	dir := t.TempDir()
	makeVMwareVM(t, dir, "myvm", true)

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "myvm" || got[0].VMX != filepath.Join(dir, "myvm.vmwarevm", "myvm.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for myvm", got)
	}
}

func TestDiscoverVMs_SkipsBundleWithoutMatchingVMX(t *testing.T) {
	dir := t.TempDir()
	makeVMwareVM(t, dir, "myvm", false)

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries (bundle has no matching .vmx)", got)
	}
}

func TestDiscoverVMs_SkipsNonexistentDir(t *testing.T) {
	got, err := discoverVMs([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v, want nil (missing dir is not an error)", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries", got)
	}
}

func TestDiscoverVMs_SortsByNameAcrossMultipleDirs(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	makeVMwareVM(t, dirA, "zebra", true)
	makeVMwareVM(t, dirB, "alpha", true)

	got, err := discoverVMs([]string{dirA, dirB})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zebra" {
		t.Errorf("discoverVMs = %+v, want [alpha, zebra] sorted", got)
	}
}

func TestDefaultVMSearchDirs_IncludesVirtualMachinesUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dirs := defaultVMSearchDirs()
	want := filepath.Join(home, "Virtual Machines")
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("defaultVMSearchDirs() = %v, want [%q]", dirs, want)
	}
}

func TestDiscoverVMs_FollowsSymlinkedBundle(t *testing.T) {
	realParent := t.TempDir()
	makeVMwareVM(t, realParent, "myvm", true)

	searchDir := t.TempDir()
	link := filepath.Join(searchDir, "myvm.vmwarevm")
	if err := os.Symlink(filepath.Join(realParent, "myvm.vmwarevm"), link); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	got, err := discoverVMs([]string{searchDir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "myvm" || got[0].VMX != filepath.Join(link, "myvm.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for myvm via the symlink", got)
	}
}

func TestDiscoverVMs_MatchesCaseInsensitiveSuffix(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "MyVM.VMwareVM")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("failed to create bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "MyVM.vmx"), []byte(""), 0o644); err != nil {
		t.Fatalf("failed to create .vmx: %v", err)
	}

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "MyVM" || got[0].VMX != filepath.Join(bundle, "MyVM.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for MyVM despite the differently-cased extension", got)
	}
}

func TestDiscoverVMs_SkipsVMXThatIsADirectory(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "myvm.vmwarevm")
	if err := os.MkdirAll(filepath.Join(bundle, "myvm.vmx"), 0o755); err != nil {
		t.Fatalf("failed to create myvm.vmx as a directory: %v", err)
	}

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries (myvm.vmx is a directory, not a file)", got)
	}
}
