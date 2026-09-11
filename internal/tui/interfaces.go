package tui

import "github.com/xortim/snapback/internal/progress"

// pipelineResult is implemented by *backup.Result and *backup.RestoreResult
// -- the two outcome types run.go's and restore.go's public entry points
// return. Decouples Model from importing internal/backup's concrete types
// directly.
type pipelineResult interface {
	Summary() string
}

// pipelineError is implemented by *backup.RunError and *backup.RestoreError
// -- both carry the progress.Stage active when their pipeline failed.
type pipelineError interface {
	error
	FailedStage() progress.Stage
}
