package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

func newTestRootForInit(t *testing.T, deps initDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "init", newInitCmdWithDeps(deps))
}

// fakeInitDeps returns an initDeps whose writeFile captures its argument
// into written, and whose fileExists/discoverVMs are controlled by the
// caller -- covers the common case where a test only cares about what
// init would have written, not real disk I/O.
func fakeInitDeps(candidates []discoveredVM, exists bool, written *[]byte, writtenPath *string) initDeps {
	return initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return candidates, nil },
		marshal:     config.Marshal,
		writeFile: func(path string, data []byte) error {
			*writtenPath = path
			*written = data
			return nil
		},
		fileExists: func(string) bool { return exists },
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

func TestInitCmd_WritesConfigFromDiscoveredVMAndDefaults(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// One blank line per prompt, in order: VM selection, destination,
	// compression, keep_last, keep_daily, keep_weekly, notifications --
	// every prompt accepts its default.
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	got := string(written)
	for _, want := range []string{"name: dev", "vmx: /vms/dev.vmwarevm/dev.vmx", "destination: /Volumes/Backups/snapback", "compression: zstd"} {
		if !strings.Contains(got, want) {
			t.Errorf("written config = %q, want it to contain %q", got, want)
		}
	}
	if !strings.Contains(out.String(), "wrote config to /cfg/config.yaml") {
		t.Errorf("stdout = %q, want a confirmation naming the config path", out.String())
	}
}

func TestInitCmd_NoDiscoveredVMs_PromptsManualEntry(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// name "devbox", vmx "/vms/devbox.vmx", blank name to stop manual
	// entry, then defaults for destination/compression/retention/notify
	// (7 more blank lines: destination, compression, keep_last,
	// keep_daily, keep_weekly, notifications).
	root.SetIn(strings.NewReader("devbox\n/vms/devbox.vmx\n\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := string(written)
	if !strings.Contains(got, "name: devbox") || !strings.Contains(got, "vmx: /vms/devbox.vmx") {
		t.Errorf("written config = %q, want the manually-entered devbox VM", got)
	}
}

func TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{
		{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
	}, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// Select the one candidate twice. If init deferred validation to the
	// end, it would next try to read the destination prompt, find no more
	// input, and fail with an "unexpected end of input" error instead --
	// this test only passes if the duplicate is caught immediately after
	// selection.
	root.SetIn(strings.NewReader("1,1\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Execute() error = %v, want an error wrapped as \"invalid VM selection\" mentioning \"duplicate\"", err)
	}
	if written != nil {
		t.Errorf("writeFile was called, want init to fail before writing")
	}
}

func TestInitCmd_ExistingConfig_WithoutForce_Errors(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, true, &written, &writtenPath)

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

func TestInitCmd_ExistingConfig_WithForce_Overwrites(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, true, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want --force to allow overwriting", err)
	}
	if written == nil {
		t.Errorf("writeFile was not called, want --force to allow the write")
	}
}

func TestInitCmd_InvalidCompressionChoice_Reprompts(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// blank name to skip manual VM entry, blank destination, an invalid
	// compression choice, then a valid one -- followed by defaults for
	// the remaining prompts (keep_last, keep_daily, keep_weekly, notify).
	root.SetIn(strings.NewReader("\n\nbogus\ngzip\n\n\n\n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil after a valid retry", err)
	}
	if !strings.Contains(out.String(), `"bogus" is not one of`) {
		t.Errorf("stdout = %q, want it to explain the invalid choice before re-prompting", out.String())
	}
	if !strings.Contains(string(written), "compression: gzip") {
		t.Errorf("written config = %q, want the retried valid choice", written)
	}
}

func TestInitCmd_InvalidRetentionCount_Reprompts(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// blank name, blank destination, blank compression, invalid keep_last,
	// then a valid one, then defaults for keep_daily/keep_weekly/notify.
	root.SetIn(strings.NewReader("\n\n\nnotanumber\n3\n\n\n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil after a valid retry", err)
	}
	if !strings.Contains(out.String(), `"notanumber" is not a whole number`) {
		t.Errorf("stdout = %q, want it to explain the invalid count before re-prompting", out.String())
	}
	if !strings.Contains(string(written), "keep_last: 3") {
		t.Errorf("written config = %q, want the retried valid count", written)
	}
}

func TestInitCmd_InvalidYesNoAnswer_Reprompts(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// blank name, blank destination, blank compression, blank retention x3,
	// invalid notify answer, then a valid one.
	root.SetIn(strings.NewReader("\n\n\n\n\n\nmaybe\nn\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil after a valid retry", err)
	}
	if !strings.Contains(out.String(), `"maybe" is not y/n`) {
		t.Errorf("stdout = %q, want it to explain the invalid answer before re-prompting", out.String())
	}
	if !strings.Contains(string(written), "enabled: false") {
		t.Errorf("written config = %q, want the retried valid answer", written)
	}
}

func TestInitCmd_InvalidChoiceThenEOF_Errors(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// blank name, blank destination, invalid compression, then input runs
	// out -- the re-prompt itself must still hard-fail on a genuinely
	// unrecoverable read, not loop forever.
	root.SetIn(strings.NewReader("\n\nbogus\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "read input") {
		t.Fatalf("Execute() error = %v, want a read-input error once retry input is exhausted", err)
	}
}

func TestInitCmd_DiscoverVMsError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return false },
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

func TestInitCmd_WriteFileError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return errBoom },
		fileExists:  func(string) bool { return false },
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "write config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"write config\" context", err, errBoom)
	}
}
