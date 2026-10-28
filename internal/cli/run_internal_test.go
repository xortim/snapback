// Package cli (internal test package, not cli_test like root_test.go) so
// this file can call newRunCmdWithDeps directly and inject a fake config
// loader and vm.Controller -- no real config file or Fusion install needed.
package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/notify"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
	"github.com/xortim/snapback/internal/vm"
)

// newTestRoot builds the real root command with a run subcommand wired to
// a fake deps -- see swapSubcommand for why it's built on NewRootCmd().
// Its --config flag inherits the real default (~/.config/snapback/config.yaml),
// so any test using the real config.Load loader must pass --config explicitly
// or set HOME to a scratch dir -- otherwise it silently reads (and passes or
// fails based on) the developer's actual config file.
func newTestRoot(t *testing.T, deps runDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "run", newRunCmdWithDeps(deps))
}

func writeVMBundle(t *testing.T) (vmxPath string) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath = filepath.Join(bundle, "myvm.vmx")
	vmxContent := "guestOS = \"ubuntu-64\"\nscsi0:0.fileName = \"disk.vmdk\"\n"
	if err := os.WriteFile(vmxPath, []byte(vmxContent), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "disk.vmdk"), []byte("fake disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	return vmxPath
}

func TestRunCmd_MissingVMFlag_ReturnsError(t *testing.T) {
	root := newTestRoot(t, runDeps{
		loadConfig:    func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		newController: func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"run"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing required --vm flag")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout = %q, want usage text for a flag-misuse error", out.String())
	}
}

func TestRunCmd_DependencyError_IsWrappedAndSuppressesUsage(t *testing.T) {
	tests := []struct {
		name string
		deps func(t *testing.T) runDeps
	}{
		{
			name: "config load error",
			deps: func(t *testing.T) runDeps {
				return runDeps{
					loadConfig: func(path string) (*config.Config, error) { return nil, errBoom },
					newController: func() (vm.Controller, error) {
						t.Fatal("newController should not be called")
						return nil, nil
					},
				}
			},
		},
		{
			name: "controller connect error",
			deps: func(t *testing.T) runDeps {
				return runDeps{
					loadConfig: func(path string) (*config.Config, error) {
						return &config.Config{VMs: []config.VM{{Name: "myvm"}}}, nil
					},
					newController: func() (vm.Controller, error) { return nil, errBoom },
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRoot(t, tt.deps(t))
			root.SetArgs([]string{"run", "--vm", "myvm"})
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want a wrapped error")
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
			}
			if strings.Contains(out.String(), "Usage:") {
				t.Errorf("stdout = %q, want no usage text for an operational error", out.String())
			}
		})
	}
}

func TestRunCmd_ConfigLoadError_DoesNotDuplicatePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := newTestRoot(t, runDeps{
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

func TestRunCmd_ConfigLoadError_MalformedYAML_NamesPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := newTestRoot(t, runDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	badPath := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(badPath, []byte("destination: [unterminated"), 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	root.SetArgs([]string{"run", "--vm", "myvm", "--config", badPath})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a config parse error")
	}
	if !strings.Contains(err.Error(), badPath) {
		t.Errorf("Execute() error = %q, want it to name the config path %q", err.Error(), badPath)
	}
}

func TestRunCmd_UnknownVMName_ReturnsError(t *testing.T) {
	root := newTestRoot(t, runDeps{
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

func TestRunCmd_HappyPath_PrintsArchivePath(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
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

func TestRunCmd_InteractiveTerminal_UsesRunInteractiveAndSkipsPlainPrint(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	var calledWithVMName string
	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return true },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			calledWithVMName = vmName
			return backupFn(progress.NoOpReporter{})
		},
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if calledWithVMName != "myvm" {
		t.Errorf("runInteractive called with vmName = %q, want %q", calledWithVMName, "myvm")
	}
	if strings.Contains(out.String(), "backup complete:") {
		t.Errorf("stdout = %q, want no plain \"backup complete\" line -- the interactive renderer owns that", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
}

func TestRunCmd_InteractiveTerminal_MergeFailure_WarnsAboutPossibleOrphanOnStderr(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return true },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return backupFn(progress.NoOpReporter{})
		},
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

// TestRunCmd_InteractiveTerminal_IncompleteRun_StillPrintsAnError covers
// tui.ErrInteractiveRunIncomplete: the TUI exited without ever rendering
// its own "error: ..." line (see tui.RunInteractive's doc comment), so
// runVM must NOT set cmd.SilenceErrors here -- otherwise cobra's own
// default "Error: ..." print is suppressed too and the run fails with no
// message at all.
func TestRunCmd_InteractiveTerminal_IncompleteRun_StillPrintsAnError(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return true },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return nil, tui.ErrInteractiveRunIncomplete
		},
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want ErrInteractiveRunIncomplete")
	}
	if !strings.Contains(errOut.String(), "interactive run ended before the backup finished") {
		t.Errorf("stderr = %q, want cobra's own error line since the TUI never rendered one", errOut.String())
	}
}

func TestRunCmd_NilIsTerminal_UsesPlainOutput(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		// isTerminal and runInteractive both left nil.
	})
	var out bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "backup complete:") {
		t.Errorf("stdout = %q, want the plain completion line when isTerminal is nil", out.String())
	}
}

func TestRunCmd_RotatesOversizedLogBeforeBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logDir := filepath.Join(home, "Library", "Logs", "snapback")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	logPath := filepath.Join(logDir, "dev.log")
	if err := os.WriteFile(logPath, make([]byte, 6*1024*1024), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deps := runDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}, nil
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
		isTerminal:    func(io.Writer) bool { return false },
	}
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "dev", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	_ = root.Execute() // the fake controller/backup.Run may or may not succeed fully; rotation happens before that regardless

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("oversized log at %s still exists after run, want it rotated to %s.1", logPath, logPath)
	}
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Errorf(".1 rotated log missing: %v", err)
	}
}

