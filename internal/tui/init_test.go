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
	"time"

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

func TestSelectVMs_DuplicateDiscoveredSelection_FailsBeforeManualEntry(t *testing.T) {
	candidates := []VMCandidate{
		{Name: "dev", VMX: "/vms/a/dev.vmx"},
		{Name: "dev", VMX: "/vms/b/dev.vmx"},
	}
	// Both candidates are pre-selected by default, so "0" alone confirms
	// the (duplicate-named) default selection with no toggling needed.
	// Deliberately no manual-entry answers follow: that flow must never
	// run once the discovered selection alone is already invalid, so a
	// regression back to validating only after manual entry would fail on
	// an EOF/errUnexpectedEOF instead of this error.
	in := strings.NewReader("0\n")
	var out bytes.Buffer

	_, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("selectVMs() error = %v, want it to mention \"invalid VM selection\" and \"duplicate\"", err)
	}
}

func TestSelectVMs_DuplicateName_TogglingOffOneKeepsOnlyTheOther(t *testing.T) {
	candidates := []VMCandidate{
		{Name: "dev", VMX: "/vms/a/dev.vmx", Dir: "a"},
		{Name: "dev", VMX: "/vms/b/dev.vmx", Dir: "b"},
	}
	// Both pre-selected by default; "1" toggles the first (index 1) off,
	// "0" confirms the remaining selection (just the second), "n" declines
	// manual entry. Reproduces #48's actual motivation: before switching
	// the MultiSelect option value from Name to VMX, both entries shared
	// the same option value ("dev"), so there was no way to select just
	// one of two same-named candidates -- toggling either one affected
	// the same underlying value.
	in := strings.NewReader("1\n0\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].VMX != "/vms/b/dev.vmx" {
		t.Errorf("selectVMs() = %+v, want only the second (untoggled) candidate", vms)
	}
}

func TestSelectVMs_DuplicateNames_LabelsDisambiguateOnlyTheColliding(t *testing.T) {
	candidates := []VMCandidate{
		{Name: "dev", VMX: "/vms/a/dev.vmx", Dir: "Virtual Machines"},
		{Name: "dev", VMX: "/vms/b/dev.vmx", Dir: "Virtual Machines.localized"},
		{Name: "unique-vm", VMX: "/vms/c/unique-vm.vmx", Dir: "Virtual Machines"},
	}
	// Toggle the first "dev" candidate off (both start pre-selected, and
	// selecting both would trip config.ValidateVMs's duplicate-name
	// rejection -- irrelevant to what this test checks: the rendered
	// labels, not the selection outcome).
	in := strings.NewReader("1\n0\nn\n")
	var out bytes.Buffer

	_, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "dev (also in Virtual Machines)") || !strings.Contains(rendered, "dev (also in Virtual Machines.localized)") {
		t.Errorf("output = %q, want both colliding \"dev\" entries labeled with their containing dir", rendered)
	}
	if strings.Contains(rendered, "unique-vm (also in") {
		t.Errorf("output = %q, want the non-colliding \"unique-vm\" entry to keep its plain label", rendered)
	}
}

