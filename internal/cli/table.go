package cli

import (
	"bytes"
	"io"
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
//
// writeRows must write its header via a line ending in "\n" (every
// current caller does this with a leading fmt.Fprintln) before returning
// -- renderTabwriterTable trusts that invariant rather than guarding
// against a header-less buffer that can't occur with a well-formed
// writeRows.
func renderTabwriterTable(out io.Writer, writeRows func(w *tabwriter.Writer) error) error {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	if err := writeRows(w); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	data := buf.Bytes()
	i := bytes.IndexByte(data, '\n')
	if _, err := io.WriteString(out, style.Header.Render(string(data[:i]))); err != nil {
		return err
	}
	_, err := out.Write(data[i:])
	return err
}