func TestDefaultIsTerminal_NonFileWriter_ReturnsFalse(t *testing.T) {
	if defaultIsTerminal(&bytes.Buffer{}) {
		t.Error("defaultIsTerminal(*bytes.Buffer) = true, want false")
	}
}

func TestDefaultIsTerminal_RegularFile_ReturnsFalse(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer func() { _ = f.Close() }()
	if defaultIsTerminal(f) {
		t.Error("defaultIsTerminal(regular *os.File) = true, want false")
	}
}

func TestRunCmd_MergeFailure_WarnsAboutPossibleOrphanOnStderr(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom // fails at Stage: Merging, which is >= Snapshotting

	root := newTestRoot(t, runDeps{
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

func notifyTestDeps(t *testing.T, cfg *config.Config, fake *vm.FakeVMController, tty bool, n *notify.Fake) runDeps {
	t.Helper()
	return runDeps{
		loadConfig:    func(string) (*config.Config, error) { return cfg, nil },
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return tty },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return backupFn(progress.NoOpReporter{})
		},
		newNotifier: func() notify.Notifier { return n },
	}
}

func TestRunCmd_Notifications_Gating(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		onSuccess bool
		tty       bool
		fail      bool
		wantSent  bool
	}{
		{"disabled, success", false, true, false, false, false},
		{"disabled, failure", false, true, false, true, false},
		{"tty, success", true, true, true, false, false},
		{"tty, failure", true, true, true, true, false},
		{"non-tty, success, on_success", true, true, false, false, true},
		{"non-tty, success, !on_success", true, false, false, false, false},
		{"non-tty, failure, on_success", true, true, false, true, true},
		{"non-tty, failure, !on_success", true, false, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmxPath := writeVMBundle(t)
			ctrl := vm.NewFakeVMController()
			ctrl.ToolsState = vm.ToolsRunning
			if tt.fail {
				ctrl.DeleteSnapshotErr = errBoom
			}
			cfg := &config.Config{
				Destination:   t.TempDir(),
				Compression:   "gzip",
				VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
				Notifications: config.Notifications{Enabled: tt.enabled, OnSuccess: tt.onSuccess},
			}
			n := &notify.Fake{}
			root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, tt.tty, n))
			root.SetArgs([]string{"run", "--vm", "myvm"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if (err != nil) != tt.fail {
				t.Fatalf("Execute() error = %v, want failure=%v", err, tt.fail)
			}
			if got := len(n.Sent) == 1; got != tt.wantSent {
				t.Fatalf("sent %d notifications, want sent=%v", len(n.Sent), tt.wantSent)
			}
		})
	}
}

func TestRunCmd_Notifications_SuccessContent(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true, OnSuccess: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := n.Sent[0]
	if got.Subtitle != "myvm backed up" || got.Message == "" || got.Message == "backup complete" || got.Sound != "" {
		t.Errorf("notification = %+v, want success subtitle and the archive ID as message, no sound", got)
	}
}

func TestRunCmd_Notifications_OrphanFailureIncludesCleanupHint(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	ctrl.DeleteSnapshotErr = errBoom // Stage: Merging, >= Snapshotting
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	_ = root.Execute()
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	got := n.Sent[0]
	if got.Subtitle != "myvm backup failed" || got.Sound != notify.FailureSound {
		t.Errorf("notification = %+v, want failure subtitle and sound", got)
	}
	if !strings.Contains(got.Message, "snapback cleanup --vm myvm") {
		t.Errorf("Message = %q, want cleanup hint", got.Message)
	}
}

func TestRunCmd_Notifications_UnknownVMNotifiesWithoutCleanupHint(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	deps := notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, n)
	deps.newController = func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil }
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want unknown-VM error")
	}
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	if strings.Contains(n.Sent[0].Message, "cleanup") {
		t.Errorf("Message = %q, want no cleanup hint before any snapshot was taken", n.Sent[0].Message)
	}
}

func TestRunCmd_Notifications_NotifierErrorWarnsAndKeepsOutcome(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true, OnSuccess: true},
	}
	n := &notify.Fake{Err: errors.New("osascript exploded")}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	var errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errOut)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil -- a notification failure must not fail the run", err)
	}
	if !strings.Contains(errOut.String(), "warning: could not send notification: ") || !strings.Contains(errOut.String(), "osascript exploded") {
		t.Errorf("stderr = %q, want a notification warning", errOut.String())
	}
}

func TestRunCmd_Notifications_CanceledContextStillNotifies(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = root.ExecuteContext(ctx)
	if len(n.Contexts) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Contexts))
	}
	if err := n.Contexts[0].Err(); err != nil {
		t.Errorf("ctx passed to Notify is already done (%v), want it detached from the command's cancellation", err)
	}
}

func TestRunCmd_Notifications_NilNotifierNeverPanics(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	deps := notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, nil)
	deps.newNotifier = nil
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	_ = root.Execute() // must not panic
}

func TestSnapshotMayRemain(t *testing.T) {
	if snapshotMayRemain(errBoom) {
		t.Error("plain error: got true, want false")
	}
	if snapshotMayRemain(&backup.RunError{Stage: progress.CheckingTools, Err: errBoom}) {
		t.Error("CheckingTools: got true, want false")
	}
	if !snapshotMayRemain(&backup.RunError{Stage: progress.Merging, Err: errBoom}) {
		t.Error("Merging: got false, want true")
	}
}
