package launchd

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"dev-ubuntu", "dev-ubuntu"},
		{"My VM!", "my-vm"},
		{"win_testbed 2", "win-testbed-2"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"!!!", ""},
		{"A", "a"},
		{"a--b", "a-b"},
	}
	for _, tt := range tests {
		if got := sanitizeLabel(tt.name); got != tt.want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDetectCollisions_NoCollision(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	if err := DetectCollisions(vms); err != nil {
		t.Errorf("DetectCollisions() = %v, want nil", err)
	}
}

func TestDetectCollisions_TwoNamesSanitizeToSameLabel(t *testing.T) {
	vms := []config.VM{
		{Name: "My VM!", VMX: "/vms/a.vmx"},
		{Name: "My VM?", VMX: "/vms/b.vmx"},
	}
	err := DetectCollisions(vms)
	if err == nil || !strings.Contains(err.Error(), "My VM!") || !strings.Contains(err.Error(), "My VM?") {
		t.Errorf("DetectCollisions() = %v, want an error naming both colliding VMs", err)
	}
}

func TestDetectCollisions_AllPunctuationName_RejectsEmptyLabel(t *testing.T) {
	vms := []config.VM{{Name: "!!!", VMX: "/vms/a.vmx"}}
	err := DetectCollisions(vms)
	if err == nil || !strings.Contains(err.Error(), "!!!") {
		t.Errorf("DetectCollisions() = %v, want an error naming the all-punctuation VM", err)
	}
}
