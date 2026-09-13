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
	"time"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
	"github.com/xortim/snapback/internal/tui"
)

func newTestRootForInit(t *testing.T, deps initDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "init", newInitCmdWithDeps(deps))
}

// fakeInitDeps returns an initDeps whose writeFile captures its argument
// into written, and whose fileExists/discoverVMs/runWizard are
// controlled by the caller -- covers the common case where a test only
// cares about what init would have written, not real disk I/O or
// prompting.
func fakeInitDeps(candidates []discoveredVM, exists bool, written *[]byte, writtenPath *string, cfg *config.Config) initDeps {
	return initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return candidates, nil },
		loadConfig:  func(string) (*config.Config, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile: func(path string, data []byte) error {
			*writtenPath = path
			*written = data
			return nil
		},
		fileExists: func(string) bool { return exists },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return cfg, nil
		},
	}
}

func TestWriteConfigFile_CreatesParentDirAndWritesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.yaml")
	want := []byte("destination: /Volumes/Backups/snapback\n")

	if err := writeConfigFile(path, want); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("written content = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("file permissions = %o, want %o", perm, 0o644)
	}
}

func TestConfigFileExists(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	missing := filepath.Join(dir, "missing.yaml")

	if !configFileExists(existing) {
		t.Errorf("configFileExists(%q) = false, want true", existing)
	}
	if configFileExists(missing) {
		t.Errorf("configFileExists(%q) = true, want false", missing)
	}
}

func TestInitCmd_ExistingConfig_WithoutForce_Errors(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, true, &written, &writtenPath, &config.Config{Destination: "/dest", Compression: "zstd"})

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Execute() error = %v, want a message about the existing config and --force", err)
	}
	if written != nil {
		t.Errorf("writeFile was called, want init to refuse before writing")
	}
}

func TestInitCmd_ContextCancelledDuringDiscovery_StopsInsteadOfHanging(t *testing.T) {
	blockUntilCancelled := make(chan struct{})
	deps := initDeps{
		searchDirs: func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) {
			<-blockUntilCancelled // stands in for a scan stalled on an unresponsive volume
			return nil, nil
		},
		loadConfig: func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:    config.Marshal,
		writeFile:  func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists: func(string) bool { return false },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			t.Fatal("runWizard should not be called")
			return nil, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	t.Cleanup(func() { close(blockUntilCancelled) })

	err := root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "init cancelled") {
		t.Fatalf("ExecuteContext() error = %v, want an \"init cancelled\" error instead of hanging on a stalled scan", err)
	}
}

func TestInitCmd_DiscoverVMsError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, errBoom },
		loadConfig:  func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			t.Fatal("runWizard should not be called")
			return nil, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "discover VMs") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"discover VMs\" context", err, errBoom)
	}
}

func TestInitCmd_WritesWizardResult(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}},
	}
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, false, &written, &writtenPath, cfg)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	if !strings.Contains(string(written), "name: dev") {
		t.Errorf("written config = %q, want the wizard's result marshaled", written)
	}
	if !strings.Contains(out.String(), "wrote config to /cfg/config.yaml") {
		t.Errorf("stdout = %q, want a confirmation naming the config path", out.String())
	}
}

// TestInitCmd_SanitizedNameCollision_RejectedBeforeWriting covers #92:
// the wizard can hand back two VMs whose names sanitize to the same
// launchd label (config.Validate/ValidateVMs, which the wizard itself
// already runs, has no way to catch this -- see launchd.DetectCollisions
// and its import-cycle-driven separation from the config package). That
// must be caught before config.yaml is written, not discovered only once
// launchd.Sync runs.
func TestInitCmd_SanitizedNameCollision_RejectedBeforeWriting(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs: []config.VM{
			{Name: "My VM!", VMX: "/vms/a.vmx"},
			{Name: "My VM?", VMX: "/vms/b.vmx"},
		},
	}
	deps := fakeInitDeps(nil, false, &written, &writtenPath, cfg)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "sanitize") {
		t.Fatalf("Execute() error = %v, want it to mention the sanitized-label collision", err)
	}
	if written != nil {
		t.Error("writeFile was called, want no write when the wizard's VM list collides")
	}
}

func TestInitCmd_PassesDiscoveredCandidatesToWizard(t *testing.T) {
	var gotCandidates []tui.VMCandidate
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return []discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, nil },
		loadConfig:  func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, _ bool, candidates []tui.VMCandidate, _ *config.Config) (*config.Config, error) {
			gotCandidates = candidates
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(gotCandidates) != 1 || gotCandidates[0].Name != "dev" || gotCandidates[0].VMX != "/vms/dev.vmx" {
		t.Errorf("candidates passed to runWizard = %+v, want the one discovered VM converted to tui.VMCandidate", gotCandidates)
	}
}

