# Run Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `snapback run` nobody is watching (non-TTY, e.g. launchd) sends a macOS notification on failure — and on success unless `notifications.on_success: false` — plus a `snapback notify test` command to get the notification permission granted.

**Architecture:** New `internal/notify` package (a `Notifier` interface, a real `OSAScript` implementation that passes all user strings as `osascript` argv, a `Fake`, and pure message builders). `internal/cli`'s `runVM` decides whether to notify after the backup outcome is known; the choreography (`internal/backup`) is untouched. Config gains `notifications.on_success`, defaulting to `true` when absent.

**Tech Stack:** Go 1.26.5, cobra, koanf, `/usr/bin/osascript`.

**Spec:** `docs/superpowers/specs/2026-10-26-run-notifications-design.md`

## Global Constraints

- Module path `github.com/xortim/snapback`; run `make lint` and `make test` (CI's own targets) before each commit — not ad-hoc `go vet`.
- Conventional commits with package as scope, e.g. `feat(notify): ...`; every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- `osascript` is invoked by absolute path `/usr/bin/osascript` (launchd agents don't inherit `$PATH`).
- User-controlled strings (VM names, error text) are **never** interpolated into AppleScript source — only passed as trailing argv.
- `Notify` applies a 5-second timeout.
- A notification failure never changes `run`'s returned error or exit code; it prints `warning: could not send notification: <err>` to stderr.
- `internal/notify` must not import `internal/backup` or `internal/cli`.
- Notification copy (exact): title `snapback`; success subtitle `<vm> backed up`, message = archive ID; failure subtitle `<vm> backup failed`, message = first line of error truncated to 200 runes with `…`, failure sound `Basso`; cleanup suffix ` — run snapback cleanup --vm <vm>` appended after truncation.
- Fakes live in non-test files (pattern: `vm.FakeVMController`, `launchd.FakeInstaller`).

## Review Focus

- A VM name or error message containing `"`, `\`, newlines, or `& do shell script` — must reach `osascript` only as argv, never inside an `-e` script line (Task 1 test).
- A multi-line or very long error (e.g. wrapped vmcli stderr) — message must be the first line only, ≤200 runes, and the cleanup hint must survive truncation (Task 2 test).
- A non-TTY run interrupted by SIGINT (context canceled) — the failure notification must still be attempted rather than failing instantly on the canceled context (Task 4 uses `context.WithoutCancel`; test asserts the ctx passed to `Notify` is not canceled).
- An existing config.yaml with no `on_success` key — must keep notifying on success once enabled (Task 3 test).
- A re-run of `init --force` over a config with `on_success: false` — must not silently flip it back to `true` (Task 3 test).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/notify/notify.go` (create) | `Notification`, `Notifier`, `OSAScript`, `NewOSAScript` |
| `internal/notify/fake.go` (create) | `Fake` recording notifier |
| `internal/notify/message.go` (create) | `Success`, `Failure` builders |
| `internal/notify/notify_test.go`, `message_test.go`, `fake_test.go` (create) | unit tests |
| `internal/notify/osascript_integration_test.go` (create) | one real `osascript` call, `integration` tag |
| `internal/config/config.go` (modify) | `Notifications.OnSuccess`, default in `Load` |
| `internal/tui/init.go` (modify) | carry `OnSuccess` through the wizard |
| `internal/cli/run.go` (modify) | `newNotifier` dep, `snapshotMayRemain`, `notifyRunOutcome` |
| `internal/cli/notify.go` (create) | `snapback notify test` |
| `internal/cli/root.go` (modify) | register `notify` command |
| `internal/cli/init.go` (modify) | print `notify test` tip |
| `internal/progress/progress.go`, `docs/design.md`, `CLAUDE.md` (modify) | docs |

---

### Task 1: `internal/notify` — Notifier, OSAScript, Fake

**Files:**
- Create: `internal/notify/notify.go`, `internal/notify/fake.go`
- Test: `internal/notify/notify_test.go`, `internal/notify/fake_test.go`

**Interfaces:**
- Produces:
  - `type Notification struct { Title, Subtitle, Message, Sound string }`
  - `type Notifier interface { Notify(ctx context.Context, n Notification) error }`
  - `type OSAScript struct { Run func(ctx context.Context, name string, args ...string) ([]byte, error) }`
  - `func NewOSAScript() *OSAScript`
  - `const OSAScriptPath = "/usr/bin/osascript"`, `const NotifyTimeout = 5 * time.Second`
  - `type Fake struct { Err error; Sent []Notification; Contexts []context.Context }` with `func (f *Fake) Notify(ctx context.Context, n Notification) error`

- [ ] **Step 1: Write the failing tests**

`internal/notify/notify_test.go`:

```go
package notify_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/notify"
)

type recordedCall struct {
	name     string
	args     []string
	deadline time.Time
	hasDL    bool
}

func recordingOSAScript(out []byte, err error) (*notify.OSAScript, *[]recordedCall) {
	var calls []recordedCall
	n := &notify.OSAScript{
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			dl, ok := ctx.Deadline()
			calls = append(calls, recordedCall{name: name, args: args, deadline: dl, hasDL: ok})
			return out, err
		},
	}
	return n, &calls
}

// scriptLines returns every argument that follows a "-e" flag.
func scriptLines(args []string) []string {
	var lines []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" {
			lines = append(lines, args[i+1])
		}
	}
	return lines
}

// trailingArgs returns everything after the last "-e <line>" pair.
func trailingArgs(args []string) []string {
	last := -1
	for i, a := range args {
		if a == "-e" {
			last = i
		}
	}
	return args[last+2:]
}

func TestOSAScript_NoSound_PassesStringsAsArgv(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	err := n.Notify(context.Background(), notify.Notification{Title: "snapback", Subtitle: "myvm backed up", Message: "myvm-20261026"})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("Run called %d times, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.name != notify.OSAScriptPath {
		t.Errorf("name = %q, want %q", c.name, notify.OSAScriptPath)
	}
	if got, want := trailingArgs(c.args), []string{"snapback", "myvm backed up", "myvm-20261026"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %q, want %q", got, want)
	}
	script := strings.Join(scriptLines(c.args), "\n")
	if strings.Contains(script, "sound name") {
		t.Errorf("script = %q, want no sound clause when Sound is empty", script)
	}
	if !strings.Contains(script, "on run argv") {
		t.Errorf("script = %q, want an argv-reading run handler", script)
	}
}

func TestOSAScript_WithSound_AppendsSoundArgAndClause(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	if err := n.Notify(context.Background(), notify.Notification{Title: "t", Subtitle: "s", Message: "m", Sound: "Basso"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	if got, want := trailingArgs(c.args), []string{"t", "s", "m", "Basso"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %q, want %q", got, want)
	}
	if script := strings.Join(scriptLines(c.args), "\n"); !strings.Contains(script, "sound name (item 4 of argv)") {
		t.Errorf("script = %q, want a sound clause reading item 4 of argv", script)
	}
}

func TestOSAScript_HostileStrings_NeverReachScriptSource(t *testing.T) {
	hostile := "x\" & (do shell script \"touch /tmp/pwned\") & \"\\\nline2"
	n, calls := recordingOSAScript(nil, nil)
	if err := n.Notify(context.Background(), notify.Notification{Title: "snapback", Subtitle: hostile, Message: hostile, Sound: "Basso"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	for _, line := range scriptLines(c.args) {
		if strings.Contains(line, "pwned") || strings.Contains(line, hostile) {
			t.Fatalf("script line %q contains user data; strings must only travel as argv", line)
		}
	}
	trail := trailingArgs(c.args)
	if trail[1] != hostile || trail[2] != hostile {
		t.Errorf("trailing args = %q, want the hostile string passed through verbatim as argv", trail)
	}
}

func TestOSAScript_AppliesTimeout(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	before := time.Now()
	if err := n.Notify(context.Background(), notify.Notification{Title: "t"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	if !c.hasDL {
		t.Fatal("ctx passed to Run has no deadline, want NotifyTimeout applied")
	}
	if d := c.deadline.Sub(before); d > notify.NotifyTimeout+time.Second || d <= 0 {
		t.Errorf("deadline is %v after start, want about %v", d, notify.NotifyTimeout)
	}
}

func TestOSAScript_RunError_IsWrappedWithOutput(t *testing.T) {
	boom := errors.New("exit status 1")
	n, _ := recordingOSAScript([]byte("execution error: nope"), boom)
	err := n.Notify(context.Background(), notify.Notification{Title: "t"})
	if !errors.Is(err, boom) {
		t.Fatalf("Notify() error = %v, want it to wrap the Run error", err)
	}
	if !strings.Contains(err.Error(), "execution error: nope") {
		t.Errorf("Notify() error = %q, want osascript's output included", err.Error())
	}
}

func TestNewOSAScript_HasRealRunner(t *testing.T) {
	if notify.NewOSAScript().Run == nil {
		t.Fatal("NewOSAScript().Run = nil, want a real exec-backed runner")
	}
}
```

`internal/notify/fake_test.go`:

```go
package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

var _ notify.Notifier = (*notify.Fake)(nil)
var _ notify.Notifier = (*notify.OSAScript)(nil)

func TestFake_RecordsAndReturnsErr(t *testing.T) {
	boom := errors.New("boom")
	f := &notify.Fake{Err: boom}
	n := notify.Notification{Title: "t", Message: "m"}
	if err := f.Notify(context.Background(), n); !errors.Is(err, boom) {
		t.Fatalf("Notify() error = %v, want boom", err)
	}
	if len(f.Sent) != 1 || f.Sent[0] != n {
		t.Errorf("Sent = %+v, want [%+v] (recorded even when returning Err)", f.Sent, n)
	}
	if len(f.Contexts) != 1 {
		t.Errorf("Contexts len = %d, want 1", len(f.Contexts))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/... -v`
Expected: FAIL — package/identifiers undefined.

- [ ] **Step 3: Implement**

`internal/notify/notify.go`:

```go
// Package notify sends macOS user notifications. Its only real
// implementation shells out to osascript -- see
// docs/superpowers/specs/2026-10-26-run-notifications-design.md for why
// (UserNotifications needs a signed app bundle; a bare CLI binary isn't
// one) and why every caller goes through the Notifier interface so a
// native helper-app implementation can replace it later.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// OSAScriptPath is osascript's absolute path. launchd agents don't
// inherit the interactive shell's $PATH, so never rely on a lookup.
const OSAScriptPath = "/usr/bin/osascript"

// NotifyTimeout bounds a single osascript invocation, on top of
// whatever deadline the caller's context already carries.
const NotifyTimeout = 5 * time.Second

// Notification is one user-visible macOS notification.
type Notification struct {
	Title    string
	Subtitle string
	Message  string
	Sound    string // macOS sound name, e.g. "Basso"; "" = no sound
}

// Notifier sends a Notification.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// The two fixed scripts OSAScript chooses between. Every user-supplied
// string is read from argv -- never spliced into script source -- so a
// VM name like `x" & do shell script "..."` can't inject AppleScript.
var (
	scriptNoSound = []string{
		"on run argv",
		"display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)",
		"end run",
	}
	scriptWithSound = []string{
		"on run argv",
		"display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv) sound name (item 4 of argv)",
		"end run",
	}
)

// OSAScript is the real Notifier, backed by osascript's
// `display notification`. Run is the process runner; NewOSAScript sets
// it to an exec.CommandContext-backed one, and tests replace it to
// assert exact argv without running osascript.
type OSAScript struct {
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewOSAScript returns an OSAScript that really runs osascript.
func NewOSAScript() *OSAScript {
	return &OSAScript{Run: runCombined}
}

func runCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Notify implements Notifier.
func (o *OSAScript) Notify(ctx context.Context, n Notification) error {
	ctx, cancel := context.WithTimeout(ctx, NotifyTimeout)
	defer cancel()

	script := scriptNoSound
	argv := []string{n.Title, n.Subtitle, n.Message}
	if n.Sound != "" {
		script = scriptWithSound
		argv = append(argv, n.Sound)
	}

	args := make([]string, 0, 2*len(script)+len(argv))
	for _, line := range script {
		args = append(args, "-e", line)
	}
	args = append(args, argv...)

	out, err := o.Run(ctx, OSAScriptPath, args...)
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

`internal/notify/fake.go`:

```go
package notify

import (
	"context"
	"sync"
)

// Fake is an in-memory Notifier for unit tests, mirroring
// vm.FakeVMController and launchd.FakeInstaller. Every call is recorded
// before Err (if set) is returned, so a failed send still shows up in
// Sent for a test to assert against.
type Fake struct {
	Err      error
	Sent     []Notification
	Contexts []context.Context

	mu sync.Mutex
}

// Notify implements Notifier.
func (f *Fake) Notify(ctx context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, n)
	f.Contexts = append(f.Contexts, ctx)
	return f.Err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/... -v`
Expected: PASS

- [ ] **Step 5: Lint, full tests, commit**

```bash
make lint && make test
git add internal/notify
git commit -m "feat(notify): add osascript-backed Notifier and Fake

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `internal/notify` — message builders

**Files:**
- Create: `internal/notify/message.go`
- Test: `internal/notify/message_test.go`

**Interfaces:**
- Consumes: `Notification` (Task 1)
- Produces:
  - `func Success(vmName, archiveID string) Notification`
  - `func Failure(vmName string, err error, snapshotMayRemain bool) Notification`
  - `const FailureSound = "Basso"`, `const maxMessageRunes = 200`

- [ ] **Step 1: Write the failing tests**

`internal/notify/message_test.go`:

```go
package notify_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xortim/snapback/internal/notify"
)

func TestSuccess(t *testing.T) {
	got := notify.Success("myvm", "myvm-20261026T000000Z")
	want := notify.Notification{Title: "snapback", Subtitle: "myvm backed up", Message: "myvm-20261026T000000Z"}
	if got != want {
		t.Errorf("Success() = %+v, want %+v", got, want)
	}
}

func TestSuccess_EmptyArchiveID_StillSaysComplete(t *testing.T) {
	if got := notify.Success("myvm", ""); got.Message != "backup complete" {
		t.Errorf("Message = %q, want %q fallback", got.Message, "backup complete")
	}
}

func TestFailure_Basic(t *testing.T) {
	got := notify.Failure("myvm", errors.New("snapshot: boom"), false)
	want := notify.Notification{Title: "snapback", Subtitle: "myvm backup failed", Message: "snapshot: boom", Sound: notify.FailureSound}
	if got != want {
		t.Errorf("Failure() = %+v, want %+v", got, want)
	}
}

func TestFailure_MultiLine_UsesFirstLine(t *testing.T) {
	got := notify.Failure("myvm", errors.New("copy failed\nvmcli stderr: details\nmore"), false)
	if got.Message != "copy failed" {
		t.Errorf("Message = %q, want first line only", got.Message)
	}
}

func TestFailure_Long_TruncatesTo200RunesWithEllipsis(t *testing.T) {
	long := strings.Repeat("é", 500)
	got := notify.Failure("myvm", errors.New(long), false)
	if n := utf8.RuneCountInString(got.Message); n != 200 {
		t.Errorf("Message rune count = %d, want 200", n)
	}
	if !strings.HasSuffix(got.Message, "…") {
		t.Errorf("Message = %q, want trailing ellipsis", got.Message)
	}
}

func TestFailure_SnapshotMayRemain_SuffixSurvivesTruncation(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := notify.Failure("myvm", errors.New(long), true)
	if !strings.HasSuffix(got.Message, " — run snapback cleanup --vm myvm") {
		t.Errorf("Message = %q, want cleanup suffix appended after truncation", got.Message)
	}
	if !strings.Contains(got.Message, "…") {
		t.Errorf("Message = %q, want the error part truncated", got.Message)
	}
}

func TestFailure_EmptyOrNilError(t *testing.T) {
	if got := notify.Failure("myvm", errors.New(""), false); got.Message != "unknown error" {
		t.Errorf("empty error Message = %q, want %q", got.Message, "unknown error")
	}
	if got := notify.Failure("myvm", nil, false); got.Message != "unknown error" {
		t.Errorf("nil error Message = %q, want %q", got.Message, "unknown error")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/notify/... -run 'TestSuccess|TestFailure' -v`
Expected: FAIL — `Success`/`Failure` undefined.

- [ ] **Step 3: Implement**

`internal/notify/message.go`:

```go
package notify

import (
	"fmt"
	"strings"
)

// FailureSound is played for failure notifications only -- a success
// notification for a nightly backup should be quiet.
const FailureSound = "Basso"

// maxMessageRunes caps the error text in a failure notification;
// Notification Center truncates long bodies anyway, and a wrapped vmcli
// error can run to kilobytes.
const maxMessageRunes = 200

const title = "snapback"

// Success builds the notification for a completed backup.
func Success(vmName, archiveID string) Notification {
	msg := archiveID
	if msg == "" {
		msg = "backup complete"
	}
	return Notification{Title: title, Subtitle: vmName + " backed up", Message: msg}
}

// Failure builds the notification for a failed backup. snapshotMayRemain
// appends a pointer to `snapback cleanup` -- computed by the caller
// (internal/cli) with the same predicate its stderr warning uses, so the
// two can't disagree. The suffix is appended after truncation so it's
// never the part that gets cut off.
func Failure(vmName string, err error, snapshotMayRemain bool) Notification {
	msg := ""
	if err != nil {
		msg, _, _ = strings.Cut(err.Error(), "\n")
		msg = strings.TrimSpace(msg)
	}
	if msg == "" {
		msg = "unknown error"
	}
	if r := []rune(msg); len(r) > maxMessageRunes {
		msg = string(r[:maxMessageRunes-1]) + "…"
	}
	if snapshotMayRemain {
		msg += fmt.Sprintf(" — run snapback cleanup --vm %s", vmName)
	}
	return Notification{Title: title, Subtitle: vmName + " backup failed", Message: msg, Sound: FailureSound}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/notify/... -v`
Expected: PASS

- [ ] **Step 5: Lint, test, commit**

```bash
make lint && make test
git add internal/notify/message.go internal/notify/message_test.go
git commit -m "feat(notify): add success/failure notification builders

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Config `on_success` + wizard carry-through

**Files:**
- Modify: `internal/config/config.go` (`Notifications` struct ~line 36; `Load` after `k.Unmarshal`)
- Modify: `internal/tui/init.go` (`coreSettings` ~line 134; `promptCoreSettings` ~lines 163-171 and return ~line 213; config build ~line 547)
- Test: `internal/config/config_test.go`, `internal/config/marshal_test.go`, `internal/tui/init_test.go`

**Interfaces:**
- Produces: `config.Notifications{Enabled bool; OnSuccess bool}` — yaml/koanf key `on_success`; `Load` sets `OnSuccess = true` when the key is absent.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go` (uses the existing `writeTempConfig` helper):

```go
func TestLoad_OnSuccessAbsent_DefaultsTrue(t *testing.T) {
	path := writeTempConfig(t, "destination: /d\nnotifications:\n  enabled: true\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Notifications.OnSuccess {
		t.Error("OnSuccess = false, want true when on_success is absent")
	}
}

func TestLoad_NoNotificationsBlock_OnSuccessDefaultsTrue(t *testing.T) {
	path := writeTempConfig(t, "destination: /d\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Notifications.OnSuccess {
		t.Error("OnSuccess = false, want true when notifications block is absent")
	}
}

func TestLoad_OnSuccessExplicitFalse_Honored(t *testing.T) {
	path := writeTempConfig(t, "destination: /d\nnotifications:\n  enabled: true\n  on_success: false\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Notifications.OnSuccess {
		t.Error("OnSuccess = true, want explicit false honored")
	}
}
```

Append to `internal/config/marshal_test.go` (add `strings` import if missing):

```go
func TestMarshal_AlwaysWritesOnSuccess(t *testing.T) {
	out, err := config.Marshal(&config.Config{Destination: "/d", Compression: "zstd", Notifications: config.Notifications{Enabled: true, OnSuccess: false}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(out), "on_success: false") {
		t.Errorf("Marshal() = %s, want an explicit on_success: false", out)
	}
}
```

Append to `internal/tui/init_test.go`:

```go
func TestRunInitWizard_FreshInit_OnSuccessTrue(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n")
	cfg, err := RunInitWizard(context.Background(), in, &bytes.Buffer{}, true, candidates, nil)
	if err != nil {
		t.Fatalf("RunInitWizard() error = %v", err)
	}
	if !cfg.Notifications.OnSuccess {
		t.Error("OnSuccess = false, want true on a fresh init")
	}
}

func TestRunInitWizard_Force_PreservesOnSuccessFalse(t *testing.T) {
	candidates := []VMCandidate{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}
	prior := &config.Config{
		Destination: defaultDestination,
		Compression: defaultCompression,
		Retention: config.Retention{
			KeepLast:   defaultKeepLast,
			KeepDaily:  defaultKeepDaily,
			KeepWeekly: defaultKeepWeekly,
		},
		VMs:           []config.VM{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}},
		Notifications: config.Notifications{Enabled: true, OnSuccess: false},
	}
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n")
	cfg, err := RunInitWizard(context.Background(), in, &bytes.Buffer{}, true, candidates, prior)
	if err != nil {
		t.Fatalf("RunInitWizard() error = %v", err)
	}
	if cfg.Notifications.OnSuccess {
		t.Error("OnSuccess = true, want prior's false preserved across init --force")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/... ./internal/tui/... -run 'OnSuccess' -v`
Expected: FAIL — `OnSuccess` field undefined.

- [ ] **Step 3: Implement**

`internal/config/config.go`:

```go
type Notifications struct {
	Enabled bool `koanf:"enabled" yaml:"enabled"`
	// OnSuccess controls whether a successful unattended run notifies;
	// failures always notify when Enabled. Load defaults it to true when
	// the key is absent so configs written before it existed keep
	// notifying on success -- see
	// docs/superpowers/specs/2026-10-26-run-notifications-design.md.
	OnSuccess bool `koanf:"on_success" yaml:"on_success"`
}
```

In `Load`, immediately after the `k.Unmarshal("", &cfg)` error check:

```go
	if !k.Exists("notifications.on_success") {
		cfg.Notifications.OnSuccess = true
	}
```

`internal/tui/init.go`:
- add `onSuccess bool` to `coreSettings` (after `notify`);
- in `promptCoreSettings`: after `notify := true` add `onSuccess := true`; inside `if prior != nil {` add `onSuccess = prior.Notifications.OnSuccess`; add `onSuccess: onSuccess,` to the returned `coreSettings`;
- in the config build (~line 547): `Notifications: config.Notifications{Enabled: settings.notify, OnSuccess: settings.onSuccess},`.

No prompt is added — the wizard's question count is unchanged.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config/... ./internal/tui/... -v`
Expected: PASS (including all pre-existing tests; `TestLoad_ParsesFullConfig` etc. are unaffected).

- [ ] **Step 5: Lint, test, commit**

```bash
make lint && make test
git add internal/config internal/tui
git commit -m "feat(config): add notifications.on_success, defaulting to true

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Wire notifications into `run`

**Files:**
- Modify: `internal/cli/run.go`
- Test: `internal/cli/run_internal_test.go`

**Interfaces:**
- Consumes: `notify.Notifier`, `notify.Fake`, `notify.Success`, `notify.Failure`, `notify.NewOSAScript` (Tasks 1-2); `config.Notifications.OnSuccess` (Task 3)
- Produces:
  - `runDeps.newNotifier func() notify.Notifier` (nil = never notify)
  - `func snapshotMayRemain(err error) bool`
  - `func notifyRunOutcome(cmd *cobra.Command, deps runDeps, cfg *config.Config, vmName string, result *backup.Result, runErr error)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/run_internal_test.go` (add `"github.com/xortim/snapback/internal/notify"` to imports):

```go
func notifyTestDeps(t *testing.T, cfg *config.Config, fake *vm.FakeVMController, tty bool, n *notify.Fake) runDeps {
	t.Helper()
	return runDeps{
		loadConfig:    func(string) (*config.Config, error) { return cfg, nil },
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return tty },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return backupFn(progress.NoOpReporter{})
		},
		newNotifier: func() notify.Notifier { return n },
	}
}

func TestRunCmd_Notifications_Gating(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		onSuccess bool
		tty       bool
		fail      bool
		wantSent  bool
	}{
		{"disabled, success", false, true, false, false, false},
		{"disabled, failure", false, true, false, true, false},
		{"tty, success", true, true, true, false, false},
		{"tty, failure", true, true, true, true, false},
		{"non-tty, success, on_success", true, true, false, false, true},
		{"non-tty, success, !on_success", true, false, false, false, false},
		{"non-tty, failure, on_success", true, true, false, true, true},
		{"non-tty, failure, !on_success", true, false, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmxPath := writeVMBundle(t)
			ctrl := vm.NewFakeVMController()
			ctrl.ToolsState = vm.ToolsRunning
			if tt.fail {
				ctrl.DeleteSnapshotErr = errBoom
			}
			cfg := &config.Config{
				Destination:   t.TempDir(),
				Compression:   "gzip",
				VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
				Notifications: config.Notifications{Enabled: tt.enabled, OnSuccess: tt.onSuccess},
			}
			n := &notify.Fake{}
			root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, tt.tty, n))
			root.SetArgs([]string{"run", "--vm", "myvm"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if (err != nil) != tt.fail {
				t.Fatalf("Execute() error = %v, want failure=%v", err, tt.fail)
			}
			if got := len(n.Sent) == 1; got != tt.wantSent {
				t.Fatalf("sent %d notifications, want sent=%v", len(n.Sent), tt.wantSent)
			}
		})
	}
}

func TestRunCmd_Notifications_SuccessContent(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true, OnSuccess: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := n.Sent[0]
	if got.Subtitle != "myvm backed up" || got.Message == "" || got.Message == "backup complete" || got.Sound != "" {
		t.Errorf("notification = %+v, want success subtitle and the archive ID as message, no sound", got)
	}
}

func TestRunCmd_Notifications_OrphanFailureIncludesCleanupHint(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	ctrl.DeleteSnapshotErr = errBoom // Stage: Merging, >= Snapshotting
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	_ = root.Execute()
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	got := n.Sent[0]
	if got.Subtitle != "myvm backup failed" || got.Sound != notify.FailureSound {
		t.Errorf("notification = %+v, want failure subtitle and sound", got)
	}
	if !strings.Contains(got.Message, "snapback cleanup --vm myvm") {
		t.Errorf("Message = %q, want cleanup hint", got.Message)
	}
}

func TestRunCmd_Notifications_UnknownVMNotifiesWithoutCleanupHint(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	deps := notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, n)
	deps.newController = func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil }
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want unknown-VM error")
	}
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	if strings.Contains(n.Sent[0].Message, "cleanup") {
		t.Errorf("Message = %q, want no cleanup hint before any snapshot was taken", n.Sent[0].Message)
	}
}

func TestRunCmd_Notifications_NotifierErrorWarnsAndKeepsOutcome(t *testing.T) {
	vmxPath := writeVMBundle(t)
	ctrl := vm.NewFakeVMController()
	ctrl.ToolsState = vm.ToolsRunning
	cfg := &config.Config{
		Destination: t.TempDir(), Compression: "gzip",
		VMs:           []config.VM{{Name: "myvm", VMX: vmxPath}},
		Notifications: config.Notifications{Enabled: true, OnSuccess: true},
	}
	n := &notify.Fake{Err: errors.New("osascript exploded")}
	root := newTestRoot(t, notifyTestDeps(t, cfg, ctrl, false, n))
	var errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errOut)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil -- a notification failure must not fail the run", err)
	}
	if !strings.Contains(errOut.String(), "warning: could not send notification: ") || !strings.Contains(errOut.String(), "osascript exploded") {
		t.Errorf("stderr = %q, want a notification warning", errOut.String())
	}
}

