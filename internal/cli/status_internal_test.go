// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newStatusCmdWithDeps directly and inject a fake
// config loader and archive lister -- no real config file or destination
// directory needed.
package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/vm"
)

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
	})
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout had %d lines, want 3 (header + 2 VM rows): %q", len(lines), out.String())
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
						VMName: "myvm", SizeBytes: 2048, Comment: "nightly",
						Timestamp: ts, ToolsState: vm.ToolsRunning,
					},
				},
				{ArchiveID: "other-vm-1", Manifest: backup.Manifest{VMName: "other-vm"}},
			}, nil
		},
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	if !strings.Contains(got, "keep last 5") || !strings.Contains(got, "keep daily 7") || !strings.Contains(got, "keep weekly 4") {
		t.Errorf("stdout = %q, want it to print the configured retention policy", got)
	}
	for _, want := range []string{"myvm-1", ts.Local().Format(time.RFC3339), "2.0 KiB", string(vm.ToolsRunning), "nightly"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "other-vm-1") {
		t.Errorf("stdout = %q, want it to contain only myvm's archives, not other-vm's", got)
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
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "total size: 4.0 KiB") {
		t.Errorf("stdout = %q, want a \"total size: 4.0 KiB\" line summing both archives", out.String())
	}
}

func TestStatusCmd_VMFlag_NoBackupsYet(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
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
}

func TestStatusCmd_VMFlag_SanitizesCommentForTable(t *testing.T) {
	root := newTestRootForStatus(t, statusDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "myvm"}}}, nil
		},
		listArchives: func(string) ([]backup.Archive, error) {
			return []backup.Archive{
				{ArchiveID: "myvm-1", Manifest: backup.Manifest{VMName: "myvm", Comment: "line1\tline2\nline3"}},
			}, nil
		},
	})
	root.SetArgs([]string{"status", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// retention line + total size line + table header + one archive row.
	if len(lines) != 4 {
		t.Fatalf("stdout had %d lines, want 4 -- an unsanitized embedded newline in Comment would split it into a fifth line: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[3], "line1 line2 line3") {
		t.Errorf("row = %q, want Comment's embedded tab/newline replaced with spaces (\"line1 line2 line3\")", lines[3])
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
