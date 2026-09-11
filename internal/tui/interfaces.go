package tui

import "github.com/xortim/snapback/internal/progress"

// pipelineResult is implemented by *backup.Result and *backup.RestoreResult
// -- the two outcome types run.go's and restore.go's public entry points
// return. Decouples Model from importing internal/backup's concrete types
// directly.
type pipelineResult interface {
	Summary() string
}

// pipelineError is implemented by *backup.RunError -- shared by both Run
// and Restore's failures, each carrying the progress.Stage active when
// their pipeline failed.
type pipelineError interface {
	error
	FailedStage() progress.Stage
}
