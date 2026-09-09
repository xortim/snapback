// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newStatusCmdWithDeps directly and inject a fake
// config loader and archive lister -- no real config file or destination
// directory needed.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

func TestSpliceTitleIntoTopBorder_EmbedsTitleKeepingLength(t *testing.T) {
	top := "╭" + strings.Repeat("─", 30) + "╮"
	got := spliceTitleIntoTopBorder(top, "myvm")

	if !strings.Contains(got, "myvm") {
		t.Errorf("got %q, want it to contain the title %q", got, "myvm")
	}
	if utf8.RuneCountInString(got) != utf8.RuneCountInString(top) {
		t.Errorf("got rune length %d, want unchanged length %d (must stay the same width as the box border)",
			utf8.RuneCountInString(got), utf8.RuneCountInString(top))
	}
	if !strings.HasPrefix(got, "╭─ myvm ") {
		t.Errorf("got %q, want it to start with the corner, a dash, then the title", got)
	}
	if !strings.HasSuffix(got, "╮") {
		t.Errorf("got %q, want it to still end with the closing corner", got)
	}
}

func TestSpliceTitleIntoTopBorder_TruncatesTitleTooLongToFit(t *testing.T) {
	top := "╭" + strings.Repeat("─", 10) + "╮"
	got := spliceTitleIntoTopBorder(top, "a very long virtual machine name")

	if utf8.RuneCountInString(got) != utf8.RuneCountInString(top) {
		t.Errorf("got rune length %d, want unchanged length %d", utf8.RuneCountInString(got), utf8.RuneCountInString(top))
	}
	if !strings.Contains(got, "…") {
		t.Errorf("got %q, want a truncation ellipsis when the title doesn't fit", got)
	}
}

func TestSpliceTitleIntoTopBorder_NoRoomAtAll_ReturnsUnchanged(t *testing.T) {
	top := "╭─╮"
	got := spliceTitleIntoTopBorder(top, "myvm")

	if got != top {
		t.Errorf("got %q, want the original border unchanged when there's no room for any title", got)
	}
}

func TestRenderCard_TitleInTopBorderAndBodyIsBoxed(t *testing.T) {
	got := renderCard("myvm", "hello world")
	lines := strings.Split(got, "\n")

	if len(lines) < 3 {
		t.Fatalf("renderCard output has %d lines, want at least 3 (top border, body, bottom border): %q", len(lines), got)
	}
	if !strings.Contains(lines[0], "myvm") {
		t.Errorf("top line = %q, want it to contain the title %q", lines[0], "myvm")
	}
	if !strings.HasPrefix(lines[0], "╭") {
		t.Errorf("top line = %q, want it to start with the rounded top-left corner", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "╰") {
		t.Errorf("last line = %q, want it to start with the rounded bottom-left corner", last)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("renderCard output = %q, want it to contain the body text", got)
	}
}

// writeVMXWithDisk writes a minimal .vmx with one virtual disk device --
// needed for the disk-consistency-warning tests, since
// backup.CheckVMDiskConsistency has nothing to check against a vmx with
// no disk device lines.
func writeVMXWithDisk(t *testing.T) (vmxPath string) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath = filepath.Join(bundle, "myvm.vmx")
	contents := "guestOS = \"ubuntu-64\"\nnvme0:0.fileName = \"disk.vmdk\"\n"
	if err := os.WriteFile(vmxPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	return vmxPath
}

// runningController returns a fake vm.Controller reporting ToolsRunning,
// which skips the disk-consistency check entirely -- used by summary
// tests that don't otherwise care about disk consistency, so they don't
// need a real vmx-with-disk fixture.
func runningController() (vm.Controller, error) {
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	return fake, nil
}

// newTestRootForStatus builds the real root command with a status
// subcommand wired to a fake deps -- see swapSubcommand for why it's
// built on NewRootCmd().
func newTestRootForStatus(t *testing.T, deps statusDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "status", newStatusCmdWithDeps(deps))
}

func TestStatusCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig:   func(string) (*config.Config, error) { return nil, errBoom },
		listArchives: func(string) ([]backup.Archive, error) { t.Fatal("listArchives should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestStatusCmd_UnknownVMName_ReturnsErrorWithoutListingArchives(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{VMs: []config.VM{{Name: "other-vm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { t.Fatal("listArchives should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "myvm") {
		t.Fatalf("Execute() error = %v, want an error naming the unconfigured VM %q", err, "myvm")
	}
}

func TestStatusCmd_ListArchivesError_IsWrapped(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		listArchives: func(string) ([]backup.Archive, error) { return nil, errBoom },
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), errBoom.Error()) || !strings.Contains(err.Error(), "list archives") {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"list archives\" context", err, errBoom)
	}
}

