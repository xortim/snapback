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
