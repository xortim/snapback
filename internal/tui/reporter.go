package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

// Sender is the one method of *tea.Program that Reporter needs --
// defined as an interface (rather than depending on *tea.Program
// directly) so tests can inject a fake instead of running a real
// bubbletea program just to exercise Report.
type Sender interface {
	Send(tea.Msg)
}

// Reporter forwards progress.Events to a running bubbletea program.
// Report is called from whatever goroutine backup.Run executes on --
// separate from the TUI's own event-loop goroutine -- and
// (*tea.Program).Send is documented as safe to call from any goroutine,
// including after the program has already quit (a no-op in that case).
type Reporter struct {
	sender Sender
}

// NewReporter returns a Reporter that forwards every Report call to s.
func NewReporter(s Sender) Reporter {
	return Reporter{sender: s}
}

// Report implements progress.Reporter.
func (r Reporter) Report(e progress.Event) {
	r.sender.Send(eventMsg(e))
}
