package tui

import "io"

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
type lineBufferedReader struct {
	r io.Reader
}

func (l lineBufferedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return l.r.Read(p[:1])
}
