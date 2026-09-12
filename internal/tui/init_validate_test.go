package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWritableDestination_ExistingWritableDir_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if err := validateWritableDestination(filepath.Join(dir, "backups")); err != nil {
		t.Errorf("validateWritableDestination() error = %v, want nil for a writable ancestor", err)
	}
}

func TestValidateWritableDestination_Empty_ReturnsError(t *testing.T) {
	if err := validateWritableDestination("   "); err == nil {
		t.Error("validateWritableDestination(\"   \") error = nil, want an error")
	}
}

func TestValidateWritableDestination_NoExistingAncestor_ReturnsError(t *testing.T) {
	err := validateWritableDestination("/this/path/almost-certainly/does/not/exist/anywhere")
	if err == nil {
		t.Error("validateWritableDestination() error = nil, want an error when no ancestor exists")
	}
}

func TestValidateWritableDestination_MountRootAncestor_ReturnsNilDespiteBeingUnwritable(t *testing.T) {
	// Reproduces a destination typed under an unmounted backup drive's
	// mountpoint (e.g. /Volumes/Backups/snapback): the walk finds no
	// existing "Backups" subdirectory and lands on the mount root itself,
	// which is unwritable by design. volumesMountRoot is swapped for a
	// dir this test controls since the real /Volumes doesn't exist on the
	// ubuntu-latest CI runner (see volumesMountRoot's doc comment).
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	orig := volumesMountRoot
	volumesMountRoot = root
	t.Cleanup(func() { volumesMountRoot = orig })

	err := validateWritableDestination(filepath.Join(root, "Backups", "snapback"))
	if err != nil {
		t.Errorf("validateWritableDestination() error = %v, want nil when landing on the mount root", err)
	}
}

func TestValidateWritableDestination_UnwritableAncestor_ReturnsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block writes")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := validateWritableDestination(filepath.Join(dir, "backups"))
	if err == nil {
		t.Error("validateWritableDestination() error = nil, want an error for a read-only ancestor")
	}
}

func TestValidateNonNegativeInt(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"0", false},
		{"5", false},
		{"  7  ", false},
		{"-1", true},
		{"notanumber", true},
		{"", true},
	}
	for _, tt := range tests {
		err := validateNonNegativeInt(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateNonNegativeInt(%q) error = %v, wantErr = %v", tt.in, err, tt.wantErr)
		}
	}
	if err := validateNonNegativeInt("notanumber"); err == nil || !strings.Contains(err.Error(), "not a whole number") {
		t.Errorf("validateNonNegativeInt(%q) error = %v, want it to mention \"not a whole number\"", "notanumber", err)
	}
}

func TestAcceptBlankInAccessibleMode_Accessible_BlankPassesUnvalidated(t *testing.T) {
	wrapped := acceptBlankInAccessibleMode(true, validateWritableDestination)
	// validateWritableDestination on its own rejects "" (see
	// TestValidateWritableDestination_Empty_ReturnsError above) -- the
	// wrapper must short-circuit before ever calling it.
	if err := wrapped(""); err != nil {
		t.Errorf("wrapped(\"\") error = %v, want nil in accessible mode", err)
	}
	if err := wrapped("   "); err != nil {
		t.Errorf("wrapped(\"   \") error = %v, want nil (whitespace-only) in accessible mode", err)
	}
}

func TestAcceptBlankInAccessibleMode_Accessible_NonBlankStillValidated(t *testing.T) {
	wrapped := acceptBlankInAccessibleMode(true, validateWritableDestination)
	err := wrapped("/this/path/almost-certainly/does/not/exist/anywhere")
	if err == nil {
		t.Error("wrapped(non-blank) error = nil, want the underlying validator's error to still apply")
	}
}

func TestAcceptBlankInAccessibleMode_NotAccessible_BlankStillValidated(t *testing.T) {
	wrapped := acceptBlankInAccessibleMode(false, validateWritableDestination)
	if err := wrapped(""); err == nil {
		t.Error("wrapped(\"\") error = nil, want the underlying validator's rejection to still apply outside accessible mode")
	}
}
