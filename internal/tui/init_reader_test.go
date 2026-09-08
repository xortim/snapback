package tui

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestLineBufferedReader_PreservesLaterLinesAcrossSeparateScanners
// reproduces exactly what huh's accessible-mode prompts do internally
// (huh@v1.0.0/internal/accessibility/accessibility.go's PromptString):
// a fresh bufio.Scanner constructed per prompt, used for one Scan() call,
// then discarded. Without lineBufferedReader, the first scanner's first
// Read() on a strings.Reader returns the *entire* remaining string in one
// call (strings.Reader.Read has no per-call size limit of its own), so
// everything after the first line is buffered inside that discarded
// scanner and lost -- the next fresh scanner sees only EOF. This test
// fails without the fix and passes with it.
func TestLineBufferedReader_PreservesLaterLinesAcrossSeparateScanners(t *testing.T) {
	r := &lineBufferedReader{r: strings.NewReader("first\nsecond\nthird\n")}

	for _, want := range []string{"first", "second", "third"} {
		scanner := bufio.NewScanner(r)
		if !scanner.Scan() {
			t.Fatalf("Scan() = false before reading %q, want more input", want)
		}
		if got := scanner.Text(); got != want {
			t.Errorf("Text() = %q, want %q", got, want)
		}
	}
}

func TestLineBufferedReader_ReadsAtMostOneByte(t *testing.T) {
	r := &lineBufferedReader{r: strings.NewReader("abc")}
	buf := make([]byte, 8)

	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 1 {
		t.Errorf("Read() n = %d, want 1", n)
	}
	if buf[0] != 'a' {
		t.Errorf("Read() byte = %q, want %q", buf[0], 'a')
	}
}

// TestLineBufferedReader_SetsSawEOFOnEOF reproduces the root cause behind
// findings 1 and 2 of the whole-branch review: huh's accessible-mode
// prompts have no way to report reaching EOF, so without tracking it
// ourselves at this layer, a truncated or completely empty input reader
// would silently leave every remaining field at its default/last-attempted
// value instead of surfacing an error.
func TestLineBufferedReader_SetsSawEOFOnEOF(t *testing.T) {
	r := &lineBufferedReader{r: strings.NewReader("")}

	n, err := r.Read(make([]byte, 1))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v), want (0, io.EOF)", n, err)
	}
	if !r.sawEOF {
		t.Error("sawEOF = false, want true after Read returned io.EOF")
	}
}

func TestLineBufferedReader_SawEOFFalseWithoutEOF(t *testing.T) {
	r := &lineBufferedReader{r: strings.NewReader("a")}

	if _, err := r.Read(make([]byte, 1)); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if r.sawEOF {
		t.Error("sawEOF = true, want false: the reader hasn't hit EOF yet")
	}
}
