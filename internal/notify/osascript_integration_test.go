//go:build integration

package notify_test

// Sends one real notification via osascript, per README.md / CLAUDE.md:
//
//	SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
//
// Asserts only that osascript exited cleanly; whether the notification
// actually appeared (Script Editor's notification permission) has to be
// confirmed by eye.

import (
	"context"
	"os"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

func TestOSAScript_Integration_SendsRealNotification(t *testing.T) {
	if os.Getenv("SNAPBACK_INTEGRATION") != "1" {
		t.Skip("set SNAPBACK_INTEGRATION=1 to run osascript integration tests")
	}
	err := notify.NewOSAScript().Notify(context.Background(), notify.Notification{
		Title: "snapback", Subtitle: "integration test", Message: "x\" & \"injected?", Sound: notify.FailureSound,
	})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
}