func TestRunCmd_Notifications_CanceledContextStillNotifies(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	n := &notify.Fake{}
	root := newTestRoot(t, notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, n))
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = root.ExecuteContext(ctx)
	if len(n.Contexts) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Contexts))
	}
	if err := n.Contexts[0].Err(); err != nil {
		t.Errorf("ctx passed to Notify is already done (%v), want it detached from the command's cancellation", err)
	}
}

func TestRunCmd_Notifications_NilNotifierNeverPanics(t *testing.T) {
	cfg := &config.Config{
		VMs:           []config.VM{{Name: "other-vm"}},
		Notifications: config.Notifications{Enabled: true},
	}
	deps := notifyTestDeps(t, cfg, vm.NewFakeVMController(), false, nil)
	deps.newNotifier = nil
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	_ = root.Execute() // must not panic
}

func TestSnapshotMayRemain(t *testing.T) {
	if snapshotMayRemain(errBoom) {
		t.Error("plain error: got true, want false")
	}
	if snapshotMayRemain(&backup.RunError{Stage: progress.CheckingTools, Err: errBoom}) {
		t.Error("CheckingTools: got true, want false")
	}
	if !snapshotMayRemain(&backup.RunError{Stage: progress.Merging, Err: errBoom}) {
		t.Error("Merging: got false, want true")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/... -run 'Notifications|SnapshotMayRemain' -v`
Expected: FAIL — `newNotifier`, `snapshotMayRemain` undefined.

- [ ] **Step 3: Implement**

In `internal/cli/run.go`:

Add the import `"github.com/xortim/snapback/internal/notify"`.

Add to `runDeps` (and extend its doc comment with: "A nil newNotifier means never notify, so existing runDeps{...} test literals stay valid."):

```go
	newNotifier    func() notify.Notifier
```

In `defaultRunDeps()` add:

```go
		newNotifier: func() notify.Notifier { return notify.NewOSAScript() },
```

Replace `runVM` so the outcome is computed in a helper and notification runs on every path after config load:

```go
func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		// No config means no way to know whether notifications are
		// enabled -- see the run-notifications spec's "covered failure
		// points".
		return err
	}
	result, err := runVMWithConfig(cmd, deps, cfg, configPath, vmName)
	notifyRunOutcome(cmd, deps, cfg, vmName, result, err)
	return err
}

