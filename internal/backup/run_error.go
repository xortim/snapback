package backup

import "github.com/xortim/snapback/internal/progress"

// RunError wraps an error returned by Run with the Stage active when the
// failure occurred, so a caller can tell whether a snapshot may have been
// left behind on the source VM (Stage >= progress.Snapshotting) --
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback in Run itself.
type RunError struct {
	Stage progress.Stage
	Err   error
}

// Error implements the error interface, returning the underlying error's
// message unchanged.
func (e *RunError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error to errors.Is/errors.As.
func (e *RunError) Unwrap() error { return e.Err }
