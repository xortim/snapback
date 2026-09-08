// Package backup implements the snapshot -> copy -> merge -> archive
// choreography described in docs/design.md ("Backup choreography"),
// isolated behind the vm.Controller interface so it can be tested against
// vm.FakeVMController with no real VMware Fusion install required.
package backup

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// readGuestOS extracts the guestOS value from a .vmx file, e.g.
// `guestOS = "ubuntu-64"` -> "ubuntu-64". Returns "" if the key is absent.
func readGuestOS(vmxPath string) (string, error) {
	f, err := os.Open(vmxPath)
	if err != nil {
		return "", fmt.Errorf("read vmx: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) != "guestOS" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read vmx: %w", err)
	}
	return "", nil
}

// diskDeviceKey matches a virtual disk device's fileName key, e.g.
// "nvme0:0.fileName" or "scsi0:1.fileName" -- the four bus types VMware
// Fusion actually uses for disk devices. A CD-ROM/DVD device on the same
// bus (e.g. sata0:1 pointing at an .iso) has the identical key shape, so
// this alone doesn't distinguish a virtual disk from other device types;
// readDiskFiles below also requires the value to end in ".vmdk".
var diskDeviceKey = regexp.MustCompile(`^(scsi|sata|nvme|ide)\d+:\d+\.fileName$`)

// readDiskFiles returns the fileName value of every virtual disk device
// configured in vmxPath -- e.g. ["Virtual Disk.vmdk"] for a single-disk
// VM -- in the order encountered. Each is the *top* of that disk's
// snapshot chain (the file the device currently points at, not
// necessarily the base disk), which is exactly what
// vm.Controller.CheckDiskConsistency needs: checking the top validates
// every parent underneath it too. A device whose fileName isn't a
// ".vmdk" (a CD-ROM/DVD's .iso, or the "-1" empty-drive placeholder) is
// not a virtual disk and is skipped.
func readDiskFiles(vmxPath string) ([]string, error) {
	f, err := os.Open(vmxPath)
	if err != nil {
		return nil, fmt.Errorf("read vmx: %w", err)
	}
	defer func() { _ = f.Close() }()

	var diskFiles []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if !diskDeviceKey.MatchString(strings.TrimSpace(key)) {
			continue
		}
		fileName := strings.Trim(strings.TrimSpace(value), `"`)
		if !strings.HasSuffix(strings.ToLower(fileName), ".vmdk") {
			continue
		}
		diskFiles = append(diskFiles, fileName)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read vmx: %w", err)
	}
	return diskFiles, nil
}