// runVMWithConfig is everything runVM does after config has loaded. It
// returns the backup.Result (nil on failure, or if the run never
// started) so runVM can notify on every return path from one place --
// an early return added here later can't silently skip notification.
func runVMWithConfig(cmd *cobra.Command, deps runDeps, cfg *config.Config, configPath, vmName string) (*backup.Result, error) {
	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return nil, fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	if logPath, err := launchd.LogPath(vmCfg.Name); err == nil {
		if err := launchd.RotateIfOversized(logPath); err != nil {
			// Rotation failing is never a reason to skip the actual
			// backup -- warn and continue, same posture warnIfMaybeOrphaned
			// already takes for a non-fatal, best-effort side channel.
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not rotate log %s: %v\n", logPath, err)
		}
	}

	ctrl, err := deps.newController()
	if err != nil {
		return nil, fmt.Errorf("connect to VM controller: %w", err)
	}

	opts := backup.Options{
		VMName:      vmCfg.Name,
		VMXPath:     vmCfg.VMX,
		Destination: cfg.Destination,
		Compression: cfg.Compression,
	}

	out := cmd.OutOrStdout()

	// (keep the existing comment block about the TUI's own error line here, unchanged)
	var result *backup.Result
	err = dispatchRun(cmd, out, deps.isTerminal, deps.runInteractive, vmName,
		func(ctx context.Context, r progress.Reporter) (*backup.Result, error) {
			res, err := backup.Run(ctx, ctrl, r, opts)
			result = res
			return res, err
		},
		func(err error) { warnIfMaybeOrphaned(cmd, vmName, err) },
		func(res *backup.Result) {
			_, _ = fmt.Fprintf(out, "backup complete: %s\n", res.ArchivePath)
		},
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// notifyRunOutcome sends a macOS notification for a finished run when
// notifications are enabled and nobody is watching (stdout isn't a
// terminal -- e.g. a launchd-scheduled run). Failures always notify;
// successes only when on_success is set. A send failure is a stderr
// warning only: it never changes run's outcome. The notification gets a
// context detached from the command's cancellation so a SIGINT'd run
// still reports its failure. See
// docs/superpowers/specs/2026-10-26-run-notifications-design.md.
func notifyRunOutcome(cmd *cobra.Command, deps runDeps, cfg *config.Config, vmName string, result *backup.Result, runErr error) {
	if deps.newNotifier == nil || !cfg.Notifications.Enabled || isTerminalWriter(deps.isTerminal, cmd.OutOrStdout()) {
		return
	}
	var n notify.Notification
	switch {
	case runErr != nil:
		n = notify.Failure(vmName, runErr, snapshotMayRemain(runErr))
	case !cfg.Notifications.OnSuccess:
		return
	default:
		archiveID := ""
		if result != nil {
			archiveID = result.ArchiveID
		}
		n = notify.Success(vmName, archiveID)
	}
	if err := deps.newNotifier().Notify(context.WithoutCancel(cmd.Context()), n); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not send notification: %v\n", err)
	}
}
```

Replace `warnIfMaybeOrphaned`'s body to use a shared predicate, and add the predicate:

```go
func warnIfMaybeOrphaned(cmd *cobra.Command, vmName string, err error) {
	if snapshotMayRemain(err) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; run `snapback cleanup --vm %s` to remove it\n", vmName, vmName)
	}
}

