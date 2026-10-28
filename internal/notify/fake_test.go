package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

var _ notify.Notifier = (*notify.Fake)(nil)
var _ notify.Notifier = (*notify.OSAScript)(nil)

func TestFake_RecordsAndReturnsErr(t *testing.T) {
	boom := errors.New("boom")
	f := &notify.Fake{Err: boom}
	n := notify.Notification{Title: "t", Message: "m"}
	if err := f.Notify(context.Background(), n); !errors.Is(err, boom) {
		t.Fatalf("Notify() error = %v, want boom", err)
	}
	if len(f.Sent) != 1 || f.Sent[0] != n {
		t.Errorf("Sent = %+v, want [%+v] (recorded even when returning Err)", f.Sent, n)
	}
	if len(f.Contexts) != 1 {
		t.Errorf("Contexts len = %d, want 1", len(f.Contexts))
	}
}
