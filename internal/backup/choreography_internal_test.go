// Package backup (internal test package, not backup_test like
// choreography_test.go) so this file can override the unexported dirSize
// var to force a deterministic Run() failure at the size-measurement step
// -- see size.go's doc comment on dirSize for why that seam exists.
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/vm"
)

var errDirSizeBoom = errors.New("boom")

func TestRun_DirSizeError_RunErrorStageIsCopying(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(bundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}

	restore := dirSize
	dirSize = func(string) (int64, error) { return 0, errDirSizeBoom }
	defer func() { dirSize = restore }()

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	_, err := Run(t.Context(), fake, progress.NoOpReporter{}, Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error from dirSize failing")
	}

	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("errors.As(err, &runErr) = false, want true; err = %v", err)
	}
	if runErr.Stage != progress.Copying {
		t.Errorf("runErr.Stage = %v, want %v (per ADR-003: dirSize failure is tagged Copying)", runErr.Stage, progress.Copying)
	}
}
