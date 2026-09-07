# init huh Wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `snapback init`'s hand-rolled `bufio`-based prompts (`internal/cli/init.go`'s `prompter` type) with a step-by-step `charmbracelet/huh` wizard: VM discovery/select (plus manual entry) → destination → compression → retention → per-VM schedule (nightly/weekly/custom cron/none) → a review screen showing the exact YAML before anything is written.

**Architecture:** A new `internal/tui/init.go` (plus sibling files) owns `RunInitWizard`, built from a sequence of small `huh.Form`s driven either by a real interactive bubbletea render (a real terminal) or huh's built-in "accessible" mode (plain sequential prompts over any `io.Reader`/`io.Writer` — used both for this package's own tests and for real non-terminal `snapback init` invocations, e.g. piped/redirected stdin, exactly mirroring how `internal/cli/run.go` already picks between `tui.RunInteractive` and the plain renderer via a TTY check). `internal/tui` never does filesystem I/O of its own — `internal/cli/init.go` still owns VM discovery (unchanged), config marshaling, and writing to disk, calling into the wizard only to collect answers, the same boundary `tui.RunInteractive`/`backup.Run` already keep.

**Tech Stack:** `github.com/charmbracelet/huh` (new), reusing already-present `github.com/charmbracelet/lipgloss`/`bubbletea` transitively via huh. Go 1.26.5, existing `cobra`-based CLI.

**Spec:** `docs/superpowers/specs/2026-08-23-cli-ux-design.md` (its `init` section: "Flow: discover VMs → select which to manage → destination path → compression choice → retention numbers → per-VM schedule (presets: nightly/weekly/custom cron) → review screen → write config.yaml")

## Global Constraints

