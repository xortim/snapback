package tui

import (
	"errors"
	"io"
)

// lineBufferedReader wraps r so every Read call returns at most one
// byte. huh's accessible-mode prompts (charmbracelet/huh's internal
// accessibility package) each construct a fresh bufio.Scanner around
// whatever io.Reader the wizard is given, used for exactly one line
// before being discarded -- a bufio.Scanner reads in chunks, so on any
// reader whose single Read call can return more than one line at a time
// (a strings.Reader, or a pipe/redirected file with several lines
// already sitting in the OS buffer), that discarded scanner's read-ahead
// silently drops every line beyond the first before the next field's
// brand-new scanner ever sees it. Limiting each Read to one byte forces
// every such scanner to stop exactly at its own line's newline, leaving
// the underlying reader positioned correctly for the next prompt. Used
// for every accessible-mode form this package builds -- including real,
// non-terminal `snapback init` invocations (piped/redirected stdin), not
// only this package's own tests.
//
// sawEOF records whether the wrapped Read call itself ever returned
// io.EOF, checked by runForm after the form finishes running. huh's
// accessible-mode field prompts (accessibility.PromptString) have no way
// to report reaching EOF back up through huh.Form.RunWithContext -- on
// EOF they just silently fall back to whatever value was already sitting
// in the bound variable (see that function's own "no way to bubble up
// errors ... but the program is probably not continuing if stdin sent
// EOF" comment) or, for a field that already failed validation once, the
// unvalidated last-attempted string -- and huh's runAccessible always
// returns nil regardless (verified by reading huh@v1.0.0/form.go).
// Without tracking this ourselves, truncated accessible-mode input (a
// piped/redirected stdin that runs out mid-wizard, or a completely empty
// io.Reader) would silently produce a fully-populated, never-actually-
// reviewed config instead of an error -- exactly what the old hand-rolled
// prompter guarded against ("a truncated interactive session means the
// resulting config was never actually reviewed by the user").
type lineBufferedReader struct {
	r      io.Reader
	sawEOF bool
}

// Read uses a pointer receiver (unlike a typical small wrapper like this)
// specifically so sawEOF survives past the call: runForm hands
// huh.Form.WithInput a *lineBufferedReader so the same struct instance is
// still reachable, and still mutated, after WithInput has copied its
// io.Reader interface value away.
func (l *lineBufferedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := l.r.Read(p[:1])
	if errors.Is(err, io.EOF) {
		l.sawEOF = true
	}
	return n, err
}
