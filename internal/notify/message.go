package notify

import (
	"fmt"
	"strings"
)

// FailureSound is played for failure notifications only -- a success
// notification for a nightly backup should be quiet.
const FailureSound = "Basso"

// maxMessageRunes caps the error text in a failure notification;
// Notification Center truncates long bodies anyway, and a wrapped vmcli
// error can run to kilobytes.
const maxMessageRunes = 200

const title = "snapback"

// Success builds the notification for a completed backup.
func Success(vmName, archiveID string) Notification {
	msg := archiveID
	if msg == "" {
		msg = "backup complete"
	}
	return Notification{Title: title, Subtitle: vmName + " backed up", Message: msg}
}

// Failure builds the notification for a failed backup. snapshotMayRemain
// appends a pointer to `snapback cleanup` -- computed by the caller
// (internal/cli) with the same predicate its stderr warning uses, so the
// two can't disagree. The suffix is appended after truncation so it's
// never the part that gets cut off.
func Failure(vmName string, err error, snapshotMayRemain bool) Notification {
	msg := ""
	if err != nil {
		msg, _, _ = strings.Cut(strings.TrimSpace(err.Error()), "\n")
		msg = strings.TrimSpace(msg)
	}
	if msg == "" {
		msg = "unknown error"
	}
	if r := []rune(msg); len(r) > maxMessageRunes {
		msg = string(r[:maxMessageRunes-1]) + "…"
	}
	if snapshotMayRemain {
		msg += fmt.Sprintf(" — run snapback cleanup --vm %s", vmName)
	}
	return Notification{Title: title, Subtitle: vmName + " backup failed", Message: msg, Sound: FailureSound}
}
