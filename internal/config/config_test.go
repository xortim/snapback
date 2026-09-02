package config_test

import (
	"os"
	"path/filepath"
	"strings"
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
    vmx: /Users/testuser/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
    schedule: "0 2 * * *"
    comment_template: "nightly auto-backup"
  - name: win-testbed
    vmx: /Users/testuser/Virtual Machines/win-testbed.vmwarevm/win-testbed.vmx
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
	if cfg.VMs[0].Name != "dev-ubuntu" || cfg.VMs[0].VMX != "/Users/testuser/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx" || cfg.VMs[0].Schedule != "0 2 * * *" || cfg.VMs[0].CommentTemplate != "nightly auto-backup" {
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

func TestLoad_MalformedYAML_ErrorNamesPath(t *testing.T) {
	path := writeTempConfig(t, "destination: [unterminated")

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load returned nil error for malformed YAML, want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("Load error = %q, want it to name the config path %q", err.Error(), path)
	}
}

func TestLoad_ExpandsTildeInDestinationAndVMX(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := writeTempConfig(t, `
destination: ~/backups
compression: zstd
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
vms:
  - name: dev
    vmx: ~/Virtual Machines/dev.vmwarevm/dev.vmx
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	wantDest := filepath.Join(home, "backups")
	if cfg.Destination != wantDest {
		t.Errorf("Destination = %q, want %q", cfg.Destination, wantDest)
	}
	wantVMX := filepath.Join(home, "Virtual Machines/dev.vmwarevm/dev.vmx")
	if cfg.VMs[0].VMX != wantVMX {
		t.Errorf("VMs[0].VMX = %q, want %q", cfg.VMs[0].VMX, wantVMX)
	}
}

func TestLoad_DefaultsMissingCompressionToZstd(t *testing.T) {
	path := writeTempConfig(t, `
destination: /Volumes/Backups/snapback
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v, want a missing compression field to default rather than fail validation", err)
	}
	if cfg.Compression != "zstd" {
		t.Errorf("Compression = %q, want the default %q", cfg.Compression, "zstd")
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
