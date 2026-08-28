// Package progress defines the event vocabulary backup choreography and
// its renderers share, so choreography code never imports a rendering
// library directly (see docs/superpowers/specs/2026-08-23-cli-ux-design.md).
package progress

import "fmt"

// Stage identifies which step of a choreography pipeline an Event
// describes.
type Stage int

const (
	CheckingTools Stage = iota
	Snapshotting
	Copying
	Merging
	Compressing
	Checksumming
	Pruning
	Notifying
	Done
)

// stageNames is indexed by Stage; must stay in sync with the const block
// above.
var stageNames = [...]string{
	CheckingTools: "checking tools",
	Snapshotting:  "snapshotting",
	Copying:       "copying",
	Merging:       "merging",
	Compressing:   "compressing",
	Checksumming:  "checksumming",
	Pruning:       "pruning",
	Notifying:     "notifying",
	Done:          "done",
}

// String returns a human-readable label for s, or "stage(N)" for an
// out-of-range value.
func (s Stage) String() string {
	if s < 0 || int(s) >= len(stageNames) {
		return fmt.Sprintf("stage(%d)", int(s))
	}
	return stageNames[s]
}

// Event is one progress update emitted by a choreography pipeline.
type Event struct {
	Stage   Stage
	Message string
	Percent float64 // set for Copying/Compressing, where a byte count is known
	Err     error
}

// Reporter receives progress Events. Implementations render them however
// they like (a TUI, a plain log line, or nothing at all); choreography
// code depends only on this interface, never on a specific renderer.
type Reporter interface {
	Report(Event)
}

// NoOpReporter discards every Event. Use it wherever progress reporting
// isn't needed -- e.g. tests that assert on a Result or error, not on
// emitted events.
type NoOpReporter struct{}

// Report implements Reporter by doing nothing.
func (NoOpReporter) Report(Event) {}
