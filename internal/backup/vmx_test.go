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

func TestReadDiskFiles_SingleDisk(t *testing.T) {
	path := writeTempVMX(t, "nvme0:0.fileName = \"Virtual Disk.vmdk\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "Virtual Disk.vmdk" {
		t.Errorf("readDiskFiles() = %v, want [Virtual Disk.vmdk]", got)
	}
}

func TestReadDiskFiles_MultipleDisksAcrossBusTypes(t *testing.T) {
	path := writeTempVMX(t, "scsi0:0.fileName = \"Disk A.vmdk\"\nsata0:0.fileName = \"Disk B.vmdk\"\nide0:0.fileName = \"Disk C.vmdk\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	want := []string{"Disk A.vmdk", "Disk B.vmdk", "Disk C.vmdk"}
	if len(got) != len(want) {
		t.Fatalf("readDiskFiles() = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("readDiskFiles()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestReadDiskFiles_SkipsCDROMAndEmptyDriveDevices(t *testing.T) {
	path := writeTempVMX(t, "nvme0:0.fileName = \"Virtual Disk.vmdk\"\nsata0:1.fileName = \"/Users/tim/Downloads/ubuntu.iso\"\nsound.fileName = \"-1\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "Virtual Disk.vmdk" {
		t.Errorf("readDiskFiles() = %v, want only the .vmdk device, not the ISO/empty-drive entries", got)
	}
}

func TestReadDiskFiles_SkipsDisconnectedDevice(t *testing.T) {
	path := writeTempVMX(t, "nvme0:0.fileName = \"Virtual Disk.vmdk\"\nsata0:1.present = \"FALSE\"\nsata0:1.fileName = \"OldDisk.vmdk\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "Virtual Disk.vmdk" {
		t.Errorf("readDiskFiles() = %v, want only the connected disk, not the stale disconnected device's fileName", got)
	}
}

func TestReadDiskFiles_PresentTrueDeviceIsKept(t *testing.T) {
	path := writeTempVMX(t, "nvme0:0.present = \"TRUE\"\nnvme0:0.fileName = \"Virtual Disk.vmdk\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "Virtual Disk.vmdk" {
		t.Errorf("readDiskFiles() = %v, want [Virtual Disk.vmdk]", got)
	}
}

func TestReadDiskFiles_PresentKeyBeforeFileNameKey(t *testing.T) {
	// The "present" key can appear before its sibling "fileName" key in a
	// real .vmx file -- this must still be caught even though readDiskFiles
	// hasn't seen the fileName key yet at the point it encounters "present".
	path := writeTempVMX(t, "sata0:1.present = \"FALSE\"\nsata0:1.fileName = \"OldDisk.vmdk\"\nnvme0:0.fileName = \"Virtual Disk.vmdk\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "Virtual Disk.vmdk" {
		t.Errorf("readDiskFiles() = %v, want only the connected disk", got)
	}
}

func TestReadDiskFiles_NoDiskDevicesReturnsEmpty(t *testing.T) {
	path := writeTempVMX(t, "guestOS = \"ubuntu-64\"\n")

	got, err := readDiskFiles(path)
	if err != nil {
		t.Fatalf("readDiskFiles() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("readDiskFiles() = %v, want empty", got)
	}
}

func TestReadDiskFiles_MissingFileReturnsError(t *testing.T) {
	_, err := readDiskFiles(filepath.Join(t.TempDir(), "does-not-exist.vmx"))
	if err == nil {
		t.Fatal("readDiskFiles() error = nil, want error for missing file")
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