func TestInitCmd_SearchDirFlag_AppendedAfterDefaults(t *testing.T) {
	var gotSearchDirs []string
	deps := initDeps{
		searchDirs: func() []string { return []string{"/default/a", "/default/b"} },
		discoverVMs: func(dirs []string) ([]discoveredVM, error) {
			gotSearchDirs = dirs
			return nil, nil
		},
		loadConfig: func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:    config.Marshal,
		writeFile:  func(string, []byte) error { return nil },
		fileExists: func(string) bool { return false },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--search-dir", "/extra/one", "--search-dir", "/extra/two"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := []string{"/default/a", "/default/b", "/extra/one", "/extra/two"}
	if len(gotSearchDirs) != len(want) {
		t.Fatalf("search dirs passed to discoverVMs = %v, want %v", gotSearchDirs, want)
	}
	for i, dir := range want {
		if gotSearchDirs[i] != dir {
			t.Errorf("search dirs passed to discoverVMs = %v, want %v", gotSearchDirs, want)
			break
		}
	}
}

func TestInitCmd_NoSearchDirFlag_UsesOnlyDefaults(t *testing.T) {
	var gotSearchDirs []string
	deps := initDeps{
		searchDirs: func() []string { return []string{"/default/a"} },
		discoverVMs: func(dirs []string) ([]discoveredVM, error) {
			gotSearchDirs = dirs
			return nil, nil
		},
		loadConfig: func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:    config.Marshal,
		writeFile:  func(string, []byte) error { return nil },
		fileExists: func(string) bool { return false },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(gotSearchDirs) != 1 || gotSearchDirs[0] != "/default/a" {
		t.Errorf("search dirs passed to discoverVMs = %v, want just the default [/default/a]", gotSearchDirs)
	}
}

func TestInitCmd_BothStdoutAndStdinAreTerminals_UsesNonAccessibleMode(t *testing.T) {
	var gotAccessible bool
	deps := initDeps{
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:   func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:      config.Marshal,
		writeFile:    func(string, []byte) error { return nil },
		fileExists:   func(string) bool { return false },
		isTerminal:   func(io.Writer) bool { return true },
		isTerminalIn: func(io.Reader) bool { return true },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, accessible bool, _ []tui.VMCandidate, _ *config.Config) (*config.Config, error) {
			gotAccessible = accessible
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotAccessible {
		t.Error("accessible = true, want false when both stdout and stdin are real terminals")
	}
}

// TestInitCmd_StdoutTerminalButStdinNot_UsesAccessibleMode reproduces
// finding 3 from the whole-branch review: `snapback init < answers.txt`
// run at an actual terminal has a real tty stdout but a redirected-file
// stdin. The old check only tested stdout, so this combination wrongly
// kept the rich interactive bubbletea path, which can't read a
// non-terminal stdin correctly.
func TestInitCmd_StdoutTerminalButStdinNot_UsesAccessibleMode(t *testing.T) {
	var gotAccessible bool
	deps := initDeps{
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:   func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:      config.Marshal,
		writeFile:    func(string, []byte) error { return nil },
		fileExists:   func(string) bool { return false },
		isTerminal:   func(io.Writer) bool { return true },
		isTerminalIn: func(io.Reader) bool { return false },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, accessible bool, _ []tui.VMCandidate, _ *config.Config) (*config.Config, error) {
			gotAccessible = accessible
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !gotAccessible {
		t.Error("accessible = false, want true when stdin is not a real terminal, even though stdout is")
	}
}

// TestInitCmd_NilIsTerminalIn_TreatedAsNotATerminal mirrors the existing
// nil-isTerminal behavior: a nil isTerminalIn (as every other existing
// initDeps literal in this file now has, by omission) must behave like
// "not a terminal", keeping every one of those tests on the accessible
// path they were already exercising.
func TestInitCmd_NilIsTerminalIn_TreatedAsNotATerminal(t *testing.T) {
	var gotAccessible bool
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return true },
		// isTerminalIn intentionally left nil.
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, accessible bool, _ []tui.VMCandidate, _ *config.Config) (*config.Config, error) {
			gotAccessible = accessible
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !gotAccessible {
		t.Error("accessible = false, want true when isTerminalIn is nil")
	}
}

func TestInitCmd_WizardError_IsPropagatedUnwrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return nil, errBoom
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, errBoom) {
		t.Fatalf("Execute() error = %v, want it to be (or wrap) %v", err, errBoom)
	}
}

func TestInitCmd_ExistingConfig_WithForce_Overwrites(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{Destination: "/dest", Compression: "zstd", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, true, &written, &writtenPath, cfg)
	// This test is about --force permitting an overwrite, not about the
	// prefill load itself (covered separately by
	// TestInitCmd_Force_LoadConfigError_Blocks) -- fakeInitDeps' loadConfig
	// always errors, which would now block --force entirely, so give it a
	// loadConfig that succeeds instead.
	deps.loadConfig = func(string) (*config.Config, error) { return cfg, nil }

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want --force to allow overwriting", err)
	}
	if written == nil {
		t.Errorf("writeFile was not called, want --force to allow the write")
	}
}

func TestInitCmd_Force_ExistingConfig_PassesPriorToWizard(t *testing.T) {
	existing := &config.Config{Destination: "/existing/dest", Compression: "gzip"}
	var gotPrior *config.Config
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { return existing, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return nil },
		fileExists:  func(string) bool { return true },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, _ bool, _ []tui.VMCandidate, prior *config.Config) (*config.Config, error) {
			gotPrior = prior
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPrior != existing {
		t.Errorf("prior passed to runWizard = %+v, want the loaded existing config %+v", gotPrior, existing)
	}
}

