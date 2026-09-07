package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
)

func TestSelectVMs_AcceptsDefaultSelection_AllDiscovered(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// "0" confirms the MultiSelect's pre-selected-by-default candidate
	// without toggling anything; "n" declines manual entry.
	in := strings.NewReader("0\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "dev" || vms[0].VMX != "/vms/dev.vmwarevm/dev.vmx" {
		t.Errorf("selectVMs() = %+v, want the one discovered candidate", vms)
	}
}

func TestSelectVMs_CanAlsoAddManually(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// confirm default selection, opt into manual entry, name+vmx for a VM
	// discovery can't see (renamed after creation -- see
	// internal/cli/vmdiscovery.go's own doc comment on this), decline a
	// second manual VM.
	in := strings.NewReader("0\ny\nrenamed\n/vms/renamed.vmwarevm/renamed.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("selectVMs() = %+v, want 2 VMs", vms)
	}
	if vms[0].Name != "dev" || vms[1].Name != "renamed" || vms[1].VMX != "/vms/renamed.vmwarevm/renamed.vmx" {
		t.Errorf("selectVMs() = %+v, want dev then the manually-added renamed VM", vms)
	}
}

func TestSelectVMs_NoDiscoveredCandidates_PromptsManualEntryImmediately(t *testing.T) {
	// No MultiSelect group exists when there are no candidates, so the
	// first prompt is the manual-add confirm -- defaulted to true for
	// the first iteration when there was nothing to discover (see
	// addManualVMs's firstDefaultYes), so a blank answer walks straight
	// into naming the first VM instead of requiring an explicit "y".
	in := strings.NewReader("\ndevbox\n/vms/devbox.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, nil)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "devbox" || vms[0].VMX != "/vms/devbox.vmx" {
		t.Errorf("selectVMs() = %+v, want the manually-entered devbox VM", vms)
	}
	if !strings.Contains(out.String(), "no VMs found automatically") {
		t.Errorf("output = %q, want a notice that discovery found nothing", out.String())
	}
}

func TestSelectVMs_ManualEntry_RejectsBlankName(t *testing.T) {
	// blank name is rejected by huh.ValidateNotEmpty(), so it reprompts;
	// give it a real name next.
	in := strings.NewReader("\n\ndevbox\n/vms/devbox.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, nil)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "devbox" {
		t.Errorf("selectVMs() = %+v, want one retried devbox entry", vms)
	}
}

func TestIsCancellation_CanceledContext_ReturnsTrue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !isCancellation(ctx, errors.New("boom")) {
		t.Error("isCancellation() = false, want true for a canceled ctx regardless of err")
	}
}

func TestIsCancellation_UserAbortedError_ReturnsTrue(t *testing.T) {
	if !isCancellation(context.Background(), huh.ErrUserAborted) {
		t.Error("isCancellation() = false, want true for huh.ErrUserAborted")
	}
}

func TestIsCancellation_WrappedUserAbortedError_ReturnsTrue(t *testing.T) {
	wrapped := fmt.Errorf("form run: %w", huh.ErrUserAborted)
	if !isCancellation(context.Background(), wrapped) {
		t.Error("isCancellation() = false, want true for a wrapped huh.ErrUserAborted")
	}
}

func TestIsCancellation_OtherError_LiveContext_ReturnsFalse(t *testing.T) {
	if isCancellation(context.Background(), errors.New("boom")) {
		t.Error("isCancellation() = true, want false for an unrelated error on a live context")
	}
}
