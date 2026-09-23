# Restore Decompression Cap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `snapback restore` from silently filling the destination disk when an archive's decompressed content vastly exceeds any expected size, by aborting extraction early with a clear error (closes issue #77).

**Architecture:** `internal/backup/choreography.go`'s `Run` already measures the VM bundle's real uncompressed size (`totalBytes`, via `dirSize`) before archiving it, but never records it. Add `Manifest.UncompressedSizeBytes` to persist that ground truth. On restore, `extractArchive` (`internal/backup/extract.go`) uses it (plus a small fixed slack, to cover the `*.lck` directories `createArchive` excludes from the tar but `dirSize` counted) as a hard cap on total bytes written during extraction, checked incrementally so a single oversized tar entry is caught mid-copy rather than only after fully (and wastefully) writing it. Archives restored from a manifest written before this field existed (`UncompressedSizeBytes == 0`) fall back to a generous multiplier against the *compressed* archive's on-disk size, since no ground truth is available for them.

Ground truth (measured uncompressed size) is preferred over a compression-ratio guess because VM disk images have large, legitimately sparse/zero-filled regions that can compress at very high ratios (thousands:1) — a ratio-based cap tight enough to catch a real bomb would risk false-positively rejecting a real backup.

**Tech Stack:** Go 1.26.5, standard library (`archive/tar`, `compress/gzip`, `io`), existing zstd-via-subprocess helper in `extract.go`.

**Spec:** GitHub issue #77 (`restore: cap decompression size to guard against archive bombs`) — no separate ADR; this plan is scoped directly from the issue body, expanded with the ground-truth-vs-multiplier design decision above (this document is the design record for that decision).

## Global Constraints

- Go 1.26.5, module `github.com/xortim/snapback`.
- Follow existing code conventions in `internal/backup`: unexported helpers, doc comments explain *why* not *what*, errors wrapped with `fmt.Errorf("%w", ...)`, tests are table-free plain functions matching the existing style in `extract_test.go` / `choreography_test.go`.
- Run `make lint` and `make test` before each commit (per CLAUDE.md / repo convention) — do not use ad hoc `go vet`/`go build` substitutes.
- Commit messages: conventional-commit style, package name as scope (e.g. `fix(backup): ...`), ending with the `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>` trailer.
- No placeholders, no speculative abstractions — this is a bounded bug fix, not a redesign of the archive format.

---

## File Structure

- **Modify `internal/backup/manifest.go`**: add `UncompressedSizeBytes int64` field to `Manifest`.
- **Modify `internal/backup/choreography.go`**: populate the new field from the already-computed `totalBytes`.
- **Modify `internal/backup/extract.go`**: add the cap constants, thread an `expectedUncompressedBytes int64` parameter through `extractArchive` → `untarFromZstd` / `untarFrom`, add the capped-copy helper, enforce it in the `TypeReg` case.
- **Modify `internal/backup/restore.go`**: pass `archive.Manifest.UncompressedSizeBytes` into the existing `extractArchive` call.
- **Modify `internal/backup/extract_test.go`**: update all existing `extractArchive` call sites for the new parameter (pass `0` — falls back to the multiplier path, which is generous enough not to affect any existing round-trip test); add new tests for the cap itself.
- **Modify `internal/backup/choreography_test.go`**: extend the existing manifest field-by-field assertions (`TestRun_...` around line 74 and the on-disk comparison around line 110) to also check `UncompressedSizeBytes`.

No new files — this is a small, contained change to two already-small functions plus a manifest field.

---

## Task 1: Add `Manifest.UncompressedSizeBytes` and populate it in `Run`

