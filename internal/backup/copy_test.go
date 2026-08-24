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
	if err := copyDir(src, dst); err != nil {
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
	err := copyDir(filepath.Join(t.TempDir(), "does-not-exist"), dst)
	if err == nil {
		t.Fatal("copyDir() error = nil, want error for missing source")
	}
}
