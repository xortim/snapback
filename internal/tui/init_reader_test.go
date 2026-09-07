package tui

import (
	"bufio"
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
	r := lineBufferedReader{r: strings.NewReader("first\nsecond\nthird\n")}

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
	r := lineBufferedReader{r: strings.NewReader("abc")}
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
