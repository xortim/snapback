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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// sha256File returns the lowercase hex-encoded SHA-256 digest of the file
// at path.
func sha256File(path string) (string, error) {
	return hashFile(path, nil)
}

// hashFile returns the lowercase hex-encoded SHA-256 digest of the file at
// path, same as sha256File, but additionally invokes onRead (if non-nil)
// with the running cumulative bytes read -- shared by sha256File
// (Run's Checksumming stage, no progress needed) and Restore's Verifying
// stage (restore.go), which does.
func hashFile(path string, onRead func(cumulative int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	var r io.Reader = f
	if onRead != nil {
		r = &countingReader{r: f, onRead: onRead}
	}
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// countingReader wraps an io.Reader, invoking onRead with the running
// cumulative byte count as bytes are read through it.
type countingReader struct {
	r          io.Reader
	onRead     func(cumulative int64)
	cumulative int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.cumulative += int64(n)
	if n > 0 && c.onRead != nil {
		c.onRead(c.cumulative)
	}
	return n, err
}