func TestStatusCmd_Summary_OneRowPerConfiguredVM(t *testing.T) {
	ts1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: "/dest",
				VMs: []config.VM{
					{Name: "backed-up-vm"},
					{Name: "never-backed-up-vm"},
				},
			}, nil
		},
		listArchives: func(destination string) ([]backup.Archive, error) {
			if destination != "/dest" {
				t.Errorf("listArchives called with %q, want %q", destination, "/dest")
			}
			return []backup.Archive{
				// Newest first, matching ListArchives' documented order.
				{ArchiveID: "backed-up-vm-2", Manifest: backup.Manifest{VMName: "backed-up-vm", SizeBytes: 3072, Timestamp: ts2}},
				{ArchiveID: "backed-up-vm-1", Manifest: backup.Manifest{VMName: "backed-up-vm", SizeBytes: 1024, Timestamp: ts1}},
			}, nil
		},
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: runningController,
	})
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout had %d lines, want 4 (header + 2 VM rows + drill-down footer): %q", len(lines), out.String())
	}
	backedUpRow := lines[1]
	for _, want := range []string{"backed-up-vm", ts2.Local().Format(time.RFC3339), "4.0 KiB", "2"} {
		if !strings.Contains(backedUpRow, want) {
			t.Errorf("backed-up-vm row = %q, want it to contain %q (newest timestamp, summed size, count)", backedUpRow, want)
		}
	}
	if strings.Contains(backedUpRow, ts1.Local().Format(time.RFC3339)) {
		t.Errorf("backed-up-vm row = %q, want the OLDER timestamp not to appear (last backup must be the newest)", backedUpRow)
	}
	neverBackedUpRow := lines[2]
	if !strings.Contains(neverBackedUpRow, "never-backed-up-vm") || !strings.Contains(neverBackedUpRow, "no backups yet") {
		t.Errorf("never-backed-up-vm row = %q, want VM name and \"no backups yet\"", neverBackedUpRow)
	}
	if !strings.Contains(lines[3], "status --vm") {
		t.Errorf("footer line = %q, want a hint pointing at the status --vm drill-down", lines[3])
	}
}

func TestStatusCmd_Summary_NotesDiscoveredVMNotInConfig(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "configured-vm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) {
			return []discoveredVM{{Name: "configured-vm"}, {Name: "new-vm"}}, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "new-vm") {
		t.Errorf("stderr = %q, want a note naming the undiscovered VM %q", errOut.String(), "new-vm")
	}
	if strings.Contains(errOut.String(), "configured-vm") {
		t.Errorf("stderr = %q, want no note for the already-configured VM", errOut.String())
	}
}

func TestStatusCmd_Summary_NoNoteWhenAllDiscoveredAreConfigured(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "configured-vm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) {
			return []discoveredVM{{Name: "configured-vm"}}, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if errOut.String() != "" {
		t.Errorf("stderr = %q, want empty when every discovered VM is already configured", errOut.String())
	}
}

func TestStatusCmd_Summary_DiscoveryErrorIsNotedNotFatal(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, errBoom },
		newController: runningController,
	})
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil -- a discovery failure must not break status's core job", err)
	}
	if !strings.Contains(errOut.String(), errBoom.Error()) {
		t.Errorf("stderr = %q, want it to note the discovery failure", errOut.String())
	}
	if !strings.Contains(out.String(), "myvm") {
		t.Errorf("stdout = %q, want the summary table still printed", out.String())
	}
}

func TestStatusCmd_VMFlag_DoesNotRunDiscovery(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { t.Fatal("searchDirs should not be called for --vm"); return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) {
			t.Fatal("discoverVMs should not be called for --vm")
			return nil, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
}

func TestStatusCmd_VMFlag_PrintsRetentionAndArchiveHistory(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: "/dest",
				Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
				VMs:         []config.VM{{Name: "myvm"}, {Name: "other-vm"}},
			}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) {
			return []backup.Archive{
				{
					ArchiveID: "myvm-1",
					Manifest: backup.Manifest{
						VMName: "myvm", SizeBytes: 2048,
						Timestamp: ts, ToolsState: vm.ToolsRunning,
					},
				},
				{ArchiveID: "other-vm-1", Manifest: backup.Manifest{VMName: "other-vm"}},
			}, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	if !strings.Contains(got, "myvm") {
		t.Errorf("stdout = %q, want the VM name in the card's border title", got)
	}
	for _, want := range []string{"keep last 5", "daily 7", "weekly 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want the retention policy stated in prose (missing %q)", got, want)
		}
	}
	if !strings.Contains(got, "fully consistent") {
		t.Errorf("stdout = %q, want a consistency sentence for the newest archive (tools were running)", got)
	}
	for _, want := range []string{ts.Local().Format(time.RFC3339), "2.0 KiB", string(vm.ToolsRunning)} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	// other-vm's archive has a zero-value Timestamp -- its RFC3339 form
	// would show up distinctively if archivesForVM's per-VM filtering ever
	// regressed and both VMs' archives got mixed into one table.
	if strings.Contains(got, "0001-01-01") {
		t.Errorf("stdout = %q, want it to contain only myvm's archives, not other-vm's", got)
	}
}