func TestInitCmd_Force_NoExistingConfig_PriorIsNilAndLoadConfigNotCalled(t *testing.T) {
	var gotPrior *config.Config
	priorWasNonNil := false
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig: func(string) (*config.Config, error) {
			t.Fatal("loadConfig should not be called when no config exists")
			return nil, nil
		},
		marshal:    config.Marshal,
		writeFile:  func(string, []byte) error { return nil },
		fileExists: func(string) bool { return false },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, _ bool, _ []tui.VMCandidate, prior *config.Config) (*config.Config, error) {
			gotPrior = prior
			priorWasNonNil = prior != nil
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if priorWasNonNil {
		t.Errorf("prior passed to runWizard = %+v, want nil when --force is used but no config exists yet", gotPrior)
	}
}

// TestInitCmd_Force_LoadConfigError_Blocks covers #88: a failed prefill
// load used to fall back to prior == nil and just print a note, which
// silently reset every VM's schedule to "none" for the wizard -- and the
// mandatory post-write auto-sync would then delete every real, working
// LaunchAgent with no chance for the user to notice first. Blocking is
// the safer default.
func TestInitCmd_Force_LoadConfigError_Blocks(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return true },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			t.Fatal("runWizard should not be called when the prefill load fails")
			return nil, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("Execute() error = %v, want it to wrap errBoom", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("Execute() error = %v, want it to mention --force", err)
	}
}

// TestInitCmd_ZeroVMConfig_WarnsButStillWrites reproduces finding 4 from
// the whole-branch review: the old hand-rolled prompter printed a
// warning to stderr when the user finished with zero VMs configured
// (config.ValidateVMs doesn't reject an empty list, so this was never a
// hard failure -- just a warning that a resulting `snapback run --all`
// would have nothing to do). That warning was dropped when the wizard
// rewrite happened.
func TestInitCmd_ZeroVMConfig_WarnsButStillWrites(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{Destination: "/dest", Compression: "zstd"} // no VMs
	deps := fakeInitDeps(nil, false, &written, &writtenPath, cfg)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want the zero-VM config to still be written", err)
	}
	wantWarning := "warning: no VMs configured; `snapback run --all` will have nothing to back up"
	if !strings.Contains(errOut.String(), wantWarning) {
		t.Errorf("stderr = %q, want it to contain %q", errOut.String(), wantWarning)
	}
	if written == nil {
		t.Error("writeFile was not called, want the zero-VM config to still be written (this is a warning, not a hard failure)")
	}
}

func TestInitCmd_WriteFileError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return errBoom },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "write config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"write config\" context", err, errBoom)
	}
}

func TestInitCmd_SyncsLaunchdSchedules(t *testing.T) {
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}},
	}
	var written []byte
	var writtenPath string
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile: func(path string, data []byte) error {
			writtenPath = path
			written = data
			return nil
		},
		fileExists:   func(string) bool { return false },
		isTerminal:   func(io.Writer) bool { return false },
		isTerminalIn: func(io.Reader) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return cfg, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Fatalf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	_ = written
	if !strings.Contains(out.String(), "installed: dev") {
		t.Errorf("stdout = %q, want \"installed: dev\" from the auto-sync", out.String())
	}
}

// TestInitCmd_SyncSchedules_ExpandsTildeDestinationBeforeRunningCheck
// guards against a regression where init's syncSchedules call forwarded
// the wizard's raw, unexpanded Destination (e.g. "~/backups") straight
// to backup.IsRunning instead of expanding it first like the other
// syncSchedules call sites (schedule.go, vm.go) do via config.Load. If
// that regression reappears, backup.IsRunning would look for the lock
// file under a literal "~" directory relative to the process's CWD,
// never find the real held lock below, and the update would proceed
// ("updated: dev") instead of being skipped.
func TestInitCmd_SyncSchedules_ExpandsTildeDestinationBeforeRunningCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := launchd.NewFakeInstaller()
	seedVMs := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := launchd.Sync(inst, seedVMs, "/bin/snapback", func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	expandedDest := filepath.Join(home, "backups")
	lock, err := backup.AcquireLock(expandedDest, "dev")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	cfg := &config.Config{
		Destination: "~/backups",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "weekly"}},
	}
	deps := initDeps{
		searchDirs:   func() []string { return nil },
		discoverVMs:  func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:   func(string) (*config.Config, error) { return nil, errBoom },
		marshal:      config.Marshal,
		writeFile:    func(string, []byte) error { return nil },
		fileExists:   func(string) bool { return false },
		isTerminal:   func(io.Writer) bool { return false },
		isTerminalIn: func(io.Reader) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return cfg, nil
		},
		newInstaller: func() (launchd.Installer, error) { return inst, nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "skipped: dev") {
		t.Errorf("stdout = %q, want \"skipped: dev\" -- proves \"~/backups\" was expanded to %q before backup.IsRunning found the held lock there", out.String(), expandedDest)
	}
}
