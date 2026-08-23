package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestLoad_ParsesFullConfig(t *testing.T) {
	yaml := `
destination: /Volumes/Backups/snapback
compression: zstd
retention:
  keep_last: 5
  keep_daily: 7
  keep_weekly: 4
vms:
  - name: dev-ubuntu
    vmx: ~/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
    schedule: "0 2 * * *"
    comment_template: "nightly auto-backup"
  - name: win-testbed
    vmx: ~/Virtual Machines/win-testbed.vmwarevm/win-testbed.vmx
    schedule: "0 2 * * 0"
notifications:
  enabled: true
`
	path := writeTempConfig(t, yaml)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Destination != "/Volumes/Backups/snapback" {
		t.Errorf("Destination = %q, want %q", cfg.Destination, "/Volumes/Backups/snapback")
	}
	if cfg.Compression != "zstd" {
		t.Errorf("Compression = %q, want %q", cfg.Compression, "zstd")
	}
	if cfg.Retention.KeepLast != 5 || cfg.Retention.KeepDaily != 7 || cfg.Retention.KeepWeekly != 4 {
		t.Errorf("Retention = %+v, want {5 7 4}", cfg.Retention)
	}
	if len(cfg.VMs) != 2 {
		t.Fatalf("len(VMs) = %d, want 2", len(cfg.VMs))
	}
	if cfg.VMs[0].Name != "dev-ubuntu" || cfg.VMs[0].VMX != "~/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx" || cfg.VMs[0].Schedule != "0 2 * * *" || cfg.VMs[0].CommentTemplate != "nightly auto-backup" {
		t.Errorf("VMs[0] = %+v, unexpected", cfg.VMs[0])
	}
	if cfg.VMs[1].Name != "win-testbed" || cfg.VMs[1].CommentTemplate != "" {
		t.Errorf("VMs[1] = %+v, unexpected", cfg.VMs[1])
	}
	if !cfg.Notifications.Enabled {
		t.Errorf("Notifications.Enabled = false, want true")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load returned nil error for a missing file, want an error")
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return path
}
