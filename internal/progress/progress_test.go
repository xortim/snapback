package progress_test

import (
	"testing"

	"github.com/xortim/snapback/internal/progress"
)

func TestNoOpReporter_ReportDoesNotPanic(t *testing.T) {
	var r progress.Reporter = progress.NoOpReporter{}
	r.Report(progress.Event{Stage: progress.Snapshotting, Message: "test", Percent: 0.5})
}

func TestStages_AreDistinctValues(t *testing.T) {
	stages := []progress.Stage{
		progress.CheckingTools,
		progress.Snapshotting,
		progress.Copying,
		progress.Merging,
		progress.Compressing,
		progress.Checksumming,
		progress.Pruning,
		progress.Notifying,
		progress.Done,
	}
	seen := map[progress.Stage]bool{}
	for _, s := range stages {
		if seen[s] {
			t.Errorf("stage %v appears more than once in the const block", s)
		}
		seen[s] = true
	}
	if len(seen) != 9 {
		t.Errorf("got %d distinct stages, want 9", len(seen))
	}
}
