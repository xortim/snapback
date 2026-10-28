package notify_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xortim/snapback/internal/notify"
)

func TestSuccess(t *testing.T) {
	got := notify.Success("myvm", "myvm-20261026T000000Z")
	want := notify.Notification{Title: "snapback", Subtitle: "myvm backed up", Message: "myvm-20261026T000000Z"}
	if got != want {
		t.Errorf("Success() = %+v, want %+v", got, want)
	}
}

func TestSuccess_EmptyArchiveID_StillSaysComplete(t *testing.T) {
	if got := notify.Success("myvm", ""); got.Message != "backup complete" {
		t.Errorf("Message = %q, want %q fallback", got.Message, "backup complete")
	}
}

func TestFailure_Basic(t *testing.T) {
	got := notify.Failure("myvm", errors.New("snapshot: boom"), false)
	want := notify.Notification{Title: "snapback", Subtitle: "myvm backup failed", Message: "snapshot: boom", Sound: notify.FailureSound}
	if got != want {
		t.Errorf("Failure() = %+v, want %+v", got, want)
	}
}

func TestFailure_MultiLine_UsesFirstLine(t *testing.T) {
	got := notify.Failure("myvm", errors.New("copy failed\nvmcli stderr: details\nmore"), false)
	if got.Message != "copy failed" {
		t.Errorf("Message = %q, want first line only", got.Message)
	}
}

func TestFailure_Long_TruncatesTo200RunesWithEllipsis(t *testing.T) {
	long := strings.Repeat("é", 500)
	got := notify.Failure("myvm", errors.New(long), false)
	if n := utf8.RuneCountInString(got.Message); n != 200 {
		t.Errorf("Message rune count = %d, want 200", n)
	}
	if !strings.HasSuffix(got.Message, "…") {
		t.Errorf("Message = %q, want trailing ellipsis", got.Message)
	}
}

func TestFailure_SnapshotMayRemain_SuffixSurvivesTruncation(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := notify.Failure("myvm", errors.New(long), true)
	if !strings.HasSuffix(got.Message, " — run snapback cleanup --vm myvm") {
		t.Errorf("Message = %q, want cleanup suffix appended after truncation", got.Message)
	}
	if !strings.Contains(got.Message, "…") {
		t.Errorf("Message = %q, want the error part truncated", got.Message)
	}
}

func TestFailure_EmptyOrNilError(t *testing.T) {
	if got := notify.Failure("myvm", errors.New(""), false); got.Message != "unknown error" {
		t.Errorf("empty error Message = %q, want %q", got.Message, "unknown error")
	}
	if got := notify.Failure("myvm", nil, false); got.Message != "unknown error" {
		t.Errorf("nil error Message = %q, want %q", got.Message, "unknown error")
	}
}

func TestFailure_LeadingNewline_UsesFirstNonEmptyLine(t *testing.T) {
	got := notify.Failure("myvm", errors.New("\ncopy failed\nmore"), false).Message
	if got != "copy failed" {
		t.Errorf("Message = %q, want %q", got, "copy failed")
	}
}
