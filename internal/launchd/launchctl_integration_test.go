//go:build integration

package launchd_test

// Real launchctl bootstrap/bootout execution, per README.md/CLAUDE.md:
//
//	SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
//
// Uses a scratch directory (t.TempDir()) as Dir, not the real
// ~/Library/LaunchAgents, and a label distinct from any real snapback
// schedule -- this never touches a real backup schedule.

import (
	"os"
	"testing"

	"github.com/xortim/snapback/internal/launchd"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("SNAPBACK_INTEGRATION") != "1" {
		t.Skip("set SNAPBACK_INTEGRATION=1 to run launchd integration tests")
	}
}

func TestIntegration_BootstrapThenBootout(t *testing.T) {
	requireIntegration(t)
	inst := &launchd.LaunchctlInstaller{Dir: t.TempDir()}
	agent := launchd.Agent{
		Label:      "com.tim.snapback.integration-test",
		VMName:     "integration-test",
		BinaryPath: "/bin/echo",
		LogPath:    t.TempDir() + "/integration-test.log",
	}

	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Bootout(agent.Label); err != nil {
			t.Errorf("cleanup Bootout() error = %v", err)
		}
	})
}

// TestIntegration_BootoutNeverBootstrapped is the one place the
// "not loaded" tolerance in LaunchctlInstaller.Bootout (isNotLoadedError)
// can actually be confirmed: the exact message and exit code launchctl
// emits for an unloaded label aren't reproducible without real launchd,
// so the implementation tolerates several known forms defensively. If
// this test ever fails, launchctl is reporting something none of those
// forms match -- add it there rather than relaxing this assertion.
func TestIntegration_BootoutNeverBootstrapped(t *testing.T) {
	requireIntegration(t)
	inst := &launchd.LaunchctlInstaller{Dir: t.TempDir()}
	if err := inst.Bootout("com.tim.snapback.integration-never-bootstrapped"); err != nil {
		t.Errorf("Bootout() on a label that was never bootstrapped = %v, want nil (idempotent removal)", err)
	}
}
