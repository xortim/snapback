package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDir_CopiesNestedFilesAndPreservesSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatalf("write top.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested.txt: %v", err)
	}
	if err := os.Symlink("top.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyDir(src, dst, nil); err != nil {
		t.Fatalf("copyDir() error = %v, want nil", err)
	}

	top, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil || string(top) != "top" {
		t.Errorf("top.txt = %q, %v, want %q, nil", top, err, "top")
	}
	nested, err := os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	if err != nil || string(nested) != "nested" {
		t.Errorf("sub/nested.txt = %q, %v, want %q, nil", nested, err, "nested")
	}
	link, err := os.Readlink(filepath.Join(dst, "link.txt"))
	if err != nil || link != "top.txt" {
		t.Errorf("link.txt = %q, %v, want %q, nil", link, err, "top.txt")
	}
}

func TestCopyDir_MissingSourceReturnsError(t *testing.T) {
	dst := t.TempDir()
	err := copyDir(filepath.Join(t.TempDir(), "does-not-exist"), dst, nil)
	if err == nil {
		t.Fatal("copyDir() error = nil, want error for missing source")
	}
}

func TestCopyDir_InvokesOnCopyWithCumulativeBytes(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	var calls []int64
	if err := copyDir(src, dst, func(cumulativeBytes int64) {
		calls = append(calls, cumulativeBytes)
	}); err != nil {
		t.Fatalf("copyDir() error = %v, want nil", err)
	}

	if len(calls) != 2 {
		t.Fatalf("onCopy called %d times, want 2 (one per file)", len(calls))
	}
	last := calls[len(calls)-1]
	if last != 15 {
		t.Errorf("final cumulative bytes = %d, want %d (5 + 10)", last, 15)
	}
}
