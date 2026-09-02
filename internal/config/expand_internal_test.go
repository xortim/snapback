package config

import (
	"path/filepath"
	"testing"
)

func TestExpandTilde_ExpandsBareTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := expandTilde("~")
	if err != nil {
		t.Fatalf("expandTilde(\"~\") error = %v", err)
	}
	if got != home {
		t.Errorf("expandTilde(\"~\") = %q, want %q", got, home)
	}
}

func TestExpandTilde_ExpandsTildeSlashPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := expandTilde("~/Virtual Machines/dev.vmwarevm/dev.vmx")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	want := filepath.Join(home, "Virtual Machines/dev.vmwarevm/dev.vmx")
	if got != want {
		t.Errorf("expandTilde(...) = %q, want %q", got, want)
	}
}

func TestExpandTilde_LeavesAbsolutePathUnchanged(t *testing.T) {
	got, err := expandTilde("/Volumes/Backups/snapback")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "/Volumes/Backups/snapback" {
		t.Errorf("expandTilde(absolute) = %q, want it unchanged", got)
	}
}

func TestExpandTilde_LeavesOtherUserTildeUnchanged(t *testing.T) {
	got, err := expandTilde("~otheruser/foo")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "~otheruser/foo" {
		t.Errorf("expandTilde(~otheruser/foo) = %q, want it unchanged (this package only resolves the current user's home)", got)
	}
}
