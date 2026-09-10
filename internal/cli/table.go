package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/xortim/snapback/internal/style"
)

// renderTabwriterTable runs writeRows against a tabwriter targeting an
// internal buffer, then bolds only the header line (the buffer's first
// line) before writing the result to out. The bolding happens as a
// whole-line wrap after tabwriter has already computed every column's
// width from the buffered, unstyled text -- bolding individual header
// cells up front would inflate tabwriter's rune-count math with invisible
// ANSI codes and misalign the header against the unstyled rows below it.
func renderTabwriterTable(out io.Writer, writeRows func(w *tabwriter.Writer) error) error {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	if err := writeRows(w); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	header, rest, found := strings.Cut(buf.String(), "\n")
	if !found {
		_, err := fmt.Fprint(out, style.Header.Render(header))
		return err
	}
	_, err := fmt.Fprintf(out, "%s\n%s", style.Header.Render(header), rest)
	return err
}