// snapshotMayRemain reports whether err is a *backup.RunError tagged at
// Stage: Snapshotting or later -- see backup.Run's doc comment for why
// that Stage range means a snapshot may have been left behind. Shared by
// warnIfMaybeOrphaned and notifyRunOutcome so the stderr warning and the
// notification's cleanup hint can never disagree.
func snapshotMayRemain(err error) bool {
	var runErr *backup.RunError
	return errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting
}
```

(Keep `warnIfMaybeOrphaned`'s existing doc comment, trimmed to point at `snapshotMayRemain`.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/cli/... -v`
Expected: PASS, including every pre-existing run test (they leave `newNotifier` nil).

- [ ] **Step 5: Lint, test, commit**

```bash
make lint && make test
git add internal/cli/run.go internal/cli/run_internal_test.go
git commit -m "feat(cli): notify on unattended run success/failure (#86)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `snapback notify test` + `init` tip

**Files:**
- Create: `internal/cli/notify.go`
- Modify: `internal/cli/root.go` (`root.AddCommand(...)` list), `internal/cli/init.go` (end of `runInit`)
- Test: `internal/cli/notify_internal_test.go` (create), `internal/cli/init_internal_test.go`

**Interfaces:**
- Consumes: `notify.Notifier`, `notify.Fake`, `notify.NewOSAScript`, `notify.Notification` (Task 1)
- Produces: `type notifyDeps struct { newNotifier func() notify.Notifier }`, `func newNotifyCmd() *cobra.Command`, `func newNotifyCmdWithDeps(deps notifyDeps) *cobra.Command`, `const notifyTestHint`, `const notifyTip`

- [ ] **Step 1: Write the failing tests**

`internal/cli/notify_internal_test.go`:

```go
package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

