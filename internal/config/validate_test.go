package config_test

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func validConfig() *config.Config {
	return &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "zstd",
		Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
		VMs: []config.VM{
			{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
		},
	}
}

func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := config.Validate(validConfig()); err != nil {
		t.Errorf("Validate(valid config) = %v, want nil", err)
	}
}

func TestValidate_RejectsEmptyDestination(t *testing.T) {
	cfg := validConfig()
	cfg.Destination = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Errorf("Validate() = %v, want an error mentioning \"destination\"", err)
	}
}

func TestValidate_RejectsUnknownCompression(t *testing.T) {
	cfg := validConfig()
	cfg.Compression = "bogus"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "compression") || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Validate() = %v, want an error naming the bad compression value", err)
	}
}

func TestValidate_RejectsNegativeRetention(t *testing.T) {
	cfg := validConfig()
	cfg.Retention.KeepLast = -1
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "keep_last") {
		t.Errorf("Validate() = %v, want an error naming keep_last", err)
	}
}

func TestValidate_RejectsVMMissingName(t *testing.T) {
	cfg := validConfig()
	cfg.VMs[0].Name = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("Validate() = %v, want an error about the missing VM name", err)
	}
}

func TestValidate_RejectsVMMissingVMX(t *testing.T) {
	cfg := validConfig()
	cfg.VMs[0].VMX = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "vmx") {
		t.Errorf("Validate() = %v, want an error about the missing vmx path", err)
	}
}

func TestValidate_RejectsDuplicateVMNames(t *testing.T) {
	cfg := validConfig()
	cfg.VMs = append(cfg.VMs, config.VM{Name: "dev", VMX: "/vms/dev2.vmx"})
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Validate() = %v, want an error about the duplicate VM name", err)
	}
}

func TestValidate_ReportsMultipleProblemsAtOnce(t *testing.T) {
	cfg := validConfig()
	cfg.Destination = ""
	cfg.Compression = "bogus"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination") || !strings.Contains(err.Error(), "compression") {
		t.Errorf("Validate() = %v, want it to report both problems, not just the first", err)
	}
}

func TestValidate_RejectsDuplicateVMNames_IgnoringSurroundingWhitespace(t *testing.T) {
	cfg := validConfig()
	cfg.VMs = append(cfg.VMs, config.VM{Name: " dev", VMX: "/vms/dev2.vmx"})
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Validate() = %v, want an error about the duplicate VM name (whitespace-padded)", err)
	}
}

func TestValidateVMs_RejectsDuplicateNames(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "dev", VMX: "/vms/dev2.vmx"},
	}
	err := config.ValidateVMs(vms)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("ValidateVMs() = %v, want an error about the duplicate VM name", err)
	}
}

func TestValidateVMs_AcceptsDistinctNames(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	if err := config.ValidateVMs(vms); err != nil {
		t.Errorf("ValidateVMs() = %v, want nil", err)
	}
}
