package backup_test

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestRunError_ErrorReturnsUnderlyingMessage(t *testing.T) {
	underlying := errors.New("snapshot failed")
	err := &backup.RunError{Stage: progress.Snapshotting, Err: underlying}

	if err.Error() != "snapshot failed" {
		t.Errorf("Error() = %q, want %q", err.Error(), "snapshot failed")
	}
}

func TestRunError_UnwrapAndErrorsAs(t *testing.T) {
	underlying := errors.New("boom")
	wrapped := error(&backup.RunError{Stage: progress.Copying, Err: underlying})

	if !errors.Is(wrapped, underlying) {
		t.Error("errors.Is(wrapped, underlying) = false, want true")
	}

	var runErr *backup.RunError
	if !errors.As(wrapped, &runErr) {
		t.Fatal("errors.As(wrapped, &runErr) = false, want true")
	}
	if runErr.Stage != progress.Copying {
		t.Errorf("runErr.Stage = %v, want %v", runErr.Stage, progress.Copying)
	}
}