**Files:**
- Modify: `internal/backup/manifest.go:19-27` (the `Manifest` struct)
- Modify: `internal/backup/choreography.go:412-420` (the `Manifest{...}` literal in `Run`)
- Modify: `internal/backup/choreography_test.go` (extend `TestRun_...`'s manifest assertions — see Step 3 below; find the exact test function name by looking at the assertions shown around lines 59-118 in the current file)

**Interfaces:**
- Produces: `Manifest.UncompressedSizeBytes int64` (json tag `uncompressed_size_bytes`) — Task 3 (`extract.go`) and Task 4 (`restore.go`) read this field via `archive.Manifest.UncompressedSizeBytes`.

- [ ] **Step 1: Add the field to `Manifest`**

In `internal/backup/manifest.go`, change:

```go
type Manifest struct {
	VMName      string        `json:"vm_name"`
	GuestOS     string        `json:"guest_os"`
	SizeBytes   int64         `json:"size_bytes"`
	Timestamp   time.Time     `json:"timestamp"`
	ToolsState  vm.ToolsState `json:"tools_state"`
	SHA256      string        `json:"sha256"`
	Compression string        `json:"compression"`
}
```

to:

```go
type Manifest struct {
	VMName      string        `json:"vm_name"`
	GuestOS     string        `json:"guest_os"`
	SizeBytes   int64         `json:"size_bytes"`
	Timestamp   time.Time     `json:"timestamp"`
	ToolsState  vm.ToolsState `json:"tools_state"`
	SHA256      string        `json:"sha256"`
	Compression string        `json:"compression"`
	// UncompressedSizeBytes is the real, measured size of the .vmwarevm
	// bundle at archive time (Run's totalBytes, from dirSize) -- ground
	// truth for how large extraction should be, used by restore's
	// decompression cap (extract.go) instead of guessing from the
	// compressed archive size and a ratio, since VM disks can legitimately
	// compress at very high ratios. Zero on a manifest written before this
	// field existed; extractArchive falls back to a multiplier-based cap
	// in that case.
	UncompressedSizeBytes int64 `json:"uncompressed_size_bytes"`
}
```

- [ ] **Step 2: Populate it in `Run`**

In `internal/backup/choreography.go`, change the `manifest := Manifest{...}` literal (around line 412):

```go
	manifest := Manifest{
		VMName:      opts.VMName,
		GuestOS:     guestOS,
		SizeBytes:   info.Size(),
		Timestamp:   startTime,
		ToolsState:  toolsState,
		SHA256:      sum,
		Compression: usedCompression,
	}
```

to:

```go
	manifest := Manifest{
		VMName:                opts.VMName,
		GuestOS:               guestOS,
		SizeBytes:             info.Size(),
		Timestamp:             startTime,
		ToolsState:            toolsState,
		SHA256:                sum,
		Compression:           usedCompression,
		UncompressedSizeBytes: totalBytes,
	}
```

(`totalBytes` is already in scope here — it's computed once near the top of `Run`, at `internal/backup/choreography.go:197`, via `dirSize(bundleDir)`, and used for the `Copying`/`Compressing` stage progress percentages.)

- [ ] **Step 3: Extend the existing manifest-round-trip test to cover the new field**

In `internal/backup/choreography_test.go`, find the test that builds `result, err := backup.Run(...)` and then asserts individual `result.Manifest.*` fields (around the block shown at lines 59-76), and add:

```go
	if result.Manifest.UncompressedSizeBytes == 0 {
		t.Error("Manifest.UncompressedSizeBytes = 0, want > 0")
	}
```

right after the existing `SizeBytes` check. Then find the on-disk-vs-result manifest comparison further down (the `if onDiskManifest.VMName != result.Manifest.VMName || ...` block, around lines 110-118) and add `onDiskManifest.UncompressedSizeBytes != result.Manifest.UncompressedSizeBytes ||` as one more disjunct in that condition, so a manifest.json round-trip mismatch on this field would also fail the test.

- [ ] **Step 4: Run the affected tests**

Run: `go test ./internal/backup/... -run TestRun -v`
Expected: PASS, including the two new assertions.

- [ ] **Step 5: Commit**

```bash
git add internal/backup/manifest.go internal/backup/choreography.go internal/backup/choreography_test.go
git commit -m "$(cat <<'EOF'
feat(backup): record real uncompressed bundle size in the manifest

Run already measures the .vmwarevm bundle's true size via dirSize
(totalBytes) for progress reporting, but never persisted it. Restore's
upcoming decompression cap (#77) needs this ground truth to bound
extraction without false-tripping on VM disks that legitimately
compress at very high ratios.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add the capped-copy helper and cap constants to `extract.go`

**Files:**
- Modify: `internal/backup/extract.go` (add constants near the top, add a new unexported function)
- Test: `internal/backup/extract_test.go` (new white-box test calling the new helper directly — this file is `package backup`, not `backup_test`, so unexported functions are directly callable)

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `copyCapped(dst io.Writer, src io.Reader, remaining int64) (written int64, limitHit bool, err error)` — Task 3 wires this into `untarFrom`'s `TypeReg` case. Also produces the two constants `fallbackDecompressionMultiplier` and `decompressionSlackBytes`, consumed by Task 3.

- [ ] **Step 1: Write the failing test for `copyCapped`**

Add to `internal/backup/extract_test.go`:

```go
func TestCopyCapped_UnderBudget_CopiesEverythingNoLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 1024)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if limitHit {
		t.Error("copyCapped() limitHit = true, want false (well under budget)")
	}
	if written != 11 || dst.String() != "hello world" {
		t.Errorf("copyCapped() = (%d, %q), want (11, %q)", written, dst.String(), "hello world")
	}
}

