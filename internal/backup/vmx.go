// Package backup implements the snapshot -> copy -> merge -> archive
// choreography described in docs/design.md ("Backup choreography"),
// isolated behind the vm.Controller interface so it can be tested against
// vm.FakeVMController with no real VMware Fusion install required.
package backup

import (
	"bufio"
	"fmt"
	"os"
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