func TestCandidateLabel(t *testing.T) {
	c := VMCandidate{Name: "dev", Dir: "Virtual Machines.localized"}
	if got := candidateLabel(c, false); got != "dev" {
		t.Errorf("candidateLabel(c, false) = %q, want the plain name %q", got, "dev")
	}
	if got := candidateLabel(c, true); got != "⚠ dev (also in Virtual Machines.localized)" {
		t.Errorf("candidateLabel(c, true) = %q, want the disambiguated label", got)
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

// TestPromptCoreSettings_InvalidRetentionThenEOF_ReturnsError reproduces
// finding 2 from the whole-branch review: an invalid retention count
// with no corrected answer following it (input runs out instead) must
// not silently reach strconv.Atoi as an unvalidated string -- it must
// surface as an error.
func TestPromptCoreSettings_InvalidRetentionThenEOF_ReturnsError(t *testing.T) {
	in := strings.NewReader("\n\nnotanumber\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err == nil {
		t.Fatalf("promptCoreSettings() = %+v, err = nil, want an error for an invalid value followed by EOF", got)
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

// blockingReader never returns from Read, standing in for an
// accessible-mode input stream stalled on a real, otherwise-idle
// terminal (e.g. `snapback init | tee log.txt`, still interactive on
// stdin but accessible since accessible triggers on either stream not
// being a real tty -- see internal/cli/init.go's runInit).
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {}
}

func TestRunForm_Accessible_CtxAlreadyCanceled_ReturnsPromptlyWithoutReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	var value string

	done := make(chan error, 1)
	go func() {
		done <- runForm(ctx, blockingReader{}, &out, true, huh.NewGroup(huh.NewInput().Title("x").Value(&value)))
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "init cancelled") {
			t.Fatalf("runForm() error = %v, want it to mention \"init cancelled\"", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runForm() did not return promptly on an already-canceled ctx -- it's still blocked on the accessible-mode read")
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

func TestReviewAndConfirm_ShowsRenderedYAMLAndDefaultsToWrite(t *testing.T) {
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}},
	}
	in := strings.NewReader("\n") // accept the default "write this config? [Y/n]"
	var out bytes.Buffer

	ok, err := reviewAndConfirm(context.Background(), in, &out, true, cfg)
	if err != nil {
		t.Fatalf("reviewAndConfirm() error = %v", err)
	}
	if !ok {
		t.Error("reviewAndConfirm() = false, want true (the default)")
	}
	if !strings.Contains(out.String(), "name: dev") || !strings.Contains(out.String(), "destination: /dest") {
		t.Errorf("output = %q, want the rendered config YAML shown for review", out.String())
	}
}

func TestReviewAndConfirm_Declined_ReturnsFalse(t *testing.T) {
	cfg := &config.Config{Destination: "/dest", Compression: "zstd", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}
	in := strings.NewReader("n\n")
	var out bytes.Buffer

	ok, err := reviewAndConfirm(context.Background(), in, &out, true, cfg)
	if err != nil {
		t.Fatalf("reviewAndConfirm() error = %v", err)
	}
	if ok {
		t.Error("reviewAndConfirm() = true, want false")
	}
}

func TestRunInitWizard_EndToEnd_DiscoveredVMWithDefaults(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// VM select: "0" (confirm default selection), "n" (decline manual).
	// Core settings: 6 blanks (destination/compression/keep_last/
	// keep_daily/keep_weekly/notify), all defaults.
	// Schedule (1 VM): 2 blanks (choice=none, custom=unused).
	// Review: blank (accept default "write? [Y/n]" = yes).
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n\n")
	var out bytes.Buffer

	cfg, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("RunInitWizard() error = %v", err)
	}
	if len(cfg.VMs) != 1 || cfg.VMs[0].Name != "dev" {
		t.Fatalf("cfg.VMs = %+v, want the one discovered VM", cfg.VMs)
	}
	if cfg.Destination != defaultDestination || cfg.Compression != defaultCompression {
		t.Errorf("cfg = %+v, want default destination/compression", cfg)
	}
	if cfg.Retention.KeepLast != defaultKeepLast {
		t.Errorf("cfg.Retention.KeepLast = %d, want %d", cfg.Retention.KeepLast, defaultKeepLast)
	}
	if !cfg.Notifications.Enabled {
		t.Error("cfg.Notifications.Enabled = false, want true (the default)")
	}
}

func TestRunInitWizard_DuplicateVMName_FailsBeforeCoreSettings(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// confirm default selection, then manually add another VM also named
	// "dev" -- config.ValidateVMs must catch this before promptCoreSettings
	// ever runs (there's no more scripted input for it to consume, so if
	// validation were deferred this test would hang/fail on a different
	// error).
	in := strings.NewReader("0\ny\ndev\n/vms/other.vmx\nn\n")
	var out bytes.Buffer

	_, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("RunInitWizard() error = %v, want it to mention \"invalid VM selection\" and \"duplicate\"", err)
	}
}

// TestRunInitWizard_EmptyReader_ReturnsError reproduces finding 1 from
// the whole-branch review: the reviewer verified empirically that
// RunInitWizard fed a completely empty io.Reader returned a fully
// populated *config.Config with err == nil, including the final "write
// this config?" Confirm silently defaulting to true, even though the
// user never saw or confirmed a single prompt. A truncated (here,
// entirely absent) interactive session must be a hard failure instead.
func TestRunInitWizard_EmptyReader_ReturnsError(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	in := strings.NewReader("")
	var out bytes.Buffer

	cfg, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err == nil {
		t.Fatalf("RunInitWizard() = %+v, err = nil, want an error for a completely empty reader", cfg)
	}
	if cfg != nil {
		t.Errorf("RunInitWizard() cfg = %+v, want nil alongside the error", cfg)
	}
}

func TestSelectVMs_EmptyReader_ReturnsError(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	in := strings.NewReader("")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err == nil {
		t.Fatalf("selectVMs() = %+v, err = nil, want an error for a completely empty reader", vms)
	}
}

func TestRunInitWizard_DeclinedAtReview_ReturnsAbortedError(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\nn\n")
	var out bytes.Buffer

	_, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("RunInitWizard() error = %v, want an \"aborted\" error", err)
	}
}