- `internal/tui` must not import `internal/cli`, and must not touch the filesystem (no `config.Load`, no `os.ReadDir`, no `writeConfigFile`) — VM discovery stays in `internal/cli/vmdiscovery.go` exactly as it is today; `internal/cli/init.go` converts its `[]discoveredVM` results into `[]tui.VMCandidate` before calling the wizard.
- **Verified against huh v1.0.0's actual source** (not assumed): `Form.runAccessible` (`form.go:703`) iterates every group's every field unconditionally via `f.selector.Range` — it does **not** consult `Group.WithHide`/`WithHideFunc`. A conditionally-hidden group is skipped in the real interactive path but still prompted in accessible mode. Do not use `WithHideFunc` anywhere in this plan for that reason — the per-VM custom-cron question (Task 5/7) is always asked, gated only by its own `Validate` closure checking the already-answered schedule choice.
- **Verified against huh v1.0.0's actual source:** `internal/accessibility.PromptString` (and everything built on it — `PromptInt`, `PromptBool`) constructs a **fresh `bufio.Scanner` on every single call**, used for exactly one line before being discarded. A `bufio.Scanner` reads in chunks: on any reader whose one `Read()` call can return more than one line's worth of bytes at once (true of `strings.Reader`, and true of a redirected/piped stdin with several lines already sitting in the pipe buffer when reading starts), that discarded scanner's read-ahead silently drops every line beyond the first before the next field's brand-new scanner ever sees it. Every accessible-mode form this plan builds must wrap its input reader with `lineBufferedReader` (Task 2) — this is a correctness fix for real piped/non-tty `snapback init` invocations, not only a test convenience.
- `lineBufferedReader` must only ever wrap the reader passed to `huh.Form.WithInput` when `accessible == true`. In the real interactive (non-accessible) path, `WithInput` also sets `tea.WithInput` for the underlying bubbletea program (verified: `form.go`'s `WithInput` does both), so wrapping it there would throttle real terminal key-event reads to one byte at a time and must not happen.
- **Verified against huh v1.0.0's actual source:** `accessibility.PromptString` (the function every `Input` field's accessible mode runs through) calls the field's `Validate` closure on the *raw scanned line* — a blank line included — and only substitutes the field's pre-set default (`cmp.Or(strings.TrimSpace(input), defaultValue)`) *after* that closure has already accepted it. This is unlike `Select`/`Confirm`, whose own accessible-mode validators (`validInt`/`validBool` in `accessibility.go`) special-case a blank line themselves before ever calling a field's `Validate` closure. Concretely: a bare `Validate(validateWritableDestination)` on the destination `Input` would reject every blank "accept the default" line the wizard's own tests (and any real accessible-mode/piped invocation) rely on, instead of applying the default. Task 3's `acceptBlankInAccessibleMode` wrapper exists specifically to close this gap, and every `Input` field with a non-empty default (destination, the three retention counts) must be wrapped with it, gated on `accessible` so the real interactive path — where a genuinely emptied field has no such default-substitution step — still rejects blank input exactly as before.
- Run `make lint`, `make test`, and `make build` (not ad-hoc `go vet`/`go build`) before every commit meant to be a checkpoint.
- No cron-parsing dependency is added. `validateCronExpression` (Task 3) is a 5-field sanity check only — nothing in this codebase parses or executes `config.VM.Schedule` yet (see CLAUDE.md's "Other components" table: launchd wiring is unbuilt), so full cron-grammar validation is out of scope here.

---

## File Structure

- Create `internal/tui/init_reader.go` — `lineBufferedReader`, the one-byte-at-a-time reader wrapper that works around the accessible-mode scanner bug above.
- Create `internal/tui/init_reader_test.go`.
- Create `internal/tui/init_validate.go` — `validateWritableDestination`, `validateNonNegativeInt`, `validateCronExpression`, `acceptBlankInAccessibleMode` — pure functions, no huh dependency.
- Create `internal/tui/init_validate_test.go`.
- Modify `internal/config/expand.go` — export `expandTilde` as `ExpandTilde` (same body, just capitalized and given a doc comment usable outside the package) so `internal/tui` can resolve `~` the same way `config.Load` does, instead of duplicating the logic.
- Modify `internal/config/config.go` — update its two `expandTilde(...)` call sites to `ExpandTilde(...)`.
- Modify `internal/config/expand_internal_test.go` — update its calls to the renamed `ExpandTilde`.
- Create `internal/tui/init_schedule.go` — schedule preset constants/choices, `resolveSchedule`.
- Create `internal/tui/init_schedule_test.go`.
- Create `internal/tui/init.go` — `VMCandidate`, wizard defaults, `newForm`/`runForm` helpers, `selectVMs`, `addManualVMs`, `promptCoreSettings`, `promptSchedules`, `reviewAndConfirm`, `RunInitWizard`. Built up across Tasks 5–8.
- Create `internal/tui/init_test.go` — tests for all of the above, added incrementally across Tasks 5–8.
- Modify `internal/cli/init.go` — delete `prompter` and all of its `prompt*` methods, `promptVMs`, `promptManualVMs` (replaced by the wizard); keep `writeConfigFile`, `configFileExists`, `discoverVMsWithContext` unchanged; add wizard wiring to `initDeps`/`runInit`.
- Modify `internal/cli/init_internal_test.go` — remove tests now covered by `internal/tui/init_test.go` (prompt-content/reprompt/manual-entry scenarios); keep the tests that exercise `cli`-owned behavior (existing-config gating, discovery-error wrapping, write-error wrapping, discovery cancellation); add tests for the new wizard wiring.
- Modify `go.mod`, `go.sum` — add `github.com/charmbracelet/huh`.

---

### Task 1: Add the huh dependency

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:** None (dependency-only task).

- [ ] **Step 1: Fetch the dependency**

```bash
go get github.com/charmbracelet/huh@latest
```

Do **not** run `go mod tidy` yet — nothing imports `huh` until Task 2 onward; `tidy` would prune it right back out. Leave that to the final verification task.

- [ ] **Step 2: Verify the build still works with no code changes yet**

Run: `go build ./...`
Expected: succeeds with no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add huh dependency"
```

---

### Task 2: `lineBufferedReader` (accessible-mode scanner-reuse fix)

**Files:**
- Create: `internal/tui/init_reader.go`
- Test: `internal/tui/init_reader_test.go`

**Interfaces:**
- Produces (used by Task 5 onward's `newForm` helper): `type lineBufferedReader struct{ r io.Reader }`, `func (l lineBufferedReader) Read(p []byte) (int, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/init_reader_test.go`:

```go
package tui

import (
	"bufio"
	"strings"
	"testing"
)

// TestLineBufferedReader_PreservesLaterLinesAcrossSeparateScanners
// reproduces exactly what huh's accessible-mode prompts do internally
// (huh@v1.0.0/internal/accessibility/accessibility.go's PromptString):
// a fresh bufio.Scanner constructed per prompt, used for one Scan() call,
// then discarded. Without lineBufferedReader, the first scanner's first
// Read() on a strings.Reader returns the *entire* remaining string in one
// call (strings.Reader.Read has no per-call size limit of its own), so
// everything after the first line is buffered inside that discarded
// scanner and lost -- the next fresh scanner sees only EOF. This test
// fails without the fix and passes with it.
func TestLineBufferedReader_PreservesLaterLinesAcrossSeparateScanners(t *testing.T) {
	r := lineBufferedReader{r: strings.NewReader("first\nsecond\nthird\n")}

	for _, want := range []string{"first", "second", "third"} {
		scanner := bufio.NewScanner(r)
		if !scanner.Scan() {
			t.Fatalf("Scan() = false before reading %q, want more input", want)
		}
		if got := scanner.Text(); got != want {
			t.Errorf("Text() = %q, want %q", got, want)
		}
	}
}

func TestLineBufferedReader_ReadsAtMostOneByte(t *testing.T) {
	r := lineBufferedReader{r: strings.NewReader("abc")}
	buf := make([]byte, 8)

	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 1 {
		t.Errorf("Read() n = %d, want 1", n)
	}
	if buf[0] != 'a' {
		t.Errorf("Read() byte = %q, want %q", buf[0], 'a')
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run TestLineBufferedReader -v`
Expected: FAIL — `lineBufferedReader` doesn't exist yet (build failure).

- [ ] **Step 3: Write the implementation**

Create `internal/tui/init_reader.go`:

```go
package tui

import "io"

// lineBufferedReader wraps r so every Read call returns at most one
// byte. huh's accessible-mode prompts (charmbracelet/huh's internal
// accessibility package) each construct a fresh bufio.Scanner around
// whatever io.Reader the wizard is given, used for exactly one line
// before being discarded -- a bufio.Scanner reads in chunks, so on any
// reader whose single Read call can return more than one line at a time
// (a strings.Reader, or a pipe/redirected file with several lines
// already sitting in the OS buffer), that discarded scanner's read-ahead
// silently drops every line beyond the first before the next field's
// brand-new scanner ever sees it. Limiting each Read to one byte forces
// every such scanner to stop exactly at its own line's newline, leaving
// the underlying reader positioned correctly for the next prompt. Used
// for every accessible-mode form this package builds -- including real,
// non-terminal `snapback init` invocations (piped/redirected stdin), not
// only this package's own tests.
type lineBufferedReader struct {
	r io.Reader
}

func (l lineBufferedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return l.r.Read(p[:1])
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -run TestLineBufferedReader -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init_reader.go internal/tui/init_reader_test.go
git commit -m "fix(tui): work around huh accessible-mode scanner reuse across prompts"
```

---

### Task 3: Pure validators

**Files:**
- Modify: `internal/config/expand.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/expand_internal_test.go`
- Create: `internal/tui/init_validate.go`
- Test: `internal/tui/init_validate_test.go`

**Interfaces:**
- Consumes: `config.ExpandTilde` (this task exports it from `internal/config`).
- Produces (used by Task 6's `promptCoreSettings` and Task 7's `promptSchedules`): `func validateWritableDestination(path string) error`, `func validateNonNegativeInt(s string) error`, `func validateCronExpression(s string) error`, `func acceptBlankInAccessibleMode(accessible bool, validate func(string) error) func(string) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/init_validate_test.go`:

```go
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

func TestValidateCronExpression(t *testing.T) {
	if err := validateCronExpression("0 2 * * *"); err != nil {
		t.Errorf("validateCronExpression() error = %v, want nil for a valid 5-field expression", err)
	}
	if err := validateCronExpression("0 2 * *"); err == nil {
		t.Error("validateCronExpression() error = nil, want an error for only 4 fields")
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestValidateWritableDestination|TestValidateNonNegativeInt|TestValidateCronExpression|TestAcceptBlankInAccessibleMode' -v`
Expected: FAIL — none of these functions exist yet.

- [ ] **Step 3: Export `config.ExpandTilde`**

In `internal/config/expand.go`, rename `expandTilde` to `ExpandTilde` and give it a doc comment usable from outside the package:

```go
// ExpandTilde expands a leading "~" (the current user's home directory
// alone) or "~/..." prefix in path using os.UserHomeDir. Any other
// leading-tilde form (e.g. "~otheruser/...") is left untouched -- this
// package only resolves the current user's home, not arbitrary user
// lookups. Exported so internal/tui's init wizard can validate a
// proposed destination the same way Load resolves one already written
// to config.yaml, without duplicating this logic.
func ExpandTilde(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
```

In `internal/config/config.go`, update both call sites (`Load`'s destination and per-VM `vmx` expansion) from `expandTilde(...)` to `ExpandTilde(...)`.

In `internal/config/expand_internal_test.go`, update every `expandTilde(...)` call to `ExpandTilde(...)` (the test names themselves can stay as-is; only the function calls change).

Run: `go test ./internal/config/... -v`
Expected: PASS — a pure rename, no behavior change.

- [ ] **Step 4: Write `internal/tui`'s implementation**

Create `internal/tui/init_validate.go`:

```go
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xortim/snapback/internal/config"
)

// validateWritableDestination is the huh Validate hook for the
// destination Input field. It expands a leading "~" (via
// config.ExpandTilde, the same expansion config.Load applies to an
// already-written config.yaml) and checks that the nearest existing
// ancestor directory is writable -- it deliberately does not create path
// itself (via os.MkdirAll): init is only proposing a destination here,
// not committing to it, and the review screen (reviewAndConfirm) is
// where the user actually confirms the config before anything is
// written. internal/cli/init.go's own writeConfigFile is the thing that
// actually creates directories, once the user has confirmed everything.
func validateWritableDestination(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("destination must not be empty")
	}
	expanded, err := config.ExpandTilde(trimmed)
	if err != nil {
		return err
	}

	dir := expanded
	for {
		info, statErr := os.Stat(dir)
		if statErr == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			break
		}
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("check %s: %w", dir, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no existing ancestor directory found for %s", expanded)
		}
		dir = parent
	}

	probe, err := os.CreateTemp(dir, ".snapback-writetest-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// validateNonNegativeInt is the Validate hook for the three retention
// Input fields (keep_last/keep_daily/keep_weekly). huh's Input only ever
// binds a string (Value(*string)), so parsing to int happens after the
// form completes in promptCoreSettings; this only rejects what
// strconv.Atoi or a negative value would otherwise let through silently.
func validateNonNegativeInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("%q is not a whole number", s)
	}
	if n < 0 {
		return fmt.Errorf("%q must not be negative", s)
	}
	return nil
}

// validateCronExpression is a lightweight sanity check -- exactly 5
// space-separated fields -- not a full cron grammar validator. No
// cron-parsing dependency exists in this module (nothing parses or
// executes config.VM.Schedule yet; see CLAUDE.md's "Other components"
// table), so this only catches the most common typo (wrong field count)
// rather than validating minute/hour/day ranges.
func validateCronExpression(s string) error {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return fmt.Errorf("cron expression must have 5 space-separated fields (minute hour day month weekday), got %d", len(fields))
	}
	return nil
}

// acceptBlankInAccessibleMode wraps validate so a blank/whitespace-only
// answer passes immediately when accessible is true, deferring to
// whatever default huh's accessible-mode PromptString substitutes
// afterward (cmp.Or(strings.TrimSpace(input), defaultValue), in
// huh@v1.0.0/internal/accessibility/accessibility.go). This exists
// because, verified against that same source, PromptString calls a
// field's Validate closure on the *raw scanned line* -- before that
// default substitution happens -- unlike Select/Confirm's own internal
// accessible-mode validators, which special-case a blank line
// themselves. Without this wrapper, every "type nothing to accept the
// default" convention this wizard relies on (in both its own tests and
// real piped/non-tty invocations) would instead fail validation and
// force a reprompt.
//
// Only applied when accessible is true: in the real interactive terminal
// path there is no equivalent default-substitution step for a genuinely
// emptied Input field (the field starts pre-filled with the default
// text, so blank only happens if a user deliberately clears it), so
// blank must still fail validation there exactly as it did before this
// wrapper existed.
func acceptBlankInAccessibleMode(accessible bool, validate func(string) error) func(string) error {
	return func(s string) error {
		if accessible && strings.TrimSpace(s) == "" {
			return nil
		}
		return validate(s)
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 6: Commit**

```bash
git add internal/config/expand.go internal/config/config.go internal/config/expand_internal_test.go internal/tui/init_validate.go internal/tui/init_validate_test.go
git commit -m "feat(tui): add init wizard field validators"
```

---

### Task 4: Schedule presets

**Files:**
- Create: `internal/tui/init_schedule.go`
- Test: `internal/tui/init_schedule_test.go`

**Interfaces:**
- Produces (used by Task 7's `promptSchedules`): `const scheduleChoiceNone/scheduleChoiceNightly/scheduleChoiceWeekly/scheduleChoiceCustom = "..."`, `var scheduleChoices []string`, `func resolveSchedule(choice, custom string) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/init_schedule_test.go`:

```go
package tui

import "testing"

func TestResolveSchedule(t *testing.T) {
	tests := []struct {
		name   string
		choice string
		custom string
		want   string
	}{
		{"none", scheduleChoiceNone, "", ""},
		{"none ignores stray custom text", scheduleChoiceNone, "0 3 * * *", ""},
		{"nightly", scheduleChoiceNightly, "", cronNightly},
		{"weekly", scheduleChoiceWeekly, "", cronWeekly},
		{"custom", scheduleChoiceCustom, "0 3 * * 1", "0 3 * * 1"},
	}
	for _, tt := range tests {
		if got := resolveSchedule(tt.choice, tt.custom); got != tt.want {
			t.Errorf("%s: resolveSchedule(%q, %q) = %q, want %q", tt.name, tt.choice, tt.custom, got, tt.want)
		}
	}
}

func TestScheduleChoices_ListsAllFourPresetsInOrder(t *testing.T) {
	want := []string{scheduleChoiceNone, scheduleChoiceNightly, scheduleChoiceWeekly, scheduleChoiceCustom}
	if len(scheduleChoices) != len(want) {
		t.Fatalf("len(scheduleChoices) = %d, want %d", len(scheduleChoices), len(want))
	}
	for i, w := range want {
		if scheduleChoices[i] != w {
			t.Errorf("scheduleChoices[%d] = %q, want %q", i, scheduleChoices[i], w)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestResolveSchedule|TestScheduleChoices' -v`
Expected: FAIL — none of these identifiers exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/init_schedule.go`:

```go
package tui

const (
	scheduleChoiceNone    = "none"
	scheduleChoiceNightly = "nightly"
	scheduleChoiceWeekly  = "weekly"
	scheduleChoiceCustom  = "custom"

	cronNightly = "0 2 * * *"
	cronWeekly  = "0 2 * * 0"
)

// scheduleChoices lists the schedule presets offered per VM, in display
// order, per docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// "presets: nightly/weekly/custom cron". "none" is included because
// nothing consumes config.VM.Schedule yet (launchd wiring is unbuilt --
// see CLAUDE.md's "Other components" table) and the field is already
// `omitempty`, so leaving a VM unscheduled is an existing, legitimate
// state, not a gap this wizard needs to force a choice around.
var scheduleChoices = []string{scheduleChoiceNone, scheduleChoiceNightly, scheduleChoiceWeekly, scheduleChoiceCustom}

// resolveSchedule turns a schedule preset choice (one of scheduleChoices)
// plus whatever the always-asked custom-cron field held into the string
// stored on config.VM.Schedule. custom is ignored unless choice is
// scheduleChoiceCustom.
//
// The custom-cron field is asked unconditionally, on every VM, not only
// when choice == scheduleChoiceCustom -- huh's accessible-mode form
// runner does not consult Group.WithHideFunc (verified by reading
// huh@v1.0.0/form.go's runAccessible, which iterates every group via
// f.selector.Range with no hide check, unlike the real interactive
// path), so a conditionally-hidden group would still be prompted in
// accessible mode. Asking it unconditionally keeps both modes identical
// instead of silently diverging.
func resolveSchedule(choice, custom string) string {
	switch choice {
	case scheduleChoiceNightly:
		return cronNightly
	case scheduleChoiceWeekly:
		return cronWeekly
	case scheduleChoiceCustom:
		return custom
	default:
		return ""
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init_schedule.go internal/tui/init_schedule_test.go
git commit -m "feat(tui): add per-VM schedule presets"
```

---

### Task 5: VM selection (`selectVMs`, `addManualVMs`)

**Files:**
- Create: `internal/tui/init.go`
- Create: `internal/tui/init_test.go`

**Interfaces:**
- Consumes: `lineBufferedReader` (Task 2), `config.VM`, `config.ValidateVMs` (`internal/config`, unchanged).
- Produces (used by Task 6-8 and by `internal/cli/init.go` in Task 9): `type VMCandidate struct{ Name, VMX string }`, `func newForm(in io.Reader, out io.Writer, accessible bool, groups ...*huh.Group) *huh.Form`, `func runForm(ctx context.Context, f *huh.Form) error`, `func selectVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) ([]config.VM, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/init_test.go`:

```go
package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSelectVMs_AcceptsDefaultSelection_AllDiscovered(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// "0" confirms the MultiSelect's pre-selected-by-default candidate
	// without toggling anything; "n" declines manual entry.
	in := strings.NewReader("0\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "dev" || vms[0].VMX != "/vms/dev.vmwarevm/dev.vmx" {
		t.Errorf("selectVMs() = %+v, want the one discovered candidate", vms)
	}
}

func TestSelectVMs_CanAlsoAddManually(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// confirm default selection, opt into manual entry, name+vmx for a VM
	// discovery can't see (renamed after creation -- see
	// internal/cli/vmdiscovery.go's own doc comment on this), decline a
	// second manual VM.
	in := strings.NewReader("0\ny\nrenamed\n/vms/renamed.vmwarevm/renamed.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("selectVMs() = %+v, want 2 VMs", vms)
	}
	if vms[0].Name != "dev" || vms[1].Name != "renamed" || vms[1].VMX != "/vms/renamed.vmwarevm/renamed.vmx" {
		t.Errorf("selectVMs() = %+v, want dev then the manually-added renamed VM", vms)
	}
}

func TestSelectVMs_NoDiscoveredCandidates_PromptsManualEntryImmediately(t *testing.T) {
	// No MultiSelect group exists when there are no candidates, so the
	// first prompt is the manual-add confirm -- defaulted to true for
	// the first iteration when there was nothing to discover (see
	// addManualVMs's firstDefaultYes), so a blank answer walks straight
	// into naming the first VM instead of requiring an explicit "y".
	in := strings.NewReader("\ndevbox\n/vms/devbox.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, nil)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "devbox" || vms[0].VMX != "/vms/devbox.vmx" {
		t.Errorf("selectVMs() = %+v, want the manually-entered devbox VM", vms)
	}
	if !strings.Contains(out.String(), "no VMs found automatically") {
		t.Errorf("output = %q, want a notice that discovery found nothing", out.String())
	}
}

func TestSelectVMs_ManualEntry_RejectsBlankName(t *testing.T) {
	// blank name is rejected by huh.ValidateNotEmpty(), so it reprompts;
	// give it a real name next.
	in := strings.NewReader("\n\ndevbox\n/vms/devbox.vmx\nn\n")
	var out bytes.Buffer

	vms, err := selectVMs(context.Background(), in, &out, true, nil)
	if err != nil {
		t.Fatalf("selectVMs() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "devbox" {
		t.Errorf("selectVMs() = %+v, want one retried devbox entry", vms)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run TestSelectVMs -v`
Expected: FAIL — `VMCandidate`, `selectVMs` don't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/init.go`:

```go
// Package tui also renders the interactive `snapback init` wizard, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md's `init` section.
// It never touches the filesystem -- internal/cli/init.go still owns VM
// discovery, config marshaling, and writing config.yaml, calling
// RunInitWizard only to collect answers, the same boundary
// tui.RunInteractive/backup.Run already keep.
package tui

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/huh"

	"github.com/xortim/snapback/internal/config"
)

// VMCandidate is a VM internal/cli's discovery already found (see
// internal/cli/vmdiscovery.go's discoverVMs), passed in rather than
// discovered by this package.
type VMCandidate struct {
	Name string
	VMX  string
}

// newForm builds a huh.Form wired to in/out. In accessible mode, in is
// wrapped in lineBufferedReader (see init_reader.go's doc comment for
// why); in real interactive mode it's passed through unwrapped, since
// WithInput also configures the underlying bubbletea program's raw
// terminal input, which must not be throttled.
func newForm(in io.Reader, out io.Writer, accessible bool, groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithOutput(out)
	if accessible {
		return form.WithAccessible(true).WithInput(lineBufferedReader{r: in})
	}
	return form.WithInput(in)
}

// runForm runs f, translating both a real ctx cancellation (ctrl+c
// propagated via huh's own tea.WithContext, in the real interactive
// path) and huh's own abort signal into the same "init cancelled: %w"
// wording internal/cli's other commands use. Accessible-mode forms don't
// support mid-read cancellation the same way (huh's runAccessible takes
// no context -- verified by reading huh@v1.0.0/form.go) -- acceptable
// since accessible mode's real-world use is piped/scripted input, not an
// interactive user waiting to press ctrl+c.
func runForm(ctx context.Context, f *huh.Form) error {
	if err := f.RunWithContext(ctx); err != nil {
		return fmt.Errorf("init cancelled: %w", err)
	}
	return nil
}

// selectVMs asks which discovered candidates to include (if any were
// found), then always offers manual entry afterward -- discoverVMs
// requires a bundle's .vmx to match the bundle's own name exactly, so a
// VM renamed in Finder after creation is invisible to discovery even
// though it's a valid VM; manual entry is the escape hatch for that,
// available whether or not discovery found anything.
func selectVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) ([]config.VM, error) {
	var vms []config.VM

	if len(candidates) > 0 {
		options := make([]huh.Option[string], len(candidates))
		for i, c := range candidates {
			options[i] = huh.NewOption(c.Name, c.Name).Selected(true)
		}
		var selected []string
		form := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Select VMs to back up").
					Options(options...).
					Value(&selected),
			),
		)
		if err := runForm(ctx, form); err != nil {
			return nil, err
		}

		byName := make(map[string]VMCandidate, len(candidates))
		for _, c := range candidates {
			byName[c.Name] = c
		}
		for _, name := range selected {
			c := byName[name]
			vms = append(vms, config.VM{Name: c.Name, VMX: c.VMX})
		}
	} else if _, err := fmt.Fprintln(out, "no VMs found automatically; add them manually below"); err != nil {
		return nil, err
	}

	manual, err := addManualVMs(ctx, in, out, accessible, len(candidates) == 0)
	if err != nil {
		return nil, err
	}
	return append(vms, manual...), nil
}

// addManualVMs loops "add a VM manually?" (Confirm) followed, if yes, by
// a name+.vmx-path pair, until the user declines. firstDefaultYes
// defaults the very first iteration's Confirm to true -- used when
// discovery found nothing, so the wizard walks straight into naming a
// VM instead of the user having to explicitly say "y" to a question
// whose answer is already implied by there being nothing to select from.
func addManualVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, firstDefaultYes bool) ([]config.VM, error) {
	var vms []config.VM
	first := true
	for {
		addMore := firstDefaultYes && first
		first = false

		confirmForm := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewConfirm().
					Title("Add a VM manually?").
					Value(&addMore),
			),
		)
		if err := runForm(ctx, confirmForm); err != nil {
			return nil, err
		}
		if !addMore {
			return vms, nil
		}

		var name, vmx string
		entryForm := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewInput().Title("VM name").Validate(huh.ValidateNotEmpty()).Value(&name),
				huh.NewInput().Title("Path to .vmx file").Validate(huh.ValidateNotEmpty()).Value(&vmx),
			),
		)
		if err := runForm(ctx, entryForm); err != nil {
			return nil, err
		}
		vms = append(vms, config.VM{Name: name, VMX: vmx})
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go
git commit -m "feat(tui): add init wizard VM selection and manual entry"
```

---

### Task 6: Core settings (`promptCoreSettings`)

**Files:**
- Modify: `internal/tui/init.go`
- Modify: `internal/tui/init_test.go`

**Interfaces:**
- Consumes: `validateWritableDestination`, `validateNonNegativeInt`, `acceptBlankInAccessibleMode` (Task 3).
- Produces (used by Task 8's `RunInitWizard`): `type coreSettings struct{ destination, compression string; keepLast, keepDaily, keepWeekly int; notify bool }`, `func promptCoreSettings(ctx context.Context, in io.Reader, out io.Writer, accessible bool) (coreSettings, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/init_test.go`:

```go
func TestPromptCoreSettings_AcceptsAllDefaults(t *testing.T) {
	// One blank line per field, in order: destination, compression,
	// keep_last, keep_daily, keep_weekly, notifications.
	in := strings.NewReader("\n\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	want := coreSettings{
		destination: defaultDestination,
		compression: defaultCompression,
		keepLast:    defaultKeepLast,
		keepDaily:   defaultKeepDaily,
		keepWeekly:  defaultKeepWeekly,
		notify:      true,
	}
	if got != want {
		t.Errorf("promptCoreSettings() = %+v, want %+v", got, want)
	}
}

func TestPromptCoreSettings_InvalidCompressionChoice_Reprompts(t *testing.T) {
	// destination blank, compression: a non-numeric answer (Select's
	// accessible mode only accepts a number) then "2" (gzip is the
	// second option), then defaults for the rest.
	in := strings.NewReader("\nbogus\n2\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.compression != "gzip" {
		t.Errorf("compression = %q, want %q", got.compression, "gzip")
	}
	if !strings.Contains(out.String(), "Invalid: must be a number between") {
		t.Errorf("output = %q, want a reprompt explaining the invalid choice", out.String())
	}
}

func TestPromptCoreSettings_InvalidRetentionCount_Reprompts(t *testing.T) {
	// destination blank, compression blank, keep_last: invalid then "3",
	// then defaults for keep_daily/keep_weekly/notifications.
	in := strings.NewReader("\n\nnotanumber\n3\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.keepLast != 3 {
		t.Errorf("keepLast = %d, want 3", got.keepLast)
	}
	if !strings.Contains(out.String(), `"notanumber" is not a whole number`) {
		t.Errorf("output = %q, want a reprompt explaining the invalid count", out.String())
	}
}

func TestPromptCoreSettings_InvalidNotifyAnswer_Reprompts(t *testing.T) {
	// destination/compression/retention x3 blank, notify: invalid then
	// "n".
	in := strings.NewReader("\n\n\n\n\nmaybe\nn\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.notify {
		t.Error("notify = true, want false (the retried answer)")
	}
	if !strings.Contains(out.String(), "invalid input") {
		t.Errorf("output = %q, want a reprompt explaining the invalid answer", out.String())
	}
}

func TestPromptCoreSettings_UnwritableDestination_Reprompts(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block writes")
	}

	writable := t.TempDir()
	in := strings.NewReader(filepath.Join(dir, "backups") + "\n" + filepath.Join(writable, "backups") + "\n\n\n\n\n\n")
	var out bytes.Buffer

	got, err := promptCoreSettings(context.Background(), in, &out, true)
	if err != nil {
		t.Fatalf("promptCoreSettings() error = %v", err)
	}
	if got.destination != filepath.Join(writable, "backups") {
		t.Errorf("destination = %q, want the retried writable path", got.destination)
	}
	if !strings.Contains(out.String(), "not writable") {
		t.Errorf("output = %q, want a reprompt explaining the unwritable destination", out.String())
	}
}
```

Add `"os"` and `"path/filepath"` to `internal/tui/init_test.go`'s import block for the last test.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run TestPromptCoreSettings -v`
Expected: FAIL — `promptCoreSettings`, `coreSettings`, `defaultDestination`, etc. don't exist yet.

- [ ] **Step 3: Write the implementation**

Add to `internal/tui/init.go` (new imports: `"strconv"`):

```go
// Defaults match internal/cli/init.go's old plain prompter, kept
// identical so a fresh `snapback init` proposes the same values as
// before this rewrite.
const (
	defaultDestination = "/Volumes/Backups/snapback"
	defaultCompression = "zstd"
	defaultKeepLast    = 5
	defaultKeepDaily   = 7
	defaultKeepWeekly  = 4
)

type coreSettings struct {
	destination string
	compression string
	keepLast    int
	keepDaily   int
	keepWeekly  int
	notify      bool
}

// promptCoreSettings asks destination, compression, the three retention
// counts, and whether to enable notifications -- one huh.Form of four
// groups, matching docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// "destination path → compression choice → retention numbers" (plus the
// pre-existing notifications toggle, part of config.Config before this
// rewrite and kept here rather than dropped).
func promptCoreSettings(ctx context.Context, in io.Reader, out io.Writer, accessible bool) (coreSettings, error) {
	destination := defaultDestination
	compression := defaultCompression
	keepLastStr := strconv.Itoa(defaultKeepLast)
	keepDailyStr := strconv.Itoa(defaultKeepDaily)
	keepWeeklyStr := strconv.Itoa(defaultKeepWeekly)
	notify := true

	form := newForm(in, out, accessible,
		huh.NewGroup(
			huh.NewInput().
				Title("Backup destination").
				Validate(acceptBlankInAccessibleMode(accessible, validateWritableDestination)).
				Value(&destination),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Compression").
				Options(huh.NewOption("zstd", "zstd"), huh.NewOption("gzip", "gzip")).
				Value(&compression),
		),
		huh.NewGroup(
			huh.NewInput().Title("Keep last N backups").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepLastStr),
			huh.NewInput().Title("Keep daily backups for N days").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepDailyStr),
			huh.NewInput().Title("Keep weekly backups for N weeks").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepWeeklyStr),
		),
		huh.NewGroup(
			huh.NewConfirm().Title("Enable notifications").Value(&notify),
		),
	)
	if err := runForm(ctx, form); err != nil {
		return coreSettings{}, err
	}

	// Each string is already validated by validateNonNegativeInt above,
	// so the parse here cannot fail.
	keepLast, _ := strconv.Atoi(strings.TrimSpace(keepLastStr))
	keepDaily, _ := strconv.Atoi(strings.TrimSpace(keepDailyStr))
	keepWeekly, _ := strconv.Atoi(strings.TrimSpace(keepWeeklyStr))

	return coreSettings{
		destination: destination,
		compression: compression,
		keepLast:    keepLast,
		keepDaily:   keepDaily,
		keepWeekly:  keepWeekly,
		notify:      notify,
	}, nil
}
```

Add `"strconv"` and `"strings"` to `internal/tui/init.go`'s import block.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go
git commit -m "feat(tui): add init wizard core settings prompts"
```

---

### Task 7: Per-VM schedule (`promptSchedules`)

**Files:**
- Modify: `internal/tui/init.go`
- Modify: `internal/tui/init_test.go`

**Interfaces:**
- Consumes: `scheduleChoices`, `resolveSchedule`, `validateCronExpression` (Tasks 3-4).
- Produces (used by Task 8's `RunInitWizard`): `func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM) error` (mutates `vms[i].Schedule` in place).

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/init_test.go`:

```go
func TestPromptSchedules_DefaultIsNone(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// schedule select blank (default "none"), custom-cron blank (unused).
	in := strings.NewReader("\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("Schedule = %q, want empty for the default \"none\" choice", vms[0].Schedule)
	}
}

func TestPromptSchedules_Nightly(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// "2" selects "nightly" (scheduleChoices[1]); custom-cron blank (unused).
	in := strings.NewReader("2\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != cronNightly {
		t.Errorf("Schedule = %q, want %q", vms[0].Schedule, cronNightly)
	}
}

func TestPromptSchedules_CustomCron_InvalidThenValid(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}
	// "4" selects "custom" (scheduleChoices[3]); custom-cron: invalid
	// (wrong field count) then a valid 5-field expression.
	in := strings.NewReader("4\nbadcron\n0 3 * * 1\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "0 3 * * 1" {
		t.Errorf("Schedule = %q, want %q", vms[0].Schedule, "0 3 * * 1")
	}
	if !strings.Contains(out.String(), "5 space-separated fields") {
		t.Errorf("output = %q, want a reprompt explaining the invalid cron expression", out.String())
	}
}

func TestPromptSchedules_MultipleVMs_AskedInOrder(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	// dev: blank (none), blank (unused). prod: "3" (weekly), blank (unused).
	in := strings.NewReader("\n\n3\n\n")
	var out bytes.Buffer

	if err := promptSchedules(context.Background(), in, &out, true, vms); err != nil {
		t.Fatalf("promptSchedules() error = %v", err)
	}
	if vms[0].Schedule != "" {
		t.Errorf("dev Schedule = %q, want empty", vms[0].Schedule)
	}
	if vms[1].Schedule != cronWeekly {
		t.Errorf("prod Schedule = %q, want %q", vms[1].Schedule, cronWeekly)
	}
	if !strings.Contains(out.String(), "Schedule for dev") || !strings.Contains(out.String(), "Schedule for prod") {
		t.Errorf("output = %q, want both VM names named in their own prompt", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run TestPromptSchedules -v`
Expected: FAIL — `promptSchedules` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Add to `internal/tui/init.go`:

```go
// promptSchedules asks a schedule preset for each VM in vms, in order,
// mutating vms[i].Schedule in place. The custom-cron question is always
// asked (see resolveSchedule's doc comment for why), gated only by its
// own Validate closure checking the choice already made in the same
// VM's prior group.
func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM) error {
	for i := range vms {
		choice := scheduleChoiceNone
		var custom string

		form := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Schedule for %s", vms[i].Name)).
					Options(huh.NewOptions(scheduleChoices...)...).
					Value(&choice),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Custom cron expression (only used if 'custom' was chosen above)").
					Validate(func(s string) error {
						if choice != scheduleChoiceCustom {
							return nil
						}
						return validateCronExpression(s)
					}).
					Value(&custom),
			),
		)
		if err := runForm(ctx, form); err != nil {
			return err
		}
		vms[i].Schedule = resolveSchedule(choice, custom)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go
git commit -m "feat(tui): add init wizard per-VM schedule prompts"
```

---

### Task 8: Review screen and orchestration (`reviewAndConfirm`, `RunInitWizard`)

**Files:**
- Modify: `internal/tui/init.go`
- Modify: `internal/tui/init_test.go`

**Interfaces:**
- Consumes: `selectVMs`, `promptCoreSettings`, `promptSchedules` (Tasks 5-7), `config.Validate`, `config.ValidateVMs`, `config.Marshal` (`internal/config`, unchanged).
- Produces (used by `internal/cli/init.go` in Task 9): `func RunInitWizard(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) (*config.Config, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/init_test.go`:

```go
func TestReviewAndConfirm_ShowsRenderedYAMLAndDefaultsToWrite(t *testing.T) {
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}},
	}
	in := strings.NewReader("\n") // accept the default "write this config? [Y/n]"
	var out bytes.Buffer

	ok, err := reviewAndConfirm(context.Background(), in, &out, true, cfg)
	if err != nil {
		t.Fatalf("reviewAndConfirm() error = %v", err)
	}
	if !ok {
		t.Error("reviewAndConfirm() = false, want true (the default)")
	}
	if !strings.Contains(out.String(), "name: dev") || !strings.Contains(out.String(), "destination: /dest") {
		t.Errorf("output = %q, want the rendered config YAML shown for review", out.String())
	}
}

func TestReviewAndConfirm_Declined_ReturnsFalse(t *testing.T) {
	cfg := &config.Config{Destination: "/dest", Compression: "zstd", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}
	in := strings.NewReader("n\n")
	var out bytes.Buffer

	ok, err := reviewAndConfirm(context.Background(), in, &out, true, cfg)
	if err != nil {
		t.Fatalf("reviewAndConfirm() error = %v", err)
	}
	if ok {
		t.Error("reviewAndConfirm() = true, want false")
	}
}

func TestRunInitWizard_EndToEnd_DiscoveredVMWithDefaults(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// VM select: "0" (confirm default selection), "n" (decline manual).
	// Core settings: 6 blanks (destination/compression/keep_last/
	// keep_daily/keep_weekly/notify), all defaults.
	// Schedule (1 VM): 2 blanks (choice=none, custom=unused).
	// Review: blank (accept default "write? [Y/n]" = yes).
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n\n")
	var out bytes.Buffer

	cfg, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err != nil {
		t.Fatalf("RunInitWizard() error = %v", err)
	}
	if len(cfg.VMs) != 1 || cfg.VMs[0].Name != "dev" {
		t.Fatalf("cfg.VMs = %+v, want the one discovered VM", cfg.VMs)
	}
	if cfg.Destination != defaultDestination || cfg.Compression != defaultCompression {
		t.Errorf("cfg = %+v, want default destination/compression", cfg)
	}
	if cfg.Retention.KeepLast != defaultKeepLast {
		t.Errorf("cfg.Retention.KeepLast = %d, want %d", cfg.Retention.KeepLast, defaultKeepLast)
	}
	if !cfg.Notifications.Enabled {
		t.Error("cfg.Notifications.Enabled = false, want true (the default)")
	}
}

func TestRunInitWizard_DuplicateVMName_FailsBeforeCoreSettings(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	// confirm default selection, then manually add another VM also named
	// "dev" -- config.ValidateVMs must catch this before promptCoreSettings
	// ever runs (there's no more scripted input for it to consume, so if
	// validation were deferred this test would hang/fail on a different
	// error).
	in := strings.NewReader("0\ny\ndev\n/vms/other.vmx\nn\n")
	var out bytes.Buffer

	_, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "invalid VM selection") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("RunInitWizard() error = %v, want it to mention \"invalid VM selection\" and \"duplicate\"", err)
	}
}

func TestRunInitWizard_DeclinedAtReview_ReturnsAbortedError(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\nn\n")
	var out bytes.Buffer

	_, err := RunInitWizard(context.Background(), in, &out, true, candidates)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("RunInitWizard() error = %v, want an \"aborted\" error", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestReviewAndConfirm|TestRunInitWizard' -v`
Expected: FAIL — `reviewAndConfirm`, `RunInitWizard` don't exist yet.

- [ ] **Step 3: Write the implementation**

Add to `internal/tui/init.go` (new import: `"errors"`):

```go
// reviewAndConfirm shows cfg rendered exactly as config.Marshal would
// write it (so the review is guaranteed accurate, not a hand-maintained
// summary that can drift from what actually gets written) and asks for
// confirmation before RunInitWizard returns it to the caller.
func reviewAndConfirm(ctx context.Context, in io.Reader, out io.Writer, accessible bool, cfg *config.Config) (bool, error) {
	rendered, err := config.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("render config for review: %w", err)
	}

	confirmed := true
	form := newForm(in, out, accessible,
		huh.NewGroup(
			huh.NewNote().
				Title("Review").
				Description(string(rendered)),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Write this config?").
				Value(&confirmed),
		),
	)
	if err := runForm(ctx, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

// RunInitWizard drives the interactive `snapback init` flow: select
// which discovered VMs to include (plus manual entry), core settings
// (destination/compression/retention/notifications), a schedule preset
// per included VM, then a review screen showing the exact YAML that
// will be written before writing anything. It returns the built,
// already config.Validate'd Config; internal/cli/init.go still owns
// marshaling and writing it to disk.
//
// accessible forces huh's plain sequential-prompt mode instead of a real
// bubbletea render -- internal/cli/init.go sets this from the same TTY
// check run.go already uses, since a real bubbletea program can't read a
// non-terminal stdin (a pipe, a test's strings.Reader) correctly. It's
// also how this package's own tests drive the wizard deterministically.
func RunInitWizard(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) (*config.Config, error) {
	vms, err := selectVMs(ctx, in, out, accessible, candidates)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateVMs(vms); err != nil {
		return nil, fmt.Errorf("invalid VM selection: %w", err)
	}

	settings, err := promptCoreSettings(ctx, in, out, accessible)
	if err != nil {
		return nil, err
	}

	if err := promptSchedules(ctx, in, out, accessible, vms); err != nil {
		return nil, err
	}

	cfg := &config.Config{
		Destination: settings.destination,
		Compression: settings.compression,
		Retention: config.Retention{
			KeepLast:   settings.keepLast,
			KeepDaily:  settings.keepDaily,
			KeepWeekly: settings.keepWeekly,
		},
		VMs:           vms,
		Notifications: config.Notifications{Enabled: settings.notify},
	}
	if err := config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("built an invalid config: %w", err)
	}

	ok, err := reviewAndConfirm(ctx, in, out, accessible, cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("init aborted: config not written")
	}
	return cfg, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/init.go internal/tui/init_test.go
git commit -m "feat(tui): add init wizard review screen and orchestration"
```

---

### Task 9: Wire `internal/cli/init.go` to the wizard

**Files:**
- Modify: `internal/cli/init.go`
- Modify: `internal/cli/init_internal_test.go`

**Interfaces:**
- Consumes: `tui.VMCandidate`, `tui.RunInitWizard` (Task 8), `defaultIsTerminal` (`internal/cli/run.go`, unchanged), `discoverVMsWithContext` (this file, unchanged).
- Produces: `initDeps` gains `isTerminal func(w io.Writer) bool` and `runWizard func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate) (*config.Config, error)`.

- [ ] **Step 1: Replace `internal/cli/init.go`'s prompting logic**

Delete the `prompter` type and every one of its methods (`promptVMs`, `promptManualVMs`, `promptString`, `promptChoice`, `promptInt`, `promptBool`) from `internal/cli/init.go`. Keep `writeConfigFile`, `configFileExists`, and `discoverVMsWithContext` exactly as they are.

Replace `initDeps`, `newInitCmd`, and `runInit` with:

```go
// initDeps groups init's external dependencies so tests can substitute a
// fake VM scanner, a fake wizard, a config writer that captures its
// argument, and a fake existing-file check instead of touching the real
// filesystem, a real terminal, or requiring a Fusion install.
type initDeps struct {
	searchDirs  func() []string
	discoverVMs func(searchDirs []string) ([]discoveredVM, error)
	marshal     func(cfg *config.Config) ([]byte, error)
	writeFile   func(path string, data []byte) error
	fileExists  func(path string) bool
	isTerminal  func(w io.Writer) bool
	runWizard   func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate) (*config.Config, error)
}

func newInitCmd() *cobra.Command {
	return newInitCmdWithDeps(initDeps{
		searchDirs:  defaultVMSearchDirs,
		discoverVMs: discoverVMs,
		marshal:     config.Marshal,
		writeFile:   writeConfigFile,
		fileExists:  configFileExists,
		isTerminal:  defaultIsTerminal,
		runWizard:   tui.RunInitWizard,
	})
}
```

Replace `runInit`'s body with:

```go
func runInit(cmd *cobra.Command, deps initDeps, force bool) error {
	configPath, err := configPathForCmd(cmd)
	if err != nil {
		return err
	}
	if !force && deps.fileExists(configPath) {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	}

	candidates, err := discoverVMsWithContext(cmd.Context(), deps.discoverVMs, deps.searchDirs())
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}
	tuiCandidates := make([]tui.VMCandidate, len(candidates))
	for i, c := range candidates {
		tuiCandidates[i] = tui.VMCandidate{Name: c.Name, VMX: c.VMX}
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	// A real bubbletea program can't read a non-terminal stdin correctly
	// (a pipe, a redirected file), so accessible mode is used whenever
	// out isn't a real terminal -- the same rule run.go's isTerminal
	// check already applies for choosing a renderer, applied here to
	// choosing a huh.Form mode instead.
	accessible := deps.isTerminal == nil || !deps.isTerminal(out)

	cfg, err := deps.runWizard(cmd.Context(), in, out, accessible, tuiCandidates)
	if err != nil {
		return err
	}

	data, err := deps.marshal(cfg)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := deps.writeFile(configPath, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
}
```

Update the file's import block: remove `"bufio"`, `"strconv"`, `"strings"` if nothing else in the file still uses them (check `newInitCmdWithDeps`, `writeConfigFile`, `configFileExists`, `discoverVMsWithContext` — none of those need those three); add `"context"` and `"github.com/xortim/snapback/internal/tui"`.

- [ ] **Step 2: Run the build to see what's still broken**

Run: `go build ./...`
Expected: FAIL in `internal/cli/init_internal_test.go` (references the now-deleted `prompter`-driven test helpers/behavior via `root.SetIn` scripted for the old prompt format). This is expected — Step 3 fixes it.

- [ ] **Step 3: Rewrite `internal/cli/init_internal_test.go`**

Keep unchanged (test bodies and assertions): `newTestRootForInit`, `TestWriteConfigFile_CreatesParentDirAndWritesContent`, `TestConfigFileExists`, `TestInitCmd_ExistingConfig_WithoutForce_Errors`. `TestInitCmd_ContextCancelledDuringDiscovery_StopsInsteadOfHanging` and `TestInitCmd_DiscoverVMsError_IsWrapped` keep their existing bodies/assertions too, but their `initDeps{...}` literals need two new fields added now that the struct has grown — see Step 3 below.

Delete (now covered by `internal/tui/init_test.go` instead): `TestInitCmd_WritesConfigFromDiscoveredVMAndDefaults`, `TestInitCmd_DiscoveredVMs_CanAlsoAddManually`, `TestInitCmd_NoDiscoveredVMs_PromptsManualEntry`, `TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts`, `TestInitCmd_InvalidCompressionChoice_Reprompts`, `TestInitCmd_InvalidRetentionCount_Reprompts`, `TestInitCmd_InvalidYesNoAnswer_Reprompts`, `TestInitCmd_InvalidChoiceThenEOF_Errors` (no longer meaningful: huh's accessible-mode `PromptString` silently falls back to whatever was already scanned on EOF instead of erroring — a real, intentional behavior difference from the old bespoke reader, not something to paper over with a misleading test), `TestInitCmd_ContextCancelled_StopsPromptingInsteadOfHanging` (ctx-cancellation during a blocked accessible-mode read isn't supported by huh — see `runForm`'s doc comment in `internal/tui/init.go`; the real interactive path's cancellation is delegated entirely to huh's own `Form.RunWithContext` and isn't independently re-tested here, the same way this codebase doesn't re-test that `context.WithCancel` itself works).

Replace `fakeInitDeps` and `TestInitCmd_ExistingConfig_WithForce_Overwrites` and `TestInitCmd_WriteFileError_IsWrapped` with wizard-aware versions, and add the new wiring tests:

```go
// fakeInitDeps returns an initDeps whose writeFile captures its argument
// into written, and whose fileExists/discoverVMs/runWizard are
// controlled by the caller -- covers the common case where a test only
// cares about what init would have written, not real disk I/O or
// prompting.
func fakeInitDeps(candidates []discoveredVM, exists bool, written *[]byte, writtenPath *string, cfg *config.Config) initDeps {
	return initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return candidates, nil },
		marshal:     config.Marshal,
		writeFile: func(path string, data []byte) error {
			*writtenPath = path
			*written = data
			return nil
		},
		fileExists: func(string) bool { return exists },
		isTerminal: func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) (*config.Config, error) {
			return cfg, nil
		},
	}
}

func TestInitCmd_WritesWizardResult(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}},
	}
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, false, &written, &writtenPath, cfg)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	if !strings.Contains(string(written), "name: dev") {
		t.Errorf("written config = %q, want the wizard's result marshaled", written)
	}
	if !strings.Contains(out.String(), "wrote config to /cfg/config.yaml") {
		t.Errorf("stdout = %q, want a confirmation naming the config path", out.String())
	}
}

func TestInitCmd_PassesDiscoveredCandidatesToWizard(t *testing.T) {
	var gotCandidates []tui.VMCandidate
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return []discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, _ bool, candidates []tui.VMCandidate) (*config.Config, error) {
			gotCandidates = candidates
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(gotCandidates) != 1 || gotCandidates[0].Name != "dev" || gotCandidates[0].VMX != "/vms/dev.vmx" {
		t.Errorf("candidates passed to runWizard = %+v, want the one discovered VM converted to tui.VMCandidate", gotCandidates)
	}
}

func TestInitCmd_IsTerminalTrue_UsesNonAccessibleMode(t *testing.T) {
	var gotAccessible bool
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return true },
		runWizard: func(_ context.Context, _ io.Reader, _ io.Writer, accessible bool, _ []tui.VMCandidate) (*config.Config, error) {
			gotAccessible = accessible
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotAccessible {
		t.Error("accessible = true, want false when isTerminal reports a real terminal")
	}
}

func TestInitCmd_WizardError_IsPropagatedUnwrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) (*config.Config, error) {
			return nil, errBoom
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, errBoom) {
		t.Fatalf("Execute() error = %v, want it to be (or wrap) %v", err, errBoom)
	}
}

func TestInitCmd_ExistingConfig_WithForce_Overwrites(t *testing.T) {
	var written []byte
	var writtenPath string
	cfg := &config.Config{Destination: "/dest", Compression: "zstd", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, true, &written, &writtenPath, cfg)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want --force to allow overwriting", err)
	}
	if written == nil {
		t.Errorf("writeFile was not called, want --force to allow the write")
	}
}

func TestInitCmd_WriteFileError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return errBoom },
		fileExists:  func(string) bool { return false },
		isTerminal:  func(io.Writer) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) (*config.Config, error) {
			return &config.Config{Destination: "/dest", Compression: "zstd"}, nil
		},
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "write config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"write config\" context", err, errBoom)
	}
}
```

Update `TestInitCmd_DiscoverVMsError_IsWrapped` and `TestInitCmd_ContextCancelledDuringDiscovery_StopsInsteadOfHanging`'s `initDeps{...}` literals to add `isTerminal: func(io.Writer) bool { return false }` and a `runWizard` field that fails the test if called (`func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) (*config.Config, error) { t.Fatal("runWizard should not be called"); return nil, nil }`), since both tests expect `runInit` to fail before ever reaching the wizard.

Update the file's import block: add `"context"`, `"errors"`, and `"github.com/xortim/snapback/internal/tui"`; drop `"os"`/`"path/filepath"`/`"time"` only if nothing else in the file still uses them (`TestWriteConfigFile_CreatesParentDirAndWritesContent` and `TestConfigFileExists` still need `"os"` and `"path/filepath"`; `"time"` was only used by the deleted `TestInitCmd_ContextCancelled_StopsPromptingInsteadOfHanging` test, so it can go — check `TestInitCmd_ContextCancelledDuringDiscovery_StopsInsteadOfHanging`, which still needs it for its own `time.AfterFunc`).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/cli/... -run TestInitCmd -v`
Expected: PASS for every test, old and new.

Run: `go test ./internal/cli/... -v`
Expected: PASS for the whole package.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/init.go internal/cli/init_internal_test.go
git commit -m "feat(cli): wire init to the interactive huh wizard"
```

---

### Task 10: Final verification

**Files:** `go.mod`, `go.sum` (possibly, via `go mod tidy`).

- [ ] **Step 1: Tidy go.mod now that huh is actually imported**

```bash
go mod tidy
git diff go.mod go.sum
```

Expected: no diff, or a trivial one (e.g. an indirect-dependency comment shuffle). If `huh` itself gets removed, stop and figure out why before continuing.

- [ ] **Step 2: Run the full lint/test/build suite**

Run: `make lint`
Expected: `0 issues.`

Run: `make test`
Expected: all packages PASS, including `internal/tui`'s new init-wizard tests.

Run: `make build`
Expected: succeeds, produces `dist/<goos-goarch>/snapback`.

- [ ] **Step 3: Note the manual-verification and known-limitation gaps honestly**

There is no real terminal render exercised anywhere in this environment (all tests drive huh's accessible mode, deliberately, since it's deterministic — see `runForm`'s doc comment). The rich interactive rendering (colors, arrow-key VM multi-select, the review screen's layout) has never been visually confirmed. Record this plainly: the user should run one real `snapback init` at a real terminal once this lands, to confirm the interactive rendering looks right in practice — the same kind of gap the `run` TUI plan flagged for its own checklist rendering.

Also record, in the PR description, the two behavior differences from the old plain prompter found and deliberately accepted during this work: (1) EOF partway through accessible-mode input no longer hard-errors — huh's `PromptString` falls back to whatever was already entered instead; (2) ctrl+c/context-cancellation during accessible-mode prompting (piped/non-tty `snapback init`) is not interruptible mid-read — only the real interactive terminal path gets huh's native cancellation. Both are pre-existing huh library behavior, not new complexity introduced by this plan, and both only affect the non-interactive (accessible) path, not normal terminal usage.

- [ ] **Step 4: Push the branch and open the PR**

```bash
git push -u origin feat/init-huh-wizard
gh pr create --title "feat(cli): interactive huh wizard for init" --body "$(cat <<'EOF'
## Summary
- Adds internal/tui/init.go (+ init_reader.go, init_validate.go, init_schedule.go): a charmbracelet/huh wizard for `snapback init` -- VM select (+ manual entry) -> destination -> compression -> retention -> per-VM schedule preset -> a review screen showing the exact YAML before writing, per docs/superpowers/specs/2026-08-23-cli-ux-design.md
- internal/cli/init.go now delegates all prompting to tui.RunInitWizard, picking huh's accessible (plain) mode on a non-terminal stdout, real interactive rendering otherwise -- same TTY-check pattern run.go already uses
- Found and fixed a real correctness bug along the way: huh's accessible-mode prompts each construct a fresh bufio.Scanner per question, which silently drops input beyond the first line whenever the underlying reader can return more than one line per Read() call (true of piped/redirected stdin, not just strings.Reader in tests) -- internal/tui/init_reader.go's lineBufferedReader works around it

Implements the `init` slice of issue #17 (the `run` checklist landed in #45/#46; `status --vm`'s card view is a separate follow-on).

## Known, deliberately-accepted behavior differences from the old plain prompter
- EOF partway through non-interactive (accessible-mode) input no longer hard-errors; huh falls back to whatever was already typed
- ctrl+c during non-interactive (piped/redirected stdin) prompting isn't interruptible mid-read; the real interactive terminal path is unaffected (huh's own RunWithContext handles that natively)

## Test plan
- [x] `make lint`
- [x] `make test`
- [x] `make build`
- [ ] Manual: run `snapback init` at a real terminal, confirm VM multi-select, the review screen, and colors render as expected -- not done in this environment (no real terminal available here); needs a pass by the user
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** "discover VMs → select which to manage" (Task 5), "destination path → compression choice → retention numbers" (Task 6), "per-VM schedule (presets: nightly/weekly/custom cron)" (Task 7, plus a "none" preset added because nothing consumes the field yet — documented as a deliberate addition, not a gap), "review screen → write config.yaml" (Task 8 + Task 9's actual write). The spec's "no non-TTY fallback" note is honored in spirit — no `--yes`/scripted flag is added; accessible mode still asks every question, just without a bubbletea render, chosen automatically the same way `run.go` already picks its renderer automatically.
- **Placeholder scan:** none found on re-read; every validator, prompt function, and test has real, complete code.
- **Type consistency:** `tui.RunInitWizard`'s signature (`func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) (*config.Config, error)`, defined in Task 8) matches `initDeps.runWizard`'s field type and every test's fake in Task 9 exactly. `VMCandidate{Name, VMX string}` (Task 5) matches `discoveredVM{Name, VMX string}`'s shape, and `internal/cli/init.go`'s conversion loop (Task 9) maps between them field-by-field.
- **Verified, not assumed:** every huh API call in this plan (`Form.WithAccessible`/`WithInput`/`WithOutput`/`RunWithContext`, `Group.WithHideFunc`'s accessible-mode gap, `Input`/`Select`/`MultiSelect`/`Confirm`/`Note`'s `Value`/`Validate`/`RunAccessible` behavior, `Option.Selected`, `NewOptions`, the accessible-mode default-substitution and exact reprompt wording) was checked directly against huh v1.0.0's downloaded source, not recalled from memory — see the Global Constraints section for the three non-obvious findings (group-hide is ignored in accessible mode; scanner reuse drops input; `Input`'s accessible-mode `Validate` runs before default-substitution, unlike `Select`/`Confirm`) that shaped this plan's design. The third of these was caught by an independent review of an earlier draft of this plan, which traced `accessibility.PromptString`'s call order directly against the same downloaded source; `acceptBlankInAccessibleMode` (Task 3) and its use in Task 6 are the fix.
- **Consistency fix from the same review:** Task 9's instructions for `TestInitCmd_DiscoverVMsError_IsWrapped`/`TestInitCmd_ContextCancelledDuringDiscovery_StopsInsteadOfHanging` previously said "keep unchanged" and then separately said to add `isTerminal`/`runWizard` fields to their `initDeps{...}` literals — a direct contradiction. Resolved: their bodies/assertions are unchanged, but their `initDeps` literals do need the two new fields (Step 3 is the single source of truth for that).
- **Duplication considered and rejected:** the same review flagged `internal/tui`'s original `expandHome` as a verbatim duplicate of `internal/config`'s unexported `expandTilde`. Fixed by exporting `config.ExpandTilde` (Task 3) and having `validateWritableDestination` call it directly instead of maintaining a second copy.
- **Per-task lint cadence, considered and kept as-is:** each task here runs only a targeted `go test`/`go build`, with the full `make lint`/`make test`/`make build` deferred to Task 10 — this mirrors `docs/superpowers/plans/2026-09-07-run-tui-renderer.md`'s established convention exactly, not a gap unique to this plan. Not changed.
