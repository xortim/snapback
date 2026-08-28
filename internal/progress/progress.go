// Package progress defines the event vocabulary backup choreography and
// its renderers share, so choreography code never imports a rendering
// library directly (see docs/superpowers/specs/2026-08-23-cli-ux-design.md).
package progress

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
