package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestAddVMs_SelectsDiscoveredAndSetsSchedule(t *testing.T) {
	candidates := []VMCandidate{{Name: "new-vm", VMX: "/vms/new-vm.vmwarevm/new-vm.vmx"}}
	// "0" confirms the MultiSelect's pre-selected default; "n" declines
	// manual entry; "2" picks the "nightly" schedule preset; custom-cron
	// blank (unused since nightly, not custom, was chosen).
	in := strings.NewReader("0\nn\n2\n\n")
	var out bytes.Buffer

	vms, err := AddVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("AddVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "new-vm" {
		t.Fatalf("AddVMs() = %+v, want the one discovered candidate", vms)
	}
	if vms[0].Schedule == "" {
		t.Errorf("Schedule = %q, want the nightly preset's cron expression set", vms[0].Schedule)
	}
}

func TestAddVMs_NoCandidates_PromptsManualEntry(t *testing.T) {
	// manual-add confirm defaults to true (nothing discovered), name +
	// vmx, decline a second manual VM, then "none" schedule.
	in := strings.NewReader("\ndevbox\n/vms/devbox.vmx\nn\n\n\n")
	var out bytes.Buffer

	vms, err := AddVMs(context.Background(), in, &out, true, nil)
	if err != nil {
		t.Fatalf("AddVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "devbox" || vms[0].VMX != "/vms/devbox.vmx" {
		t.Errorf("AddVMs() = %+v, want the manually-entered devbox VM", vms)
	}
}

func TestAddVMs_DuplicateSelection_FailsBeforeSchedulePrompt(t *testing.T) {
	candidates := []VMCandidate{
		{Name: "dup", VMX: "/vms/a/dup.vmx"},
		{Name: "dup", VMX: "/vms/b/dup.vmx"},
	}
	// Both pre-selected by default; "0" alone confirms the (duplicate-
	// named) default selection. No schedule-prompt answers follow --
	// validation must fail before ever reaching promptSchedules, or this
	// would instead fail on EOF.
	in := strings.NewReader("0\n")
	var out bytes.Buffer

	_, err := AddVMs(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("AddVMs() error = %v, want it to mention \"invalid VM selection\" and \"duplicate\"", err)
	}
}

func TestAddVMs_SelectVMsError_IsPropagated(t *testing.T) {
	// Empty reader: selectVMs's own manual-add confirm hits EOF
	// immediately.
	in := strings.NewReader("")
	var out bytes.Buffer

	_, err := AddVMs(context.Background(), in, &out, true, nil)
	if err == nil {
		t.Fatal("AddVMs() error = nil, want an error for a completely empty reader")
	}
}

// sanity check that AddVMs returns config.VM values usable exactly like
// any other config.VM (e.g. straight into config.ValidateVMs) -- no
// tui-specific wrapper type leaks out.
func TestAddVMs_ReturnsPlainConfigVMs(t *testing.T) {
	candidates := []VMCandidate{{Name: "new-vm", VMX: "/vms/new-vm.vmx"}}
	in := strings.NewReader("0\nn\n\n\n")
	var out bytes.Buffer

	vms, err := AddVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("AddVMs() error = %v", err)
	}
	if err := config.ValidateVMs(vms); err != nil {
		t.Errorf("config.ValidateVMs(AddVMs() result) error = %v, want nil", err)
	}
}
