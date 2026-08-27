# Backup Choreography Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the snapshot → copy → merge → archive backup choreography (issue #5) as a new `internal/backup` package, tested entirely against `vm.FakeVMController` — no real VM required.

**Architecture:** One exported entry point, `backup.Run(ctrl vm.Controller, opts backup.Options) (*backup.Result, error)`, orchestrating small unexported helpers (VMX parsing, recursive directory copy, tar+compress, checksum, manifest write). Each helper is a pure/isolated piece of I/O logic tested directly (whitebox, `package backup`); `Run` itself is tested only through its exported surface (blackbox, `package backup_test`), matching how `internal/vm` and `internal/config` are already tested in this repo.

**Tech Stack:** Go stdlib only (`archive/tar`, `compress/gzip`, `crypto/sha256`, `encoding/json`, `os/exec` for shelling out to `zstd`). No new go.mod dependencies.

**Spec:** `docs/design.md` § "Backup choreography" (lines 65–88); issue https://github.com/xortim/snapback/issues/5 (scopes this to steps 1–8 of its own list — the copy/checksum/manifest pipeline only; retention pruning and notifications are separate, later issues).

## Global Constraints

- Go 1.26.5, module `github.com/xortim/snapback`. New package: `internal/backup` (package name `backup`).
- No `.golangci.yml` exists — `make lint` runs golangci-lint v2's default "standard" set: `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`. Every returned error must be checked, including from deferred `Close()` calls (`defer func() { _ = f.Close() }()` or an explicit close-and-check on the success path, never a bare `defer f.Close()`).
- Match existing test conventions (`internal/vm/fake_test.go`, `internal/config/config_test.go`): explicit `t.Fatalf`/`t.Errorf` (no table-driven abstraction needed here), `t.TempDir()` for scratch files, `t.Helper()` in local test helpers, `errors.Is`/a local sentinel `errBoom` for injected-error assertions.
- Per `CLAUDE.md` and `docs/design.md`: if the process fails after a snapshot is taken, leaving an orphaned `snapback-<timestamp>` snapshot on the source VM is an **accepted, documented outcome** — recovered later by the separate `snapback cleanup` command (issue #10), not by automatic rollback here. `Run` must not attempt to delete a snapshot as error-path cleanup.
- Manifest fields per `docs/design.md`: VM name, guest OS, size, comment, timestamp, `tools_state` — plus `sha256` and `compression` (both required for the manifest to be self-verifying; not explicitly named in the design doc's manifest field list but required by step 9 "sha256 the archive").
- Out of scope (do not implement): retention pruning (design.md step 11), `osascript` notifications (step 12), the real `vmcli`/`vmrun`-backed `Controller` (issue #11), and CLI wiring (`snapback run`, issue #7) — `Run` is a library call the `run` command will invoke later.
- `zstd` is installed on the primary dev machine (`/opt/homebrew/bin/zstd`) but must not be assumed present in CI or on a fresh clone — the fallback-to-gzip path is a first-class, always-tested behavior, not an edge case.

---

### Task 1: VMX guest OS parsing

**Files:**
- Create: `internal/backup/vmx.go`
- Test: `internal/backup/vmx_test.go` (package `backup` — whitebox, this helper is unexported)

**Interfaces:**
- Produces: `readGuestOS(vmxPath string) (string, error)` — reads a `.vmx` file and returns the value of its `guestOS = "..."` line, or `""` if the key is absent. Used by Task 5's `Run`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/backup/vmx_test.go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestReadGuestOS -v`
Expected: FAIL — `undefined: readGuestOS` (package `internal/backup` doesn't exist yet)

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backup/vmx.go

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run TestReadGuestOS -v`
Expected: PASS (all three subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/backup/vmx.go internal/backup/vmx_test.go
git commit -m "backup: parse guestOS from .vmx files"
```

---

### Task 2: Recursive bundle copy

**Files:**
- Create: `internal/backup/copy.go`
- Test: `internal/backup/copy_test.go` (package `backup` — whitebox)

**Interfaces:**
- Produces: `copyDir(src, dst string) error` — recursively copies the contents of `src` into `dst` (creating `dst` and subdirectories as needed); regular files copied byte-for-byte, symlinks recreated (not followed/dereferenced). Used by Task 5's `Run` to stage the `.vmwarevm` bundle.

- [ ] **Step 1: Write the failing tests**

```go
// internal/backup/copy_test.go
package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDir_CopiesNestedFilesAndPreservesSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatalf("write top.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested.txt: %v", err)
	}
	if err := os.Symlink("top.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir() error = %v, want nil", err)
	}

	top, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil || string(top) != "top" {
		t.Errorf("top.txt = %q, %v, want %q, nil", top, err, "top")
	}
	nested, err := os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	if err != nil || string(nested) != "nested" {
		t.Errorf("sub/nested.txt = %q, %v, want %q, nil", nested, err, "nested")
	}
	link, err := os.Readlink(filepath.Join(dst, "link.txt"))
	if err != nil || link != "top.txt" {
		t.Errorf("link.txt = %q, %v, want %q, nil", link, err, "top.txt")
	}
}

func TestCopyDir_MissingSourceReturnsError(t *testing.T) {
	dst := t.TempDir()
	err := copyDir(filepath.Join(t.TempDir(), "does-not-exist"), dst)
	if err == nil {
		t.Fatal("copyDir() error = nil, want error for missing source")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestCopyDir -v`
Expected: FAIL — `undefined: copyDir`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backup/copy.go
package backup

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyDir recursively copies the contents of src into dst, creating dst
// and any subdirectories as needed. Regular files are copied byte-for-byte;
// symlinks are recreated as symlinks (not followed).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			return os.Symlink(link, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return out.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run TestCopyDir -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup/copy.go internal/backup/copy_test.go
git commit -m "backup: add recursive directory copy for staging the .vmwarevm bundle"
```

---

### Task 3: Manifest and checksum

**Files:**
- Create: `internal/backup/manifest.go`
- Test: `internal/backup/manifest_test.go` (package `backup` — whitebox)

**Interfaces:**
- Consumes: `vm.ToolsState` from `github.com/xortim/snapback/internal/vm` (already defined).
- Produces:
  - `type Manifest struct { VMName, GuestOS, Comment, SHA256, Compression string; SizeBytes int64; Timestamp time.Time; ToolsState vm.ToolsState }` (JSON tags: `vm_name`, `guest_os`, `size_bytes`, `comment`, `timestamp`, `tools_state`, `sha256`, `compression`)
  - `writeManifest(path string, m Manifest) error` — marshals `m` as indented JSON to `path`.
  - `sha256File(path string) (string, error)` — lowercase hex SHA-256 digest of the file at `path`.
  - Both used by Task 5's `Run`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/backup/manifest_test.go
package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

func TestWriteManifest_WritesReadableJSON(t *testing.T) {
	m := Manifest{
		VMName:      "dev-ubuntu",
		GuestOS:     "ubuntu-64",
		SizeBytes:   1024,
		Comment:     "nightly auto-backup",
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ToolsState:  vm.ToolsRunning,
		SHA256:      "deadbeef",
		Compression: "zstd",
	}
	path := filepath.Join(t.TempDir(), "manifest.json")

	if err := writeManifest(path, m); err != nil {
		t.Fatalf("writeManifest() error = %v, want nil", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}
	if got.VMName != m.VMName || got.GuestOS != m.GuestOS || got.SizeBytes != m.SizeBytes ||
		got.Comment != m.Comment || !got.Timestamp.Equal(m.Timestamp) || got.ToolsState != m.ToolsState ||
		got.SHA256 != m.SHA256 || got.Compression != m.Compression {
		t.Errorf("round-tripped manifest = %+v, want %+v", got, m)
	}
}

func TestSHA256File_MatchesKnownDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File() error = %v, want nil", err)
	}
	want := "b94d27b9934d3e08a52e52d7da7dacefb0de0d61f6c6c8f8bee5a3af9c2b4d1"
	if got != want {
		t.Errorf("sha256File() = %q, want %q", got, want)
	}
}

func TestSHA256File_MissingFileReturnsError(t *testing.T) {
	_, err := sha256File(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("sha256File() error = nil, want error for missing file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run 'TestWriteManifest|TestSHA256File' -v`
Expected: FAIL — `undefined: Manifest`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backup/manifest.go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run 'TestWriteManifest|TestSHA256File' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup/manifest.go internal/backup/manifest_test.go
git commit -m "backup: add Manifest type, JSON writer, and SHA-256 checksum"
```

---

### Task 4: Archive creation (tar + zstd, gzip fallback)

**Files:**
- Create: `internal/backup/archive.go`
- Test: `internal/backup/archive_test.go` (package `backup` — whitebox; needs to override the unexported `lookZstd` var to force the fallback path deterministically)

**Interfaces:**
- Produces: `createArchive(srcDir, destPath, requested string) (usedCompression string, err error)` — tars `srcDir`'s contents and compresses to `destPath`. `requested` is `"zstd"`, `"gzip"`, or `""` (prefer zstd, fall back to gzip if the `zstd` binary isn't on `$PATH`); an unrecognized value is an error. Returns `"zstd"` or `"gzip"` — whichever was actually used. Used by Task 5's `Run`.
- Internal seam: `var lookZstd = func() (string, error) { return exec.LookPath("zstd") }` — package-level so tests can override it without depending on whether zstd happens to be installed on the machine running `go test`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/backup/archive_test.go
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateArchive_GzipWhenZstdUnavailable(t *testing.T) {
	restore := lookZstd
	lookZstd = func() (string, error) { return "", errors.New("not found") }
	defer func() { lookZstd = restore }()

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "gzip" {
		t.Errorf("createArchive() compression = %q, want %q", used, "gzip")
	}
	assertTarGzContains(t, destPath, "file.txt", "hello")
}

func TestCreateArchive_ExplicitGzipIgnoresZstdAvailability(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "gzip")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "gzip" {
		t.Errorf("createArchive() compression = %q, want %q", used, "gzip")
	}
}

func TestCreateArchive_UsesZstdWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed, skipping")
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	destPath := filepath.Join(t.TempDir(), "archive.out")

	used, err := createArchive(srcDir, destPath, "zstd")
	if err != nil {
		t.Fatalf("createArchive() error = %v, want nil", err)
	}
	if used != "zstd" {
		t.Errorf("createArchive() compression = %q, want %q", used, "zstd")
	}

	decompressed, err := exec.Command("zstd", "-d", "-c", destPath).Output()
	if err != nil {
		t.Fatalf("zstd -d error = %v", err)
	}
	assertTarContains(t, bytes.NewReader(decompressed), "file.txt", "hello")
}

func TestCreateArchive_UnknownCompressionReturnsError(t *testing.T) {
	srcDir := t.TempDir()
	destPath := filepath.Join(t.TempDir(), "archive.out")

	_, err := createArchive(srcDir, destPath, "bogus")
	if err == nil {
		t.Fatal("createArchive() error = nil, want error for unknown compression")
	}
}

func assertTarGzContains(t *testing.T, path, wantName, wantContent string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gz.Close() }()

	assertTarContains(t, gz, wantName, wantContent)
}

func assertTarContains(t *testing.T, r io.Reader, wantName, wantContent string) {
	t.Helper()
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("tar entry %q not found (reader error: %v)", wantName, err)
		}
		if hdr.Name != wantName {
			continue
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatalf("read tar entry %q: %v", wantName, err)
		}
		if buf.String() != wantContent {
			t.Errorf("tar entry %q content = %q, want %q", wantName, buf.String(), wantContent)
		}
		return
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestCreateArchive -v`
Expected: FAIL — `undefined: createArchive`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backup/archive.go
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// lookZstd is overridable in tests to force the gzip fallback path without
// depending on whether zstd happens to be installed on the machine running
// the tests.
var lookZstd = func() (string, error) { return exec.LookPath("zstd") }

// createArchive tars srcDir's contents and compresses the result to
// destPath. requested is "zstd", "gzip", or "" (prefer zstd, fall back to
// gzip if the zstd binary isn't on PATH). Returns which compression was
// actually used.
func createArchive(srcDir, destPath, requested string) (string, error) {
	useZstd := false
	switch requested {
	case "gzip":
		useZstd = false
	case "zstd", "":
		_, err := lookZstd()
		useZstd = err == nil
	default:
		return "", fmt.Errorf("unknown compression %q", requested)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	if useZstd {
		if err := tarToZstd(srcDir, out); err != nil {
			_ = out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("close archive: %w", err)
		}
		return "zstd", nil
	}

	gz := gzip.NewWriter(out)
	if err := tarTo(srcDir, gz); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("close gzip writer: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}
	return "gzip", nil
}

// tarToZstd streams a tar of srcDir through the external zstd binary,
// writing the compressed result to out.
func tarToZstd(srcDir string, out io.Writer) error {
	cmd := exec.Command("zstd", "-q")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("zstd stdin pipe: %w", err)
	}
	cmd.Stdout = out

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	tarErr := tarTo(srcDir, stdin)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if tarErr != nil {
		return tarErr
	}
	if closeErr != nil {
		return fmt.Errorf("close zstd stdin: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("zstd: %w", waitErr)
	}
	return nil
}

// tarTo writes a tar stream of srcDir's contents to w. The root directory
// itself is not included as an entry, only its contents (with paths
// relative to srcDir) — the caller controls wrapping by choosing what
// srcDir points at.
func tarTo(srcDir string, w io.Writer) error {
	tw := tar.NewWriter(w)

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})

	closeErr := tw.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -run TestCreateArchive -v`
Expected: PASS (`TestCreateArchive_UsesZstdWhenAvailable` SKIPs if `zstd` isn't on PATH, otherwise PASSes)

- [ ] **Step 5: Commit**

```bash
git add internal/backup/archive.go internal/backup/archive_test.go
git commit -m "backup: tar+compress with zstd, gzip fallback when zstd is unavailable"
```

---

### Task 5: Choreography orchestration (`Run`)

**Files:**
- Create: `internal/backup/choreography.go`
- Test: `internal/backup/choreography_test.go` (package `backup_test` — blackbox, exercises only the exported API against `vm.FakeVMController`)

**Interfaces:**
- Consumes:
  - `vm.Controller` interface, `vm.ToolsState` + constants (`vm.ToolsRunning`, `vm.ToolsNotInstalled`, etc.) — `github.com/xortim/snapback/internal/vm`
  - `readGuestOS(vmxPath string) (string, error)` — Task 1
  - `copyDir(src, dst string) error` — Task 2
  - `Manifest`, `writeManifest(path string, m Manifest) error`, `sha256File(path string) (string, error)` — Task 3
  - `createArchive(srcDir, destPath, requested string) (string, error)` — Task 4
- Produces (this is the package's public surface, consumed by the future `snapback run` command, issue #7):
  ```go
  type Options struct {
      VMName      string
      VMXPath     string
      Comment     string
      Destination string
      StagingDir  string
      Compression string
      Now         func() time.Time
  }
  type Result struct {
      ArchiveID    string
      OutputDir    string
      ArchivePath  string
      ManifestPath string
      Manifest     Manifest
  }
  func Run(ctrl vm.Controller, opts Options) (*Result, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/backup/choreography_test.go
package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/vm"
)

var errBoom = errors.New("boom")

func TestRun_HappyPath_ProducesArchiveAndManifest(t *testing.T) {
	srcBundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(srcBundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	vmxPath := filepath.Join(srcBundle, "myvm.vmx")
	if err := os.WriteFile(vmxPath, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcBundle, "disk.vmdk"), []byte("fake disk contents"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	fixedNow := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stagingDir := t.TempDir()
	opts := backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Comment:     "test backup",
		Destination: t.TempDir(),
		StagingDir:  stagingDir,
		Compression: "gzip",
		Now:         func() time.Time { return fixedNow },
	}

	result, err := backup.Run(fake, opts)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if result.Manifest.VMName != "myvm" {
		t.Errorf("Manifest.VMName = %q, want %q", result.Manifest.VMName, "myvm")
	}
	if result.Manifest.GuestOS != "ubuntu-64" {
		t.Errorf("Manifest.GuestOS = %q, want %q", result.Manifest.GuestOS, "ubuntu-64")
	}
	if result.Manifest.Comment != "test backup" {
		t.Errorf("Manifest.Comment = %q, want %q", result.Manifest.Comment, "test backup")
	}
	if result.Manifest.ToolsState != vm.ToolsRunning {
		t.Errorf("Manifest.ToolsState = %q, want %q", result.Manifest.ToolsState, vm.ToolsRunning)
	}
	if result.Manifest.Compression != "gzip" {
		t.Errorf("Manifest.Compression = %q, want %q", result.Manifest.Compression, "gzip")
	}
	if result.Manifest.SHA256 == "" {
		t.Error("Manifest.SHA256 is empty, want a checksum")
	}
	if result.Manifest.SizeBytes == 0 {
		t.Error("Manifest.SizeBytes = 0, want > 0")
	}
	if !result.Manifest.Timestamp.Equal(fixedNow) {
		t.Errorf("Manifest.Timestamp = %v, want %v", result.Manifest.Timestamp, fixedNow)
	}

	if filepath.Ext(result.ArchivePath) != ".gz" {
		t.Errorf("ArchivePath = %q, want a .tar.gz path", result.ArchivePath)
	}
	if _, err := os.Stat(result.ArchivePath); err != nil {
		t.Errorf("archive file missing: %v", err)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Errorf("manifest file missing: %v", err)
	}

	// Archive should extract back to the original bundle layout:
	// myvm.vmwarevm/{myvm.vmx,disk.vmdk}
	f, err := os.Open(result.ArchivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	found := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		found[hdr.Name] = buf.String()
	}
	if found["myvm.vmwarevm/myvm.vmx"] != "guestOS = \"ubuntu-64\"\n" {
		t.Errorf("archive entry myvm.vmwarevm/myvm.vmx = %q, missing or wrong", found["myvm.vmwarevm/myvm.vmx"])
	}
	if found["myvm.vmwarevm/disk.vmdk"] != "fake disk contents" {
		t.Errorf("archive entry myvm.vmwarevm/disk.vmdk = %q, missing or wrong", found["myvm.vmwarevm/disk.vmdk"])
	}

	snapshots, err := fake.ListSnapshots(vmxPath)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty after a successful run (snapshot merged back)", snapshots)
	}

	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("ReadDir(StagingDir) error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("StagingDir contains %v, want empty after a successful run", entries)
	}
}

func TestRun_NonRunningToolsState_RecordsCrashConsistent(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsNotInstalled

	result, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if result.Manifest.ToolsState != vm.ToolsNotInstalled {
		t.Errorf("Manifest.ToolsState = %q, want %q (crash-consistent, recorded not skipped)", result.Manifest.ToolsState, vm.ToolsNotInstalled)
	}
}

func TestRun_CheckToolsStateError_AbortsBeforeSnapshot(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsStateErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	snapshots, _ := fake.ListSnapshots(vmxPath)
	if len(snapshots) != 0 {
		t.Errorf("ListSnapshots() = %v, want empty; no snapshot should have been taken", snapshots)
	}
}

func TestRun_SnapshotError_Aborts(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.SnapshotErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestRun_ListSnapshotsError_Aborts(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.ListSnapshotsErr = errBoom

	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestRun_DeleteSnapshotError_StillCleansUpStaging(t *testing.T) {
	vmxPath := writeMinimalVMX(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	stagingDir := t.TempDir()
	_, err := backup.Run(fake, backup.Options{
		VMName:      "myvm",
		VMXPath:     vmxPath,
		Destination: t.TempDir(),
		StagingDir:  stagingDir,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}

	entries, rdErr := os.ReadDir(stagingDir)
	if rdErr != nil {
		t.Fatalf("ReadDir(stagingDir) error = %v", rdErr)
	}
	if len(entries) != 0 {
		t.Errorf("stagingDir contains %v, want empty even after a failed run", entries)
	}
}

func writeMinimalVMX(t *testing.T) string {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "myvm.vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	path := filepath.Join(bundle, "myvm.vmx")
	if err := os.WriteFile(path, []byte("guestOS = \"ubuntu-64\"\n"), 0o644); err != nil {
		t.Fatalf("write vmx: %v", err)
	}
	return path
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backup/... -run TestRun_ -v`
Expected: FAIL — `undefined: backup.Run` / `undefined: backup.Options`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backup/choreography.go
package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/xortim/snapback/internal/vm"
)

// Options configures a single backup run.
type Options struct {
	VMName      string
	VMXPath     string
	Comment     string
	Destination string           // parent directory the backup's output directory is created under
	StagingDir  string           // parent directory for the temporary staging copy; os.TempDir() if empty
	Compression string           // "zstd", "gzip", or "" (prefer zstd, fall back to gzip)
	Now         func() time.Time // defaults to time.Now if nil
}

// Result describes a completed backup.
type Result struct {
	ArchiveID    string
	OutputDir    string
	ArchivePath  string
	ManifestPath string
	Manifest     Manifest
}

// Run executes the snapshot -> copy -> merge -> archive choreography
// described in docs/design.md ("Backup choreography") against ctrl. The
// source VM is never paused: ctrl.Snapshot freezes the disk state, the
// bundle is copied while the VM keeps running against a fresh delta, and
// ctrl.DeleteSnapshot merges that delta back once the copy is safely
// staged.
//
// On error, no cleanup beyond removing the staging directory is
// attempted — a snapshot left behind by a failed run is intentionally
// recovered by the separate `snapback cleanup` command (docs/design.md),
// not by automatic rollback here.
func Run(ctrl vm.Controller, opts Options) (*Result, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	startTime := now().UTC()
	ts := startTime.Format("20060102T150405Z")
	archiveID := fmt.Sprintf("%s-%s", opts.VMName, ts)
	snapshotName := "snapback-" + ts

	toolsState, err := ctrl.CheckToolsState(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("check tools state: %w", err)
	}

	if err := ctrl.Snapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	snapshots, err := ctrl.ListSnapshots(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	if !slices.Contains(snapshots, snapshotName) {
		return nil, fmt.Errorf("snapshot %q not found after creation", snapshotName)
	}

	hostSync()

	guestOS, err := readGuestOS(opts.VMXPath)
	if err != nil {
		return nil, fmt.Errorf("read guest OS: %w", err)
	}

	stagingParent := opts.StagingDir
	if stagingParent == "" {
		stagingParent = os.TempDir()
	}
	stagingRoot := filepath.Join(stagingParent, "snapback-staging-"+archiveID)
	bundleDir := filepath.Dir(opts.VMXPath)
	stagedBundle := filepath.Join(stagingRoot, filepath.Base(bundleDir))
	if err := copyDir(bundleDir, stagedBundle); err != nil {
		return nil, fmt.Errorf("copy bundle: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingRoot) }()

	if err := ctrl.DeleteSnapshot(opts.VMXPath, snapshotName); err != nil {
		return nil, fmt.Errorf("delete snapshot: %w", err)
	}

	outputDir := filepath.Join(opts.Destination, archiveID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	tempArchivePath := filepath.Join(outputDir, "archive.tmp")
	usedCompression, err := createArchive(stagingRoot, tempArchivePath, opts.Compression)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	ext := "tar.gz"
	if usedCompression == "zstd" {
		ext = "tar.zst"
	}
	archivePath := filepath.Join(outputDir, "archive."+ext)
	if err := os.Rename(tempArchivePath, archivePath); err != nil {
		return nil, fmt.Errorf("rename archive: %w", err)
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}

	sum, err := sha256File(archivePath)
	if err != nil {
		return nil, err
	}

	manifest := Manifest{
		VMName:      opts.VMName,
		GuestOS:     guestOS,
		SizeBytes:   info.Size(),
		Comment:     opts.Comment,
		Timestamp:   startTime,
		ToolsState:  toolsState,
		SHA256:      sum,
		Compression: usedCompression,
	}
	manifestPath := filepath.Join(outputDir, "manifest.json")
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, err
	}

	return &Result{
		ArchiveID:    archiveID,
		OutputDir:    outputDir,
		ArchivePath:  archivePath,
		ManifestPath: manifestPath,
		Manifest:     manifest,
	}, nil
}

// hostSync flushes the host page cache before copy.go reads the frozen
// disk files. Best-effort only: the snapshot already guarantees the
// source files are frozen at the VMware layer, so a failure here is not
// fatal — it just removes a class of doubt per docs/design.md.
func hostSync() {
	_ = exec.Command("sync").Run()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backup/... -v`
Expected: PASS — every test in the package (Tasks 1–5), `TestCreateArchive_UsesZstdWhenAvailable` PASSes if `zstd` is on `PATH`, SKIPs otherwise.

- [ ] **Step 5: Full-repo verification and commit**

Run:
```bash
make lint
make test
make build
```
Expected: all three succeed (0 lint issues, all tests pass including pre-existing `internal/cli`, `internal/config`, `internal/vm` suites, binary builds to `dist/<goos-goarch>/snapback`).

```bash
git add internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "backup: implement Run() choreography orchestration

Closes #5"
```
