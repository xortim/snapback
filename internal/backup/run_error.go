package backup

import "github.com/xortim/snapback/internal/progress"

// RunError wraps an error returned by Run or Restore with the Stage active
// when the failure occurred. For Run, a caller can tell whether a snapshot
// may have been left behind on the source VM (Stage >= progress.Snapshotting)
// -- recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback in Run itself; that check is meaningless for a
// Restore error; since Restore never takes a snapshot in the first place,
// Restore's own stages (Verifying et al.) happen to sort above
// Snapshotting in the shared progress.Stage enum but a caller checking for
// an orphaned snapshot only ever does so against an error it knows came
// from Run. Shared between both pipelines rather than duplicated (see
// checkCtx's doc comment) since both need only a Stage and an underlying
// error.
type RunError struct {
	Stage progress.Stage
	Err   error
}

// Error implements the error interface, returning the underlying error's
// message unchanged.
func (e *RunError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error to errors.Is/errors.As.
func (e *RunError) Unwrap() error { return e.Err }

// FailedStage implements the pipelineError interface internal/tui's
// generalized Model matches failures against -- see RestoreError, which
// carries the same method for backup.Restore's failures.
func (e *RunError) FailedStage() progress.Stage { return e.Stage }
