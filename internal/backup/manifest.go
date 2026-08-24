package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

// Manifest records everything needed to identify and verify a backup
// archive after the fact: which VM it came from, how consistent it is
// (ToolsState), and its checksum. Written alongside the archive as
// manifest.json.
type Manifest struct {
	VMName      string        `json:"vm_name"`
	GuestOS     string        `json:"guest_os"`
	SizeBytes   int64         `json:"size_bytes"`
	Comment     string        `json:"comment"`
	Timestamp   time.Time     `json:"timestamp"`
	ToolsState  vm.ToolsState `json:"tools_state"`
	SHA256      string        `json:"sha256"`
	Compression string        `json:"compression"`
}

// writeManifest marshals m as indented JSON to path.
func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// sha256File returns the lowercase hex-encoded SHA-256 digest of the file
// at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