func TestNotifyTest_SendsOneNotificationWithoutConfig(t *testing.T) {
	n := &notify.Fake{}
	root := swapSubcommand(t, "notify", newNotifyCmdWithDeps(notifyDeps{newNotifier: func() notify.Notifier { return n }}))
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	var out bytes.Buffer
	root.SetArgs([]string{"notify", "test", "--config", missing})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(n.Sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(n.Sent))
	}
	want := notify.Notification{Title: "snapback", Subtitle: "Test notification", Message: "Notifications are working."}
	if n.Sent[0] != want {
		t.Errorf("notification = %+v, want %+v", n.Sent[0], want)
	}
	if !strings.Contains(out.String(), "Script Editor") {
		t.Errorf("stdout = %q, want the Script Editor permission hint", out.String())
	}
}

func TestNotifyTest_NotifierErrorPropagates(t *testing.T) {
	boom := errors.New("osascript exploded")
	n := &notify.Fake{Err: boom}
	root := swapSubcommand(t, "notify", newNotifyCmdWithDeps(notifyDeps{newNotifier: func() notify.Notifier { return n }}))
	root.SetArgs([]string{"notify", "test"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want it to wrap the notifier error", err)
	}
}
```

Append to `internal/cli/init_internal_test.go`:

```go
func TestInitCmd_NotificationsEnabled_PrintsNotifyTestTip(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var written []byte
		var writtenPath string
		cfg := &config.Config{Destination: "/dest", Compression: "zstd", Notifications: config.Notifications{Enabled: enabled}}
		root := newTestRootForInit(t, fakeInitDeps(nil, false, &written, &writtenPath, cfg))
		var errOut bytes.Buffer
		root.SetArgs([]string{"init", "--config", filepath.Join(t.TempDir(), "config.yaml")})
		root.SetIn(strings.NewReader(""))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errOut)
		if err := root.Execute(); err != nil {
			t.Fatalf("enabled=%v: Execute() error = %v", enabled, err)
		}
		if got := strings.Contains(errOut.String(), "snapback notify test"); got != enabled {
			t.Errorf("enabled=%v: stderr = %q, want tip present=%v", enabled, errOut.String(), enabled)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/... -run 'NotifyTest|NotifyTestTip' -v`
Expected: FAIL — `newNotifyCmdWithDeps` undefined; `swapSubcommand` can't find `notify`.

- [ ] **Step 3: Implement**

`internal/cli/notify.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/notify"
)

// notifyDeps groups `notify test`'s one external dependency so tests can
// substitute notify.Fake for the real osascript-backed notifier.
type notifyDeps struct {
	newNotifier func() notify.Notifier
}

func defaultNotifyDeps() notifyDeps {
	return notifyDeps{newNotifier: func() notify.Notifier { return notify.NewOSAScript() }}
}

// notifyTestHint is printed after a test notification is sent. osascript
// notifications are attributed to Script Editor, and macOS may drop them
// silently until that permission is granted -- which only reliably gets
// prompted for while a person is at the machine, not during a midnight
// launchd run.
const notifyTestHint = "sent a test notification. If nothing appeared, allow notifications for Script Editor in System Settings → Notifications (osascript notifications are shown under Script Editor)."

// notifyTip is what `init` prints when notifications are enabled.
const notifyTip = "tip: run `snapback notify test` now so macOS asks for notification permission while you're here"

func newNotifyCmd() *cobra.Command {
	return newNotifyCmdWithDeps(defaultNotifyDeps())
}

func newNotifyCmdWithDeps(deps notifyDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage macOS notifications",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test notification",
		Long:  "Sends a sample notification regardless of config, so macOS prompts for notification permission while you're at the machine. Scheduled runs can't trigger that prompt reliably on their own.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			n := notify.Notification{Title: "snapback", Subtitle: "Test notification", Message: "Notifications are working."}
			if err := deps.newNotifier().Notify(cmd.Context(), n); err != nil {
				return fmt.Errorf("send test notification: %w", err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), notifyTestHint)
			return err
		},
	})
	return cmd
}
```

`internal/cli/root.go`: add `newNotifyCmd(),` to the `root.AddCommand(` list after `newScheduleCmd(),`.

`internal/cli/init.go`: replace the final two lines of `runInit`

```go
	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
```

with

```go
	if _, err := fmt.Fprintf(out, "wrote config to %s\n", configPath); err != nil {
		return err
	}
	if cfg.Notifications.Enabled {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), notifyTip); err != nil {
			return err
		}
	}
	return nil
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/cli/... -v`
Expected: PASS (check `root_test.go` still passes — if it asserts an exact subcommand list, add `notify` there).

- [ ] **Step 5: Lint, test, commit**

```bash
make lint && make test
git add internal/cli
git commit -m "feat(cli): add notify test command and init permission tip

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Integration test + docs

