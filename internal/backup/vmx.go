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

// scanVMXKeys opens vmxPath and calls fn with each line's key and value
// (already trimmed and, for the value, unquoted), in order. Shared by
// readGuestOS and readDiskFiles, which both otherwise duplicate this exact
// open/scan/TrimSpace/Cut-on-'='/TrimSpace-value skeleton.
func scanVMXKeys(vmxPath string, fn func(key, value string)) error {
	f, err := os.Open(vmxPath)
	if err != nil {
		return fmt.Errorf("read vmx: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fn(strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `"`))
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read vmx: %w", err)
	}
	return nil
}

// readGuestOS extracts the guestOS value from a .vmx file, e.g.
// `guestOS = "ubuntu-64"` -> "ubuntu-64". Returns "" if the key is absent.
func readGuestOS(vmxPath string) (string, error) {
	var guestOS string
	found := false
	err := scanVMXKeys(vmxPath, func(key, value string) {
		if !found && key == "guestOS" {
			guestOS = value
			found = true
		}
	})
	if err != nil {
		return "", err
	}
	return guestOS, nil
}

// diskDeviceKey matches a virtual disk device's fileName key, e.g.
// "nvme0:0.fileName" or "scsi0:1.fileName" -- the four bus types VMware
// Fusion actually uses for disk devices. A CD-ROM/DVD device on the same
// bus (e.g. sata0:1 pointing at an .iso) has the identical key shape, so
// this alone doesn't distinguish a virtual disk from other device types;
// readDiskFiles below also requires the value to end in ".vmdk".
var diskDeviceKey = regexp.MustCompile(`^(scsi|sata|nvme|ide)\d+:\d+\.fileName$`)

// setVMXKey sets key = "value" in the .vmx file at vmxPath, replacing an
// existing line for key (matched the same way scanVMXKeys parses lines,
// case-sensitively) if present, or appending a new line if not. Every other
// line, including comments and formatting, is left untouched.
func setVMXKey(vmxPath, key, value string) error {
	data, err := os.ReadFile(vmxPath)
	if err != nil {
		return fmt.Errorf("read vmx: %w", err)
	}
	info, err := os.Stat(vmxPath)
	if err != nil {
		return fmt.Errorf("stat vmx: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	newLine := fmt.Sprintf("%s = %q", key, value)
	found := false
	for i, line := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == key {
			// Preserve the replaced line's own CRLF-vs-LF ending so a
			// CRLF-encoded .vmx doesn't end up with one lone LF line
			// among otherwise-CRLF ones.
			if strings.HasSuffix(line, "\r") {
				lines[i] = newLine + "\r"
			} else {
				lines[i] = newLine
			}
			found = true
			break
		}
	}
	if !found {
		// A file ending in a newline splits into a trailing "" element;
		// insert before it so the new key doesn't end up on its own line
		// after a stray blank line.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = append(lines[:n-1], newLine, "")
		} else {
			lines = append(lines, newLine)
		}
	}

	if err := os.WriteFile(vmxPath, []byte(strings.Join(lines, "\n")), info.Mode()); err != nil {
		return fmt.Errorf("write vmx: %w", err)
	}
	return nil
}

// readDiskFiles returns the fileName value of every *connected* virtual
// disk device configured in vmxPath -- e.g. ["Virtual Disk.vmdk"] for a
// single-disk VM -- in the order encountered. Each is the *top* of that
// disk's snapshot chain (the file the device currently points at, not
// necessarily the base disk), which is exactly what
// vm.Controller.CheckDiskConsistency needs: checking the top validates
// every parent underneath it too. A device whose fileName isn't a
// ".vmdk" (a CD-ROM/DVD's .iso, or the "-1" empty-drive placeholder) is
// not a virtual disk and is skipped.
//
// A device's sibling "<bus><n>:<n>.present" key is also consulted: when
// present and "FALSE" (case-insensitive), the device is disconnected and
// its fileName is skipped even if it still ends in ".vmdk". Fusion can
// leave a stale fileName behind after a disk is removed via the UI, and
// checking a disconnected disk that no longer exists would otherwise fail
// the whole backup over a device that isn't actually part of the VM
// anymore. Two passes over the file's keys are needed here (unlike
// readGuestOS) because the "present" key can appear before or after its
// sibling "fileName" key.
func readDiskFiles(vmxPath string) ([]string, error) {
	kv := make(map[string]string)
	var deviceKeys []string
	err := scanVMXKeys(vmxPath, func(key, value string) {
		kv[key] = value
		if diskDeviceKey.MatchString(key) {
			deviceKeys = append(deviceKeys, key)
		}
	})
	if err != nil {
		return nil, err
	}

	var diskFiles []string
	for _, key := range deviceKeys {
		fileName := kv[key]
		if !strings.HasSuffix(strings.ToLower(fileName), ".vmdk") {
			continue
		}
		presentKey := strings.TrimSuffix(key, "fileName") + "present"
		if present, ok := kv[presentKey]; ok && strings.EqualFold(present, "FALSE") {
			continue
		}
		diskFiles = append(diskFiles, fileName)
	}
	return diskFiles, nil
}
