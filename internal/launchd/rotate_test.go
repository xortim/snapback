package launchd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateIfOversized_MissingFile_NoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.log")
	if err := RotateIfOversized(path); err != nil {
		t.Errorf("RotateIfOversized() on a missing file = %v, want nil", err)
	}
}

func TestRotateIfOversized_SmallFile_NoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.log")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := RotateIfOversized(path); err != nil {
		t.Fatalf("RotateIfOversized() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "small" {
		t.Errorf("file content = %q, err = %v; want untouched \"small\"", data, err)
	}
}

func TestRotateIfOversized_ShiftsGenerationsAndDropsTheOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.log")
	oversized := make([]byte, maxLogBytes)

	mustWrite := func(p string, content []byte) {
		t.Helper()
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	mustWrite(path, oversized)
	mustWrite(path+".1", []byte("generation-1"))
	mustWrite(path+".2", []byte("generation-2-should-be-deleted"))

	if err := RotateIfOversized(path); err != nil {
		t.Fatalf("RotateIfOversized() error = %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("current log path still exists after rotation, want it renamed away")
	}
	gen1, err := os.ReadFile(path + ".1")
	if err != nil || len(gen1) != maxLogBytes {
		t.Errorf(".1 content = %d bytes, err = %v; want the just-rotated oversized content", len(gen1), err)
	}
	gen2, err := os.ReadFile(path + ".2")
	if err != nil || string(gen2) != "generation-1" {
		t.Errorf(".2 content = %q, err = %v; want the old .1's content shifted up", gen2, err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Error("a .3 generation exists, want the oldest generation deleted rather than kept")
	}
}