func TestStatusCmd_VMFlag_CrashConsistentWhenToolsNotRunning(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) {
			return []backup.Archive{
				{ArchiveID: "myvm-1", Manifest: backup.Manifest{VMName: "myvm", ToolsState: vm.ToolsNotInstalled}},
			}, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	if !strings.Contains(got, "crash-consistent") {
		t.Errorf("stdout = %q, want a crash-consistent consistency sentence when the newest archive's tools weren't running", got)
	}
	if strings.Contains(got, "fully consistent") {
		t.Errorf("stdout = %q, want no \"fully consistent\" wording for a crash-consistent backup", got)
	}
}

func TestStatusCmd_VMFlag_PrintsTotalSizeAcrossArchives(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) {
			return []backup.Archive{
				{ArchiveID: "myvm-2", Manifest: backup.Manifest{VMName: "myvm", SizeBytes: 3072}},
				{ArchiveID: "myvm-1", Manifest: backup.Manifest{VMName: "myvm", SizeBytes: 1024}},
			}, nil
		},
		newController: runningController,
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	if !strings.Contains(got, "Total size") || !strings.Contains(got, "4.0 KiB") {
		t.Errorf("stdout = %q, want a total-size line summing both archives to 4.0 KiB", got)
	}
}

func TestStatusCmd_VMFlag_NoBackupsYet(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: "/dest",
				Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
				VMs:         []config.VM{{Name: "myvm"}},
			}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		newController: runningController,
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "no backups yet") {
		t.Errorf("stdout = %q, want a no-backups-yet message", out.String())
	}
	if strings.Contains(out.String(), "consistent") {
		t.Errorf("stdout = %q, want no consistency sentence when there's no archive to describe", out.String())
	}
	got := out.String()
	for _, want := range []string{"keep last 5", "daily 7", "weekly 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want the retention policy still stated in prose even with no archives yet (missing %q)", got, want)
		}
	}
}

func TestStatusCmd_RejectsExtraPositionalArgs(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig:   func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		listArchives: func(string) ([]backup.Archive, error) { t.Fatal("listArchives should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"status", "unexpected-arg"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for an unexpected positional argument")
	}
}

func TestStatusCmd_Summary_WarnsWhenDiskChainNeedsRepair(t *testing.T) {
	vmxPath := writeVMXWithDisk(t)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: func() (vm.Controller, error) {
			fake := vm.NewFakeVMController()
			fake.ToolsState = vm.ToolsInstalled
			fake.DiskConsistencyErr = errBoom
			return fake, nil
		},
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "myvm") {
		t.Errorf("stderr = %q, want a warning: line naming the damaged VM %q", errOut.String(), "myvm")
	}
}

func TestStatusCmd_Summary_NoWarningWhenDiskHealthy(t *testing.T) {
	vmxPath := writeVMXWithDisk(t)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: func() (vm.Controller, error) {
			fake := vm.NewFakeVMController()
			fake.ToolsState = vm.ToolsInstalled
			return fake, nil
		},
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if errOut.String() != "" {
		t.Errorf("stderr = %q, want empty for a healthy disk chain", errOut.String())
	}
}

func TestStatusCmd_Summary_NoWarningForRunningVM(t *testing.T) {
	vmxPath := writeVMXWithDisk(t)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: func() (vm.Controller, error) {
			fake := vm.NewFakeVMController()
			fake.ToolsState = vm.ToolsRunning
			fake.DiskConsistencyErr = errBoom // must not matter -- a running VM's disk files are locked
			return fake, nil
		},
	})
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if strings.Contains(errOut.String(), "warning:") {
		t.Errorf("stderr = %q, want no warning for a running VM (its disk files are locked, not actually checked)", errOut.String())
	}
}

func TestStatusCmd_VMFlag_WarnsWhenDiskChainNeedsRepair(t *testing.T) {
	vmxPath := writeVMXWithDisk(t)
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm", VMX: vmxPath}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
		newController: func() (vm.Controller, error) {
			fake := vm.NewFakeVMController()
			fake.ToolsState = vm.ToolsInstalled
			fake.DiskConsistencyErr = errBoom
			return fake, nil
		},
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "myvm") {
		t.Errorf("stderr = %q, want a warning: line naming the damaged VM %q for the --vm view too", errOut.String(), "myvm")
	}
}

func TestStatusCmd_Summary_DiskCheckFactoryErrorIsNotedNotFatal(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives:  func(string) ([]backup.Archive, error) { return nil, nil },
		searchDirs:    func() []string { return nil },
		discoverVMs:   func([]string) ([]discoveredVM, error) { return nil, nil },
		newController: func() (vm.Controller, error) { return nil, errBoom },
	})
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil -- a disk-check factory failure must not break status's core job", err)
	}
	if !strings.Contains(errOut.String(), errBoom.Error()) {
		t.Errorf("stderr = %q, want it to note the factory failure", errOut.String())
	}
	if !strings.Contains(out.String(), "myvm") {
		t.Errorf("stdout = %q, want the summary table still printed", out.String())
	}
}
