package progress_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/progress"
)

func TestTerminalReporter_PrintsMessages(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewTerminalReporter(&buf)

	r.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking VMware Tools state"})
	r.Report(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot snapback-20260101T000000Z"})

	got := buf.String()
	if !strings.Contains(got, "checking tools: checking VMware Tools state\n") {
		t.Errorf("output missing CheckingTools line, got %q", got)
	}
	if !strings.Contains(got, "snapshotting: taking snapshot snapback-20260101T000000Z\n") {
		t.Errorf("output missing Snapshotting line, got %q", got)
	}
}

func TestTerminalReporter_DropsPercentOnlyEvents(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewTerminalReporter(&buf)

	r.Report(progress.Event{Stage: progress.Copying, Percent: 0.5})

	if buf.Len() != 0 {
		t.Errorf("output = %q, want empty for a percent-only event", buf.String())
	}
}

func TestTerminalReporter_PrintsErrOnlyEvents(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewTerminalReporter(&buf)
	errBoom := errors.New("boom")

	r.Report(progress.Event{Stage: progress.Merging, Err: errBoom})

	got := buf.String()
	if !strings.Contains(got, "merging: boom\n") {
		t.Errorf("output = %q, want it to print an error-only event", got)
	}
}

func TestTerminalReporter_IncludesErr(t *testing.T) {
	var buf bytes.Buffer
	r := progress.NewTerminalReporter(&buf)
	errBoom := errors.New("boom")

	r.Report(progress.Event{Stage: progress.Merging, Message: "merge failed", Err: errBoom})

	got := buf.String()
	if !strings.Contains(got, "merging: merge failed: boom\n") {
		t.Errorf("output = %q, want it to include the error", got)
	}
}
