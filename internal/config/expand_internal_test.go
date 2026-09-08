package config

import (
	"path/filepath"
	"testing"
)

func TestExpandTilde_ExpandsBareTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ExpandTilde("~")
	if err != nil {
		t.Fatalf("ExpandTilde(\"~\") error = %v", err)
	}
	if got != home {
		t.Errorf("ExpandTilde(\"~\") = %q, want %q", got, home)
	}
}

func TestExpandTilde_ExpandsTildeSlashPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ExpandTilde("~/Virtual Machines/dev.vmwarevm/dev.vmx")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	want := filepath.Join(home, "Virtual Machines/dev.vmwarevm/dev.vmx")
	if got != want {
		t.Errorf("ExpandTilde(...) = %q, want %q", got, want)
	}
}

func TestExpandTilde_LeavesAbsolutePathUnchanged(t *testing.T) {
	got, err := ExpandTilde("/Volumes/Backups/snapback")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "/Volumes/Backups/snapback" {
		t.Errorf("ExpandTilde(absolute) = %q, want it unchanged", got)
	}
}

func TestExpandTilde_LeavesOtherUserTildeUnchanged(t *testing.T) {
	got, err := ExpandTilde("~otheruser/foo")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "~otheruser/foo" {
		t.Errorf("ExpandTilde(~otheruser/foo) = %q, want it unchanged (this package only resolves the current user's home)", got)
	}
}
