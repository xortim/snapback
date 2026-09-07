package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"github.com/xortim/snapback/internal/config"
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

func TestPromptCoreSettings_AcceptsAllDefaults(t *testing.T) {
	// One blank line per field, in order: destination, compression,
	// keep_last, keep_daily, keep_weekly, notifications.
	in := strings.NewReader("\n\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	want := coreSettings{
		destination: defaultDestination,
		compression: defaultCompression,
		keepLast:    defaultKeepLast,
		keepDaily:   defaultKeepDaily,
		keepWeekly:  defaultKeepWeekly,
		notify:      true,
	}
	if got != want {
		t.Errorf("promptCoreSettings() = %+v, want %+v", got, want)
	}
}

func TestPromptCoreSettings_InvalidCompressionChoice_Reprompts(t *testing.T) {
	// destination blank, compression: a non-numeric answer (Select's
	// accessible mode only accepts a number) then "2" (gzip is the
	// second option), then defaults for the rest.
	in := strings.NewReader("\nbogus\n2\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.compression != "gzip" {
		t.Errorf("compression = %q, want %q", got.compression, "gzip")
	}
	if !strings.Contains(out.String(), "Invalid: must be a number between") {
		t.Errorf("output = %q, want a reprompt explaining the invalid choice", out.String())
	}
}

func TestPromptCoreSettings_InvalidRetentionCount_Reprompts(t *testing.T) {
	// destination blank, compression blank, keep_last: invalid then "3",
	// then defaults for keep_daily/keep_weekly/notifications.
	in := strings.NewReader("\n\nnotanumber\n3\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.keepLast != 3 {
		t.Errorf("keepLast = %d, want 3", got.keepLast)
	}
	if !strings.Contains(out.String(), `"notanumber" is not a whole number`) {
		t.Errorf("output = %q, want a reprompt explaining the invalid count", out.String())
	}
}

func TestPromptCoreSettings_InvalidNotifyAnswer_Reprompts(t *testing.T) {
	// destination/compression/retention x3 blank, notify: invalid then
	// "n".
	in := strings.NewReader("\n\n\n\n\nmaybe\nn\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.notify {
		t.Error("notify = true, want false (the retried answer)")
	}
	if !strings.Contains(out.String(), "invalid input") {
		t.Errorf("output = %q, want a reprompt explaining the invalid answer", out.String())
	}
}

func TestPromptCoreSettings_UnwritableDestination_Reprompts(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block writes")
	}

	writable := t.TempDir()
	in := strings.NewReader(filepath.Join(dir, "backups") + "\n" + filepath.Join(writable, "backups") + "\n\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.destination != filepath.Join(writable, "backups") {
		t.Errorf("destination = %q, want the retried writable path", got.destination)
	}
	if !strings.Contains(out.String(), "not writable") {
		t.Errorf("output = %q, want a reprompt explaining the unwritable destination", out.String())
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

func TestPromptSchedules_DefaultIsNone(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// schedule select blank (default "none"), custom-cron blank (unused).
	in := strings.NewReader("\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty for the default \"none\" choice", vms[0].Schedule)
	}
}

func TestPromptSchedules_Nightly(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// "2" selects "nightly" (scheduleChoices[1]); custom-cron blank (unused).
	in := strings.NewReader("2\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != cronNightly {
		t.Errorf("Schedule = %q, want %q", vms[0].Schedule, cronNightly)
	}
}

func TestPromptSchedules_CustomCron_InvalidThenValid(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// "4" selects "custom" (scheduleChoices[3]); custom-cron: invalid
	// (wrong field count) then a valid 5-field expression.
	in := strings.NewReader("4\nbadcron\n0 3 * * 1\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "0 3 * * 1" {
		t.Errorf("Schedule = %q, want %q", vms[0].Schedule, "0 3 * * 1")
	}
	if !strings.Contains(out.String(), "5 space-separated fields") {
		t.Errorf("output = %q, want a reprompt explaining the invalid cron expression", out.String())
	}
}

func TestPromptSchedules_MultipleVMs_AskedInOrder(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	// dev: blank (none), blank (unused). prod: "3" (weekly), blank (unused).
	in := strings.NewReader("\n\n3\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("dev Schedule = %q, want empty", vms[0].Schedule)
	}
	if vms[1].Schedule != cronWeekly {
		t.Errorf("prod Schedule = %q, want %q", vms[1].Schedule, cronWeekly)
	}
	if !strings.Contains(out.String(), "Schedule for dev") || !strings.Contains(out.String(), "Schedule for prod") {
		t.Errorf("output = %q, want both VM names named in their own prompt", out.String())
	}
}
