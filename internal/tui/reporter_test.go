package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

type fakeSender struct {
	sent []tea.Msg
}

func (f *fakeSender) Send(msg tea.Msg) {
	f.sent = append(f.sent, msg)
}

func TestReporter_Report_ForwardsAsEventMsg(t *testing.T) {
	fake := &fakeSender{}
	r := NewReporter(fake)

	var _ progress.Reporter = r // compile-time interface check

	r.Report(progress.Event{Stage: progress.Copying, Percent: 0.75})

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	got, ok := fake.sent[0].(eventMsg)
	if !ok {
		t.Fatalf("sent message type = %T, want eventMsg", fake.sent[0])
	}
	if got.Stage != progress.Copying || got.Percent != 0.75 {
		t.Errorf("sent eventMsg = %+v, want Stage=Copying Percent=0.75", got)
	}
}