**Files:**
- Create: `internal/notify/osascript_integration_test.go`
- Modify: `internal/progress/progress.go` (comment on `Notifying`), `docs/design.md` (choreography step 12, Command Reference, Config Reference, Phase 2 paragraph), `CLAUDE.md` (project status paragraph)

**Interfaces:**
- Consumes: `notify.NewOSAScript`, `notify.Notification` (Task 1)

- [ ] **Step 1: Write the integration test**

`internal/notify/osascript_integration_test.go`:

```go
//go:build integration

package notify_test

// Sends one real notification via osascript, per README.md / CLAUDE.md:
//
//	SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
//
// Asserts only that osascript exited cleanly; whether the notification
// actually appeared (Script Editor's notification permission) has to be
// confirmed by eye.

import (
	"context"
	"os"
	"testing"

	"github.com/xortim/snapback/internal/notify"
)

func TestOSAScript_Integration_SendsRealNotification(t *testing.T) {
	if os.Getenv("SNAPBACK_INTEGRATION") != "1" {
		t.Skip("set SNAPBACK_INTEGRATION=1 to run osascript integration tests")
	}
	err := notify.NewOSAScript().Notify(context.Background(), notify.Notification{
		Title: "snapback", Subtitle: "integration test", Message: "x\" & \"injected?", Sound: notify.FailureSound,
	})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
}
```

