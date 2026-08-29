package progress

import (
	"fmt"
	"io"
)

// TerminalReporter writes each Event carrying a Message or an Err as one
// "stage: message" (or "stage: err") line to W. Percent-only events
// (Copying/Compressing byte-count ticks with neither set) are dropped
// rather than printed -- backup.Run reports one of those per file copied,
// which would otherwise spam a terminal on a large VM bundle.
type TerminalReporter struct {
	W io.Writer
}

// NewTerminalReporter returns a TerminalReporter writing to w.
func NewTerminalReporter(w io.Writer) TerminalReporter {
	return TerminalReporter{W: w}
}

// Report implements Reporter.
func (r TerminalReporter) Report(e Event) {
	// Report has no error return (Reporter is a fire-and-forget progress
	// sink), so a write failure here has nowhere to go -- discard it
	// explicitly rather than leave it unchecked.
	switch {
	case e.Message != "" && e.Err != nil:
		_, _ = fmt.Fprintf(r.W, "%s: %s: %v\n", e.Stage, e.Message, e.Err)
	case e.Message != "":
		_, _ = fmt.Fprintf(r.W, "%s: %s\n", e.Stage, e.Message)
	case e.Err != nil:
		_, _ = fmt.Fprintf(r.W, "%s: %v\n", e.Stage, e.Err)
	}
}
