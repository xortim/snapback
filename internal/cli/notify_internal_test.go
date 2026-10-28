package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

func TestNotifyTest_SendsOneNotificationWithoutConfig(t *testing.T) {
	n := &notify.Fake{}
	root := swapSubcommand(t, "notify", newNotifyCmdWithDeps(notifyDeps{newNotifier: func() notify.Notifier { return n }}))
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	var out bytes.Buffer
	root.SetArgs([]string{"notify", "test", "--config", missing})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	want := notify.Notification{Title: "snapback", Subtitle: "Test notification", Message: "Notifications are working."}
	if n.Sent[0] != want {
		t.Errorf("notification = %+v, want %+v", n.Sent[0], want)
	}
	if !strings.Contains(out.String(), "Script Editor") {
		t.Errorf("stdout = %q, want the Script Editor permission hint", out.String())
	}
}

func TestNotifyTest_NotifierErrorPropagates(t *testing.T) {
	boom := errors.New("osascript exploded")
	n := &notify.Fake{Err: boom}
	root := swapSubcommand(t, "notify", newNotifyCmdWithDeps(notifyDeps{newNotifier: func() notify.Notifier { return n }}))
	root.SetArgs([]string{"notify", "test"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want it to wrap the notifier error", err)
	}
}
