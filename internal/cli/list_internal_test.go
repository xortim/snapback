// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newListCmdWithDeps directly and inject a fake config
// loader and archive lister -- no real config file or destination
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
)

// newTestRootForList builds the real root command via NewRootCmd() -- so
// the --config persistent flag can't drift from root.go -- then swaps in
// a list subcommand wired to fake deps.
func newTestRootForList(t *testing.T, deps listDeps) *cobra.Command {
	t.Helper()
	root := NewRootCmd()
	removed := false
	for _, sub := range root.Commands() {
		if sub.Name() == "list" {
			root.RemoveCommand(sub)
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("newTestRootForList: NewRootCmd() has no \"list\" subcommand to replace")
	}
	root.AddCommand(newListCmdWithDeps(deps))
	return root
}

func TestListCmd_NoArchives_PrintsMessage(t *testing.T) {
	root := newTestRootForList(t, listDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		listArchives: func(string) ([]backup.Archive, error) { return nil, nil },
	})
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "no backup archives found") {
		t.Errorf("stdout = %q, want a message about no archives", out.String())
	}
}

func TestListCmd_PrintsArchiveTable(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	root := newTestRootForList(t, listDeps{
		loadConfig: func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		listArchives: func(destination string) ([]backup.Archive, error) {
			if destination != "/dest" {
				t.Errorf("listArchives called with %q, want %q", destination, "/dest")
			}
			return []backup.Archive{
				{
					ArchiveID: "myvm-20260304T050607Z",
					Manifest: backup.Manifest{
						VMName:    "myvm",
						SizeBytes: 2048,
						Comment:   "nightly",
						Timestamp: ts,
					},
				},
			}, nil
		},
	})
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	for _, want := range []string{"myvm-20260304T050607Z", "myvm", "nightly", "2.0 KiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
}

func TestListCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	root := newTestRootForList(t, listDeps{
		loadConfig:   func(string) (*config.Config, error) { return nil, errBoom },
		listArchives: func(string) ([]backup.Archive, error) { t.Fatal("listArchives should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"list"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestListCmd_ListArchivesError_IsWrapped(t *testing.T) {
	root := newTestRootForList(t, listDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		listArchives: func(string) ([]backup.Archive, error) { return nil, errBoom },
	})
	root.SetArgs([]string{"list"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q", err, errBoom)
	}
}
