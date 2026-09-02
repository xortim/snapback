package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestMarshal_ProducesExpectedYAML(t *testing.T) {
	cfg := &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "zstd",
		Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
		VMs: []config.VM{
			{Name: "dev-ubuntu", VMX: "/vms/dev-ubuntu.vmwarevm/dev-ubuntu.vmx", Schedule: "0 2 * * *", CommentTemplate: "nightly auto-backup"},
			{Name: "win-testbed", VMX: "/vms/win-testbed.vmwarevm/win-testbed.vmx"},
		},
		Notifications: config.Notifications{Enabled: true},
	}

	got, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	want := `destination: /Volumes/Backups/snapback
compression: zstd
retention:
    keep_last: 5
    keep_daily: 7
    keep_weekly: 4
vms:
    - name: dev-ubuntu
      vmx: /vms/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
      schedule: 0 2 * * *
      comment_template: nightly auto-backup
    - name: win-testbed
      vmx: /vms/win-testbed.vmwarevm/win-testbed.vmx
notifications:
    enabled: true
`
	if string(got) != want {
		t.Errorf("Marshal produced:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarshal_RoundTripsThroughLoad(t *testing.T) {
	cfg := &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "gzip",
		Retention:   config.Retention{KeepLast: 3, KeepDaily: 2, KeepWeekly: 1},
		VMs: []config.VM{
			{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
		},
		Notifications: config.Notifications{Enabled: false},
	}

	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("failed to write marshaled config: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(marshaled config) returned error: %v", err)
	}
	if loaded.Destination != cfg.Destination || loaded.Compression != cfg.Compression || loaded.Retention != cfg.Retention {
		t.Errorf("Load(Marshal(cfg)) = %+v, want it to match the original scalar/retention fields", loaded)
	}
	if len(loaded.VMs) != 1 || loaded.VMs[0] != cfg.VMs[0] {
		t.Errorf("Load(Marshal(cfg)).VMs = %+v, want %+v", loaded.VMs, cfg.VMs)
	}
}
