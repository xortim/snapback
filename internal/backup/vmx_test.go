package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadGuestOS_ParsesQuotedValue(t *testing.T) {
	path := writeTempVMX(t, ".encoding = \"UTF-8\"\ndisplayName = \"dev-ubuntu\"\nguestOS = \"ubuntu-64\"\n")

	got, err := readGuestOS(path)
	if err != nil {
		t.Fatalf("readGuestOS() error = %v, want nil", err)
	}
	if got != "ubuntu-64" {
		t.Errorf("readGuestOS() = %q, want %q", got, "ubuntu-64")
	}
}

func TestReadGuestOS_MissingKeyReturnsEmpty(t *testing.T) {
	path := writeTempVMX(t, "displayName = \"no-guestos-here\"\n")

	got, err := readGuestOS(path)
	if err != nil {
		t.Fatalf("readGuestOS() error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("readGuestOS() = %q, want empty", got)
	}
}

func TestReadGuestOS_MissingFileReturnsError(t *testing.T) {
	_, err := readGuestOS(filepath.Join(t.TempDir(), "does-not-exist.vmx"))
	if err == nil {
		t.Fatal("readGuestOS() error = nil, want error for missing file")
	}
}

func writeTempVMX(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "example.vmx")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	return path
}