- [ ] **Step 2: Verify it compiles and skips/passes**

Run: `go vet -tags=integration ./internal/notify/... && go test -tags=integration ./internal/notify/... -run Integration -v`
Expected: SKIP (without `SNAPBACK_INTEGRATION=1`). Do **not** run with `SNAPBACK_INTEGRATION=1` from a subagent — the human runs it manually.

- [ ] **Step 3: Update docs**

`internal/progress/progress.go` — add a comment on the `Notifying` constant line:

```go
	Notifying // reserved: run's notifications are sent by internal/cli after the choreography finishes, not emitted as a stage (docs/superpowers/specs/2026-10-26-run-notifications-design.md)
```

`docs/design.md`:
- Choreography list step 12 → `12. (run command, after the choreography) osascript notification: success/failure — only when stdout isn't a terminal; see the run-notifications spec`.
- Command Reference table: add row `| \`snapback notify test\` | Send a sample notification regardless of config, so macOS prompts for (Script Editor) notification permission while you're at the machine |` after the `schedule sync` row.
- Config Reference YAML: under `notifications:` add `  on_success: true # failures always notify when enabled; defaults to true if omitted`, and one sentence after the `schedule` paragraph: "`notifications.enabled` gates notifications for unattended (non-terminal) `run`s; failures always notify, successes only when `on_success` is true (the default when omitted). Notifications go through `osascript`, so macOS shows them under Script Editor — run `snapback notify test` once to get that permission granted."
- Phase 2 paragraph: replace "osascript success/failure notifications are split out to [#86](...) (`progress.Notifying` is still a stage nothing fires)" with "osascript success/failure notifications were split out to [#86](...) and have since shipped ([spec](superpowers/specs/2026-10-26-run-notifications-design.md))".

`CLAUDE.md` — in the Phase 2 paragraph replace "osascript notifications (split to #86, `progress.Notifying` still fires nothing)" with "osascript notifications (split to #86, since landed: `internal/notify` behind a `Notifier` interface with a `Fake`, sent by `run` only when stdout isn't a terminal, gated by `notifications.enabled`/`on_success`, plus `snapback notify test` for the Script Editor permission prompt)". Add `internal/notify` to the "Real Go source exists under" list.

- [ ] **Step 4: Full verification**

Run: `make lint && make test && make build`
Expected: all succeed.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/osascript_integration_test.go internal/progress/progress.go docs/design.md CLAUDE.md
git commit -m "docs(notify): document run notifications; add osascript integration test

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
