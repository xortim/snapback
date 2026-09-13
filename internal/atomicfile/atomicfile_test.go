package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFile_CreatesParentDirAndWritesContentWithPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.txt")
	want := []byte("hello")

	if err := WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissions = %o, want %o", perm, 0o644)
	}
}

// TestWriteFile_OverwritesExistingFileAtomically covers the reason this
// package exists: a rewrite of an existing file must never leave it
// truncated -- WriteFile writes to a temp file first and renames it into
// place, so the target always ends up either fully old or fully new.
func TestWriteFile_OverwritesExistingFileAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original-longer-content"), 0o644); err != nil {
		t.Fatalf("seed WriteFile() error = %v", err)
	}

	want := []byte("new")
	if err := WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("dir entries = %d, want 1 (no leftover temp file)", len(entries))
	}
}
