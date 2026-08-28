package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize_SumsRegularFilesRecursively(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize() error = %v, want nil", err)
	}
	const want = 5 + 10
	if got != want {
		t.Errorf("dirSize() = %d, want %d", got, want)
	}
}

func TestDirSize_IgnoresDirectoryEntriesThemselves(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty-sub"), 0o755); err != nil {
		t.Fatalf("mkdir empty-sub: %v", err)
	}

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize() error = %v, want nil", err)
	}
	if got != 0 {
		t.Errorf("dirSize() = %d, want 0 for a tree with only empty directories", got)
	}
}

func TestDirSize_MissingRootReturnsError(t *testing.T) {
	_, err := dirSize(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("dirSize() error = nil, want error for missing root")
	}
}