func TestCopyCapped_ExceedsBudget_ReportsLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello world")) // 11 bytes
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 5)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if !limitHit {
		t.Error("copyCapped() limitHit = false, want true (source exceeds budget)")
	}
	if written != 6 {
		t.Errorf("copyCapped() written = %d, want 6 (budget + 1 byte to detect overflow)", written)
	}
}

func TestCopyCapped_ExactlyAtBudget_NoLimitHit(t *testing.T) {
	src := bytes.NewReader([]byte("hello")) // exactly 5 bytes
	var dst bytes.Buffer
	written, limitHit, err := copyCapped(&dst, src, 5)
	if err != nil {
		t.Fatalf("copyCapped() error = %v, want nil", err)
	}
	if limitHit {
		t.Error("copyCapped() limitHit = true, want false (source exactly fills budget, no more)")
	}
	if written != 5 || dst.String() != "hello" {
		t.Errorf("copyCapped() = (%d, %q), want (5, %q)", written, dst.String(), "hello")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/backup/... -run TestCopyCapped -v`
Expected: FAIL with `undefined: copyCapped`

- [ ] **Step 3: Implement `copyCapped` and the cap constants**

In `internal/backup/extract.go`, add near the top (after the `import` block):

```go
const (
	// fallbackDecompressionMultiplier bounds total decompressed bytes as a
	// multiple of the compressed archive's on-disk size, used only when
	// the archive's manifest predates Manifest.UncompressedSizeBytes (an
	// archive created before that field existed) and so carries no ground
	// truth to check against. Deliberately generous: a VM disk with large
	// zero-filled regions can legitimately compress at very high ratios,
	// and this is a last-resort guard against a truly pathological
	// expansion, not a tight bound.
	fallbackDecompressionMultiplier = 500

	// decompressionSlackBytes is added on top of a manifest's recorded
	// UncompressedSizeBytes to get the real cap. It's not a
	// compression-ratio guess -- it accounts for dirSize (measured at
	// backup time, before createArchive runs) including the Fusion
	// "*.lck" lock directories that createArchive then excludes from the
	// tar (see the 2026-09-11 incident note in CLAUDE.md), plus tar's
	// per-entry 512-byte header rounding. A real .vmwarevm bundle has a
	// handful of files, not thousands, so this is generous relative to
	// that overhead.
	decompressionSlackBytes = 64 * 1024
)

// copyCapped copies from src to dst, stopping once remaining bytes have
// been written, and reports whether src had more data beyond that budget
// rather than silently truncating. It copies remaining+1 bytes in a single
// pass: if src is exhausted at or before remaining bytes, io.CopyN returns
// io.EOF and copyCapped reports limitHit=false; if the full remaining+1
// bytes copy without hitting src's EOF, there was more data than the
// budget allowed, and copyCapped reports limitHit=true (having written one
// byte past the budget, immediately followed by the caller aborting the
// whole extraction -- one byte of overrun is an acceptable cost for
// detecting the overrun without a second read pass).
func copyCapped(dst io.Writer, src io.Reader, remaining int64) (written int64, limitHit bool, err error) {
	n, cerr := io.CopyN(dst, src, remaining+1)
	switch cerr {
	case nil:
		return n, true, nil
	case io.EOF:
		return n, false, nil
	default:
		return n, false, cerr
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/backup/... -run TestCopyCapped -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup/extract.go internal/backup/extract_test.go
git commit -m "$(cat <<'EOF'
feat(backup): add capped-copy helper for bounded tar extraction

Standalone unit first: copyCapped copies up to a byte budget and
reports whether the source had more data beyond it, without needing a
second read pass. Not yet wired into extraction -- next commit does
that.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Enforce the cap in `extractArchive` / `untarFrom` / `untarFromZstd`

**Files:**
- Modify: `internal/backup/extract.go` (signatures of `extractArchive`, `untarFromZstd`, `untarFrom`; the `TypeReg` case body)
- Modify: `internal/backup/extract_test.go` (update all existing `extractArchive(...)` call sites for the new parameter; add new bomb/fallback/ground-truth tests)

**Interfaces:**
- Consumes: `copyCapped` and the two constants from Task 2.
- Produces: `extractArchive(srcPath, destDir, compression string, expectedUncompressedBytes int64, onWrite func(cumulativeBytes int64)) error` — Task 4 (`restore.go`) calls this with the new fourth parameter.

- [ ] **Step 1: Write the failing tests**

First, update every existing call to `extractArchive(...)` in `internal/backup/extract_test.go` to insert `0` as the new fourth argument (before `onWrite`). There are 11 call sites; for example:

```go
if err := extractArchive(archivePath, destDir, "gzip", func(cumulative int64) { calls = append(calls, cumulative) }); err != nil {
```

becomes:

```go
if err := extractArchive(archivePath, destDir, "gzip", 0, func(cumulative int64) { calls = append(calls, cumulative) }); err != nil {
```

and a call with a `nil` `onWrite`, e.g.:

```go
if err := extractArchive(archivePath, destDir, "zstd", nil); err != nil {
```

becomes:

```go
if err := extractArchive(archivePath, destDir, "zstd", 0, nil); err != nil {
```

Apply this mechanically to all 11 sites in the file (every `extractArchive(` call). Passing `0` means every existing test exercises the fallback (multiplier-against-compressed-size) path, which is intentional -- those tests use small, non-adversarial fixtures, so the generous fallback multiplier never trips.

Then add these new tests to the end of the file:

```go
func TestExtractArchive_DecompressionExceedsExpectedSize_ReturnsError(t *testing.T) {
	// Simulate the exact bomb scenario: a checksum-valid archive whose
	// actual decompressed content is far larger than what the manifest
	// (or here, the expectedUncompressedBytes the caller passes straight
	// through from Manifest.UncompressedSizeBytes) says it should be.
	srcDir := t.TempDir()
	// 256KB of a repeating byte -- highly compressible, so the archive on
	// disk stays tiny, but large relative to the tiny expected size below.
	payload := bytes.Repeat([]byte{0x42}, 256*1024)
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	// expectedUncompressedBytes=100 -- far below the 256KB payload, well
	// beyond decompressionSlackBytes (64KB) tolerance too.
	err := extractArchive(archivePath, destDir, "gzip", 100, nil)
	if err == nil {
		t.Fatal("extractArchive() error = nil, want an error for exceeding expected uncompressed size")
	}
	if _, statErr := os.Stat(destDir); statErr == nil {
		entries, _ := os.ReadDir(destDir)
		var total int64
		for _, e := range entries {
			info, _ := e.Info()
			if info != nil {
				total += info.Size()
			}
		}
	}
}

func TestExtractArchive_LegitimateHighCompressionRatio_NotFalselyCapped(t *testing.T) {
	// A large all-zero file compresses extremely well (a VM disk's sparse
	// regions do too) -- when expectedUncompressedBytes reflects the real
	// size (as Manifest.UncompressedSizeBytes would), this must NOT be
	// mistaken for a bomb despite the huge compression ratio.
	srcDir := t.TempDir()
	payload := make([]byte, 2*1024*1024) // 2MB of zeros
	if err := os.WriteFile(filepath.Join(srcDir, "sparse.vmdk"), payload, 0o644); err != nil {
		t.Fatalf("write sparse.vmdk: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	// The real uncompressed size (2MB of payload; tar adds negligible
	// header overhead well within decompressionSlackBytes).
	if err := extractArchive(archivePath, destDir, "gzip", int64(len(payload)), nil); err != nil {
		t.Fatalf("extractArchive() error = %v, want nil (legitimate high-ratio archive within expected size)", err)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "sparse.vmdk"))
	if err != nil || len(got) != len(payload) {
		t.Errorf("sparse.vmdk length = %d, err=%v, want %d, nil", len(got), err, len(payload))
	}
}

func TestExtractArchive_FallbackCap_NoExpectedSize_StillCatchesBomb(t *testing.T) {
	// expectedUncompressedBytes=0 simulates restoring an archive whose
	// manifest predates UncompressedSizeBytes -- extractArchive must fall
	// back to fallbackDecompressionMultiplier against the compressed
	// archive's own on-disk size, and that fallback cap must still be
	// enforced (not skipped just because there's no ground truth).
	srcDir := t.TempDir()
	// 1MB of a repeating byte compresses to well under a few KB; the
	// fallback cap (compressed size * 500) will then be far below 1MB.
	payload := bytes.Repeat([]byte{0x7A}, 1024*1024)
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	if _, err := createArchive(srcDir, archivePath, "gzip", nil); err != nil {
		t.Fatalf("createArchive: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size()*fallbackDecompressionMultiplier >= int64(len(payload)) {
		t.Fatalf("test fixture invalid: compressed size %d * %d = %d, want it below payload size %d (adjust payload size if this fires)",
			info.Size(), fallbackDecompressionMultiplier, info.Size()*fallbackDecompressionMultiplier, len(payload))
	}

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(archivePath, destDir, "gzip", 0, nil); err == nil {
		t.Fatal("extractArchive() error = nil, want an error from the fallback cap")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/backup/... -run TestExtractArchive -v`
Expected: compile error (wrong argument count to `extractArchive`) until Step 3 lands the signature change -- confirm by running now and seeing the compile failure, which is the "fails for the right reason" here since the new tests and updated call sites reference a signature that doesn't exist yet.

- [ ] **Step 3: Implement the cap enforcement**

In `internal/backup/extract.go`, change `extractArchive`'s signature and body:

```go
// extractArchive decompresses+untars srcPath (compressed as identified by
// compression, "zstd" or "gzip" -- Manifest.Compression, not re-sniffed)
// into destDir, which must not already exist. Mirrors createArchive's
// shape in reverse. expectedUncompressedBytes should be
// Manifest.UncompressedSizeBytes (0 for a manifest written before that
// field existed, in which case a generous multiplier against the
// compressed archive's own size is used instead) -- see the
// fallbackDecompressionMultiplier and decompressionSlackBytes doc
// comments for why. Either way, extraction aborts with an error rather
// than filling the destination disk if actual decompressed output
// exceeds the resulting cap. If onWrite is non-nil, it's invoked with
// the running cumulative bytes written across all files.
func extractArchive(srcPath, destDir, compression string, expectedUncompressedBytes int64, onWrite func(cumulativeBytes int64)) error {
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("extract archive: %s already exists (left by a previous failed restore attempt -- if you're not currently retrying that restore, it's safe to remove and try again)", destDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()

	maxBytes := expectedUncompressedBytes + decompressionSlackBytes
	if expectedUncompressedBytes <= 0 {
		info, err := in.Stat()
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}
		maxBytes = info.Size() * fallbackDecompressionMultiplier
	}

	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return untarFrom(gz, destDir, maxBytes, onWrite)
	case "zstd":
		return untarFromZstd(in, destDir, maxBytes, onWrite)
	default:
		return fmt.Errorf("unknown compression %q", compression)
	}
}
```

Then update `untarFromZstd`:

```go
// untarFromZstd streams in through the external zstd binary's decompressor
// and untars the result into destDir -- the reverse of tarToZstd
// (archive.go). Manifest.Compression already records that this archive
// was compressed with zstd, so there's no fallback-detection logic here:
// if zstd isn't on PATH, restoring this archive fails outright. maxBytes
// is the decompression cap, forwarded to untarFrom.
func untarFromZstd(in io.Reader, destDir string, maxBytes int64, onWrite func(cumulativeBytes int64)) error {
	if _, err := lookZstd(); err != nil {
		return fmt.Errorf("zstd not found on PATH (required to restore a zstd-compressed archive): %w", err)
	}
	cmd := exec.Command("zstd", "-d", "-q")
	cmd.Stdin = in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("zstd stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd: %w", err)
	}

	untarErr := untarFrom(stdout, destDir, maxBytes, onWrite)
	// Drain any remaining stdout to unblock zstd if untarFrom exited early
	// (corrupt tar, path-traversal rejection, decompression cap hit)
	// before fully reading its output. This prevents a deadlock where
	// zstd blocks on a full pipe buffer.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if waitErr != nil {
		return fmt.Errorf("zstd: %w: %s", waitErr, stderr.String())
	}
	if untarErr != nil {
		return untarErr
	}
	return nil
}
```

Then update `untarFrom`'s signature and its `TypeReg` case:

```go
// untarFrom reads a tar stream from r and writes its entries under destDir,
// rejecting any entry whose resolved path would land outside destDir
// (rejecting ".."-escaping or absolute entry names) -- a cheap, standard
// guard against a corrupted or tampered archive writing outside the
// staging directory. Aborts once the running total of regular-file bytes
// written would exceed maxBytes, rather than letting a pathological
// archive fill the destination disk (see extractArchive's doc comment for
// how maxBytes is derived). If onWrite is non-nil, it's invoked as file
// bytes are written, with the running cumulative total across all files.
func untarFrom(r io.Reader, destDir string, maxBytes int64, onWrite func(cumulativeBytes int64)) error {
	tr := tar.NewReader(r)
	var cumulative int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		if !isWithinDir(destDir, target) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		// Older archives (created before createArchive started excluding
		// these) may still carry a Fusion "<file>.lck" directory copied
		// from the live source VM -- process-specific lock state that's
		// meaningless once restored elsewhere, and actively harmful:
		// vmware-vdiskmanager -e mistakes a restored, stale lock directory
		// for one another process already has the disk open, failing the
		// post-extraction disk consistency check over nothing (confirmed
		// real case, 2026-09-11). Drop any such entry on extraction too,
		// so an archive made before that fix still restores cleanly. This
		// runs after the traversal check above so a tampered entry can't
		// dodge it just by ending in ".lck".
		if hasLockDirComponent(hdr) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeSymlink:
			// Validate that symlink target doesn't escape destDir.
			// Reject absolute paths outright; for relative paths, resolve
			// against the symlink's directory and check it stays within destDir.
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("symlink %q has absolute target %q (escapes destination directory)", hdr.Name, hdr.Linkname)
			}
			resolvedTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
			if !isWithinDir(destDir, resolvedTarget) {
				return fmt.Errorf("symlink %q with target %q escapes destination directory", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			n, limitHit, copyErr := copyCapped(out, tr, maxBytes-cumulative)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
			cumulative += n
			if limitHit {
				return fmt.Errorf("archive decompressed past %d bytes while extracting %q -- aborting to avoid filling the destination disk (archive may be corrupt or the manifest's recorded size doesn't match its actual contents)", maxBytes, hdr.Name)
			}
			if onWrite != nil {
				onWrite(cumulative)
			}
		default:
			// Anything else (device, fifo, ...) isn't something
			// createArchive ever produces from a .vmwarevm bundle -- skip
			// rather than fail the whole restore over it.
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/backup/... -run TestExtractArchive -v`
Expected: PASS for all tests, including the three new ones. Also run the full package to catch anything else touching these functions:

Run: `go test ./internal/backup/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backup/extract.go internal/backup/extract_test.go
git commit -m "$(cat <<'EOF'
fix(backup): cap decompression size during restore extraction

extractArchive had no upper bound on total decompressed bytes -- a
checksum-valid archive with a highly compressible payload (or a
manifest whose recorded size doesn't match its actual contents) could
exhaust destination disk space mid-extraction, before the
post-extraction disk-consistency check ever ran.

untarFrom now aborts as soon as cumulative regular-file bytes written
would exceed a cap: Manifest.UncompressedSizeBytes (the real bundle
size measured at backup time) plus a small fixed slack, or -- for a
manifest written before that field existed -- a generous multiplier
against the compressed archive's own on-disk size. Ground truth is
preferred over a compression-ratio guess because VM disks with large
zero-filled regions can legitimately compress at very high ratios.

Closes #77.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire the real expected size through `restore.go`

**Files:**
- Modify: `internal/backup/restore.go:143-150` (the `extractArchive` call site)

**Interfaces:**
- Consumes: `extractArchive(srcPath, destDir, compression string, expectedUncompressedBytes int64, onWrite func(int64)) error` (Task 3).

- [ ] **Step 1: Update the call site**

In `internal/backup/restore.go`, change:

```go
	reporter.Report(progress.Event{Stage: progress.Extracting, Message: "extracting archive"})
	// archive.Manifest.SizeBytes is the compressed size, an approximation
	// for extraction's (uncompressed) total -- same clamped-at-1 tolerance
	// percentOf already documents for Run's own Compressing stage.
	onWrite := throttledPercentReporter(reporter, progress.Extracting, archive.Manifest.SizeBytes)
	if err := extractArchive(archivePath, stagingDir, archive.Manifest.Compression, onWrite); err != nil {
		return nil, &RunError{Stage: progress.Extracting, Err: err}
	}
```

to:

```go
	reporter.Report(progress.Event{Stage: progress.Extracting, Message: "extracting archive"})
	// archive.Manifest.SizeBytes is the compressed size, an approximation
	// for extraction's (uncompressed) total -- same clamped-at-1 tolerance
	// percentOf already documents for Run's own Compressing stage. The
	// decompression cap enforced inside extractArchive uses the more
	// precise archive.Manifest.UncompressedSizeBytes instead (falling back
	// to a multiplier against SizeBytes for an older manifest that
	// predates that field) -- see extractArchive's doc comment.
	onWrite := throttledPercentReporter(reporter, progress.Extracting, archive.Manifest.SizeBytes)
	if err := extractArchive(archivePath, stagingDir, archive.Manifest.Compression, archive.Manifest.UncompressedSizeBytes, onWrite); err != nil {
		return nil, &RunError{Stage: progress.Extracting, Err: err}
	}
```

- [ ] **Step 2: Run the restore test suite**

Run: `go test ./internal/backup/... -run TestRestore -v`
Expected: PASS. (`buildFixtureArchive`/`buildFixtureArchiveVMX` in `choreography_test.go` don't set `UncompressedSizeBytes` on the manifests they hand-write, so these exercise the fallback path -- fine, since their fixtures are tiny and non-adversarial.)

- [ ] **Step 3: Run the full test suite and lint**

Run: `make test`
Expected: PASS, no failures anywhere in the module.

Run: `make lint`
Expected: no findings.

- [ ] **Step 4: Commit**

```bash
git add internal/backup/restore.go
git commit -m "$(cat <<'EOF'
fix(backup): pass real uncompressed size into restore's extract call

Wires archive.Manifest.UncompressedSizeBytes through to
extractArchive's new decompression cap, completing #77 end-to-end for
archives created after this change (older archives still get the
fallback multiplier cap, since their manifests don't carry this
field).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** issue #77 asks for (1) tracking cumulative bytes during extraction (already existed via `cumulative`/`onWrite`, reused) and (2) aborting past some bound derived from the manifest or an absolute cap, with a clear error. Task 3 does both — bound derived from `Manifest.UncompressedSizeBytes` (ground truth) with a documented fallback multiplier when that's unavailable, and a descriptive error naming the offending entry.
- **False-positive risk called out in the issue's own reasoning** ("this isn't about tampering... but about any archive... with a highly compressible payload") is explicitly covered by `TestExtractArchive_LegitimateHighCompressionRatio_NotFalselyCapped` in Task 3, which is the test that validates the ground-truth design decision over a naive ratio-based one.
- **Type/signature consistency:** `extractArchive`'s new fourth parameter (`expectedUncompressedBytes int64`) is defined in Task 3 and consumed identically in Task 4; `copyCapped`'s signature from Task 2 (`(dst io.Writer, src io.Reader, remaining int64) (written int64, limitHit bool, err error)`) matches its call in Task 3's `untarFrom` exactly.
- **No placeholders:** every step has literal code, not descriptions of code.
