# run TUI Renderer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `snapback run` an interactive bubbletea checklist (per-stage ✓/⚠/✗, a progress bar during Copying/Compressing, an elapsed timer) when stdout is a real terminal, while leaving the existing plain line-by-line output untouched for non-TTY runs (`launchd`-triggered `run --all` has no TTY).

**Architecture:** A new `internal/tui` package owns a bubbletea `Model` plus a `Reporter` that forwards `progress.Event`s (already emitted by `backup.Run`, unchanged) to a running `*tea.Program`. `internal/cli/run.go` gains a TTY check that picks `tui.RunInteractive` or the existing `progress.NewTerminalReporter` path — command wiring is the only place that decision is made, exactly as the spec requires. `internal/tui` depends on `internal/backup` (for `*backup.Result`/`*backup.RunError`, both plain data types) and `internal/progress`, but never the reverse — choreography code still never imports a rendering package.

**Tech Stack:** `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles` (progress bar), `github.com/charmbracelet/lipgloss` (styling), `golang.org/x/term` (TTY detection). Go 1.26.5, existing `cobra`-based CLI.

**Spec:** `docs/superpowers/specs/2026-08-23-cli-ux-design.md` (see its "Implementation status" section, added 2026-09-07, for what's already built vs. what this plan adds)

## Global Constraints

- Choreography code (`internal/backup`) must never import `internal/tui`, `bubbletea`, `lipgloss`, or `bubbles` — it only knows about `progress.Reporter`/`progress.Event`. Do not add any such import there.
- `run --all` / any non-TTY invocation must keep producing exactly the plain output it produces today — no behavior change for `launchd`. This is enforced by treating "not a terminal" (including every existing test's `bytes.Buffer` stdout) as the default.
- No new field on `cleanupDeps` — TTY detection is `run`-specific (`cleanup` never renders progress), so `runDeps` stops being a type alias of `vmCmdDeps` and becomes its own struct. Existing tests construct `runDeps{...}` with keyed fields only, so this is a non-breaking change; do not convert any existing test's struct literal to embed `vmCmdDeps`.
- `restore`'s split-preview TUI and `init`'s `huh` wizard and `status --vm`'s card view are explicitly out of scope for this plan (sequenced as separate follow-on plans per the spec).
- Run `make lint`, `make test`, and `make build` (not ad-hoc `go vet`/`go build`) before every commit that's meant to be a checkpoint.

---

## File Structure

- Create `internal/tui/model.go` — `Model` type, stage list, `stageStatus`/`stageRow`, message types (`eventMsg`, `resultMsg`, `tickMsg`), `Init`/`Update`. Pure state machine, no rendering.
- Create `internal/tui/model_test.go` — tests for `Update` transitions (no `View` assertions).
- Create `internal/tui/view.go` — `Model.View()`, lipgloss styles, progress-bar rendering.
- Create `internal/tui/view_test.go` — tests asserting rendered output contains the right rows/icons/percent/final line.
- Create `internal/tui/reporter.go` — `Reporter` (forwards `progress.Event` to anything satisfying a minimal `Send(tea.Msg)` interface, so it's testable without a real `*tea.Program`).
- Create `internal/tui/reporter_test.go`.
- Create `internal/tui/run.go` — `RunInteractive`, the orchestration function `internal/cli/run.go` calls.
- Create `internal/tui/run_test.go` — integration-style tests running a real (headless) `tea.Program` end to end.
- Modify `internal/cli/deps.go` — remove the `type runDeps = vmCmdDeps` alias line (keep `cleanupDeps` as-is).
- Modify `internal/cli/run.go` — new `runDeps` struct (its own type, not an alias), `defaultRunDeps()`, `defaultIsTerminal()`, `runVM` branches on `deps.isTerminal`.
- Modify `internal/cli/run_internal_test.go` — add tests covering the interactive branch.

---

### Task 1: Add TUI dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:** None (dependency-only task).

- [ ] **Step 1: Fetch the dependencies**

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get golang.org/x/term@latest
```

Do **not** run `go mod tidy` here: nothing imports these packages yet, and `tidy` would prune any requirement no current import needs, stripping the very dependencies this step just added right back out. Leave that to Task 8, once every package actually uses what it imports.

- [ ] **Step 2: Verify the build still works with no code changes yet**

Run: `go build ./...`
Expected: succeeds with no errors. `go.mod` now lists all four as direct requirements (via `go get`) even though nothing imports them yet -- that's expected and fine at this point.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add bubbletea, bubbles, lipgloss, x/term dependencies"
```

---

### Task 2: `internal/tui` state machine (Model, Update, no rendering)

**Files:**
- Create: `internal/tui/model.go`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `progress.Stage`, `progress.Event` (from `internal/progress`, unchanged); `*backup.Result`, `*backup.RunError` (from `internal/backup`, unchanged).
- Produces (used by Task 3's `view.go` and Task 4's `reporter.go`, same package so unexported is fine):
  - `type Model struct{ vmName string; cancel context.CancelFunc; rows []stageRow; percent float64; showBar bool; start time.Time; elapsed time.Duration; result *backup.Result; err error; finished bool; cancelling bool }`
  - `type stageStatus int` with consts `pending, active, done, failed`
  - `type stageRow struct{ stage progress.Stage; status stageStatus; message string }`
  - `var stages []progress.Stage` (the fixed 6-stage display order)
  - `func newModel(vmName string, cancel context.CancelFunc) Model`
  - `type eventMsg progress.Event`
  - `type resultMsg struct{ result *backup.Result; err error }`
  - `type tickMsg time.Time`
  - `func (m Model) Init() tea.Cmd`
  - `func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/model_test.go`:

```go
package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// errBoom is a shared sentinel error, reused by view_test.go (same
// package) wherever a test just needs some non-nil error, not a
// specific one.
var errBoom = errors.New("boom")

func TestNewModel_StartsAllRowsPending(t *testing.T) {
	m := newModel("myvm", func() {})
	if len(m.rows) != len(stages) {
		t.Fatalf("len(rows) = %d, want %d", len(m.rows), len(stages))
	}
	for _, row := range m.rows {
		if row.status != pending {
			t.Errorf("stage %v status = %v, want pending", row.stage, row.status)
		}
	}
}

func TestUpdate_EventMsg_MarksActiveAndPriorStagesDone(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot x"}))
	m = updated.(Model)

	for _, row := range m.rows {
		switch row.stage {
		case progress.CheckingTools:
			if row.status != done {
				t.Errorf("CheckingTools status = %v, want done", row.status)
			}
		case progress.Snapshotting:
			if row.status != active {
				t.Errorf("Snapshotting status = %v, want active", row.status)
			}
			if row.message != "taking snapshot x" {
				t.Errorf("Snapshotting message = %q, want %q", row.message, "taking snapshot x")
			}
		default:
			if row.status != pending {
				t.Errorf("%v status = %v, want pending", row.stage, row.status)
			}
		}
	}
}

func TestUpdate_EventMsg_PercentOnlyUpdatesBarWithoutClearingMessage(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Percent: 0.42}))
	m = updated.(Model)

	if !m.showBar {
		t.Error("showBar = false, want true after a Percent-bearing Copying event")
	}
	if m.percent != 0.42 {
		t.Errorf("percent = %v, want 0.42", m.percent)
	}
	for _, row := range m.rows {
		if row.stage == progress.Copying && row.message != "copying VM bundle to staging" {
			t.Errorf("Copying message = %q, want it preserved across the percent-only event", row.message)
		}
	}
}

func TestUpdate_ResultMsg_Success_MarksAllRowsDone(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, cmd := m.Update(resultMsg{result: &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}})
	m = updated.(Model)

	if !m.finished {
		t.Error("finished = false, want true after resultMsg")
	}
	if cmd == nil {
		t.Error("Update(resultMsg) returned a nil cmd, want tea.Quit")
	}
	for _, row := range m.rows {
		if row.status != done {
			t.Errorf("stage %v status = %v, want done after a successful result", row.stage, row.status)
		}
	}
}

func TestUpdate_ResultMsg_Failure_MarksMatchingStageFailed(t *testing.T) {
	m := newModel("myvm", func() {})
	// Advance to Merging first, as a real run would.
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"}))
	m = updated.(Model)

	runErr := &backup.RunError{Stage: progress.Merging, Err: errors.New("delete snapshot: boom")}
	updated, _ = m.Update(resultMsg{err: runErr})
	m = updated.(Model)

	for _, row := range m.rows {
		switch row.stage {
		case progress.CheckingTools, progress.Snapshotting, progress.Copying:
			if row.status != done {
				t.Errorf("%v status = %v, want done (already completed before the failure)", row.stage, row.status)
			}
		case progress.Merging:
			if row.status != failed {
				t.Errorf("Merging status = %v, want failed", row.status)
			}
			if row.message != "delete snapshot: boom" {
				t.Errorf("Merging message = %q, want the RunError text", row.message)
			}
		default:
			if row.status != pending {
				t.Errorf("%v status = %v, want pending", row.stage, row.status)
			}
		}
	}
}

func TestUpdate_ResultMsg_FailureBeforeAnyDisplayedStage_MarksFirstRowFailed(t *testing.T) {
	m := newModel("myvm", func() {})
	// ctx canceled before Run ever reported CheckingTools.
	runErr := &backup.RunError{Stage: progress.CheckingTools, Err: context.Canceled}
	updated, _ := m.Update(resultMsg{err: runErr})
	m = updated.(Model)

	if m.rows[0].status != failed {
		t.Errorf("rows[0].status = %v, want failed", m.rows[0].status)
	}
}

func TestUpdate_CtrlC_CallsCancelAndSetsCancelling(t *testing.T) {
	var canceled bool
	m := newModel("myvm", func() { canceled = true })

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if !canceled {
		t.Error("cancel was not called on ctrl+c")
	}
	if !m.cancelling {
		t.Error("cancelling = false, want true after ctrl+c")
	}
}

func TestUpdate_CtrlC_AfterFinished_DoesNotCallCancel(t *testing.T) {
	var canceled bool
	m := newModel("myvm", func() { canceled = true })
	updated, _ := m.Update(resultMsg{result: &backup.Result{}})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if canceled {
		t.Error("cancel was called after the run already finished, want no-op")
	}
}

func TestUpdate_Tick_AdvancesElapsedAndReschedules(t *testing.T) {
	m := newModel("myvm", func() {})
	m.start = time.Now().Add(-5 * time.Second)

	updated, cmd := m.Update(tickMsg(time.Now()))
	m = updated.(Model)

	if m.elapsed < 4*time.Second {
		t.Errorf("elapsed = %v, want >= ~5s", m.elapsed)
	}
	if cmd == nil {
		t.Error("Update(tickMsg) returned a nil cmd, want another tick scheduled")
	}
}

func TestUpdate_Tick_AfterFinished_DoesNotReschedule(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: &backup.Result{}})
	m = updated.(Model)

	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd != nil {
		t.Error("Update(tickMsg) after finished returned a non-nil cmd, want nil (no more ticks)")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -v`
Expected: FAIL — package `tui` doesn't exist yet (build failure naming `Model`, `newModel`, etc.).

- [ ] **Step 3: Write the implementation**

Create `internal/tui/model.go`:

```go
// Package tui renders backup.Run's progress as an interactive bubbletea
// checklist for a real terminal, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md. It depends on
// internal/progress (the Event vocabulary) and internal/backup (only for
// the plain *backup.Result/*backup.RunError data types) -- never the
// reverse. Choreography code stays free of any rendering import.
package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// stages is the fixed, display-order subset of progress.Stage values
// backup.Run actually reports today (Pruning/Notifying exist as Stage
// constants for future phases but aren't emitted yet, so they're left
// off this checklist rather than shown permanently pending).
var stages = []progress.Stage{
	progress.CheckingTools,
	progress.Snapshotting,
	progress.Copying,
	progress.Merging,
	progress.Compressing,
	progress.Checksumming,
}

type stageStatus int

const (
	pending stageStatus = iota
	active
	done
	failed
)

type stageRow struct {
	stage   progress.Stage
	status  stageStatus
	message string
}

// Model is a bubbletea model rendering one run's progress. Exported so
// internal/cli/run.go's tests can reference it if needed, though normal
// callers only interact with it via RunInteractive.
type Model struct {
	vmName     string
	cancel     context.CancelFunc
	rows       []stageRow
	percent    float64
	showBar    bool
	start      time.Time
	elapsed    time.Duration
	result     *backup.Result
	err        error
	finished   bool
	cancelling bool
}

func newModel(vmName string, cancel context.CancelFunc) Model {
	rows := make([]stageRow, len(stages))
	for i, s := range stages {
		rows[i] = stageRow{stage: s, status: pending}
	}
	return Model{
		vmName: vmName,
		cancel: cancel,
		rows:   rows,
		start:  time.Now(),
	}
}

type eventMsg progress.Event

type resultMsg struct {
	result *backup.Result
	err    error
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC && !m.finished {
			m.cancelling = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	case tickMsg:
		if m.finished {
			return m, nil
		}
		m.elapsed = time.Since(m.start)
		return m, tickCmd()
	case eventMsg:
		m.applyEvent(progress.Event(msg))
		return m, nil
	case resultMsg:
		m.result = msg.result
		m.err = msg.err
		m.finished = true
		m.applyFinalStatus()
		return m, tea.Quit
	}
	return m, nil
}

// applyEvent updates rows in place for a live progress.Event: every
// stage before e.Stage in the fixed display order is marked done (a
// stage that already finished, since events arrive in pipeline order),
// e.Stage itself becomes active, and a Percent-bearing event (the
// per-file ticks during Copying/Compressing) updates the bar without
// clearing that stage's last Message.
func (m *Model) applyEvent(e progress.Event) {
	idx := -1
	for i, row := range m.rows {
		if row.stage == e.Stage {
			idx = i
			break
		}
	}
	if idx == -1 {
		// Stage not in the displayed checklist (e.g. Done) -- nothing to
		// update here; resultMsg drives final-state rendering instead.
		return
	}
	for i := 0; i < idx; i++ {
		if m.rows[i].status != failed {
			m.rows[i].status = done
		}
	}
	m.rows[idx].status = active
	if e.Message != "" {
		m.rows[idx].message = e.Message
	}
	if e.Stage == progress.Copying || e.Stage == progress.Compressing {
		m.percent = e.Percent
		m.showBar = true
	}
}

// applyFinalStatus marks every row done (success) or the row matching
// the failing RunError's Stage as failed (leaving earlier rows done, so
// the failure is legible in context per the spec), called once when
// resultMsg arrives.
func (m *Model) applyFinalStatus() {
	if m.err == nil {
		for i := range m.rows {
			m.rows[i].status = done
		}
		return
	}
	var runErr *backup.RunError
	if errors.As(m.err, &runErr) {
		for i := range m.rows {
			if m.rows[i].stage == runErr.Stage {
				m.rows[i].status = failed
				m.rows[i].message = runErr.Err.Error()
				return
			}
		}
	}
	if len(m.rows) > 0 {
		m.rows[0].status = failed
		m.rows[0].message = m.err.Error()
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test in `model_test.go`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): add run checklist state machine"
```

---

### Task 3: `internal/tui` rendering (View)

**Files:**
- Create: `internal/tui/view.go`
- Test: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: `Model` (Task 2), `progress.Stage.String()` (already exists in `internal/progress`).
- Produces: `func (m Model) View() string` (required by `tea.Model`; also used directly by tests).

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/view_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestView_PendingStageShowsCircleIcon(t *testing.T) {
	m := newModel("myvm", func() {})
	view := m.View()
	if !strings.Contains(view, "○ checking tools") {
		t.Errorf("view = %q, want a pending-icon row for checking tools", view)
	}
}

func TestView_ActiveStageShowsMessage(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Snapshotting, Message: "taking snapshot snapback-x"}))
	m = updated.(Model)
	view := m.View()
	if !strings.Contains(view, "checking tools") {
		t.Errorf("view = %q, want the prior stage still listed", view)
	}
	if !strings.Contains(view, "snapshotting - taking snapshot snapback-x") {
		t.Errorf("view = %q, want the active stage's message shown", view)
	}
}

func TestView_CopyingWithPercent_ShowsProgressBar(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Copying, Message: "copying VM bundle to staging"}))
	m = updated.(Model)
	updated, _ = m.Update(eventMsg(progress.Event{Stage: progress.Copying, Percent: 0.5}))
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "50%") {
		t.Errorf("view = %q, want a rendered progress percentage", view)
	}
}

func TestView_Success_ShowsArchivePath(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(resultMsg{result: &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "backup complete: /dest/myvm-x/archive.tar.zst") {
		t.Errorf("view = %q, want the archive path in the completion line", view)
	}
	if !strings.Contains(view, "✓ checksumming") {
		t.Errorf("view = %q, want the last stage checked off on success", view)
	}
}

func TestView_Failure_ShowsErrorAndCrossIcon(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(eventMsg(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"}))
	m = updated.(Model)
	updated, _ = m.Update(resultMsg{err: &backup.RunError{Stage: progress.Merging, Err: errBoom}})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "✗ merging") {
		t.Errorf("view = %q, want merging marked failed", view)
	}
	if !strings.Contains(view, "error:") {
		t.Errorf("view = %q, want an error summary line", view)
	}
}

func TestView_Cancelling_ShowsCancellingNotice(t *testing.T) {
	m := newModel("myvm", func() {})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "cancelling") {
		t.Errorf("view = %q, want a cancelling notice", view)
	}
}
```

`TestView_Failure_ShowsErrorAndCrossIcon` above reuses the package-level `errBoom` variable already declared in `internal/tui/model_test.go` (added in Task 2) -- no new fixture needed here.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -v -run TestView`
Expected: FAIL — `Model.View` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/view.go`:

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	bprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/xortim/snapback/internal/progress"
)

// Palette per docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// semantic color table. Yellow (crash-consistent tools state) isn't used
// for stage rows here -- progress.Event doesn't carry tools_state, only
// the manifest does after a run completes -- so it's reserved for the
// cancelling notice instead, which is a real "degraded, not failed"
// signal available today.
var (
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff"))
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149"))
	pendingStyle = lipgloss.NewStyle().Faint(true)
	noticeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341"))

	barWidth = 40
)

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "snapback run --vm %s\n\n", m.vmName)

	for _, row := range m.rows {
		b.WriteString(renderRow(row))
		b.WriteString("\n")
		if row.status == active && m.showBar && (row.stage == progress.Copying || row.stage == progress.Compressing) {
			bar := bprogress.New(bprogress.WithDefaultGradient())
			bar.Width = barWidth
			b.WriteString("  " + bar.ViewAs(m.percent) + "\n")
		}
	}

	fmt.Fprintf(&b, "\nelapsed: %s\n", m.elapsed.Round(time.Second))

	if m.cancelling && !m.finished {
		b.WriteString(noticeStyle.Render("cancelling... (waiting for the current step to finish)") + "\n")
	}
	if m.finished {
		if m.err == nil {
			b.WriteString(doneStyle.Render(fmt.Sprintf("backup complete: %s", m.result.ArchivePath)) + "\n")
		} else {
			b.WriteString(failStyle.Render(fmt.Sprintf("error: %v", m.err)) + "\n")
		}
	}
	return b.String()
}

func renderRow(row stageRow) string {
	icon, style := iconFor(row.status)
	line := icon + " " + row.stage.String()
	if row.message != "" {
		line += " - " + row.message
	}
	return style.Render(line)
}

func iconFor(s stageStatus) (string, lipgloss.Style) {
	switch s {
	case done:
		return "✓", doneStyle
	case active:
		return "◐", activeStyle
	case failed:
		return "✗", failStyle
	default:
		return "○", pendingStyle
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test in both `model_test.go` and `view_test.go`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/view.go internal/tui/view_test.go internal/tui/model_test.go
git commit -m "feat(tui): render run checklist view"
```

---

### Task 4: `internal/tui` Reporter

**Files:**
- Create: `internal/tui/reporter.go`
- Test: `internal/tui/reporter_test.go`

**Interfaces:**
- Consumes: `progress.Reporter`, `progress.Event` (from `internal/progress`).
- Produces: `type Reporter struct{...}`, `func NewReporter(s Sender) Reporter`, `type Sender interface{ Send(tea.Msg) }`, `func (r Reporter) Report(e progress.Event)` (implements `progress.Reporter`).

- [ ] **Step 1: Write the failing test**

Create `internal/tui/reporter_test.go`:

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

type fakeSender struct {
	sent []tea.Msg
}

func (f *fakeSender) Send(msg tea.Msg) {
	f.sent = append(f.sent, msg)
}

func TestReporter_Report_ForwardsAsEventMsg(t *testing.T) {
	fake := &fakeSender{}
	r := NewReporter(fake)

	var _ progress.Reporter = r // compile-time interface check

	r.Report(progress.Event{Stage: progress.Copying, Percent: 0.75})

	if len(fake.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(fake.sent))
	}
	got, ok := fake.sent[0].(eventMsg)
	if !ok {
		t.Fatalf("sent message type = %T, want eventMsg", fake.sent[0])
	}
	if got.Stage != progress.Copying || got.Percent != 0.75 {
		t.Errorf("sent eventMsg = %+v, want Stage=Copying Percent=0.75", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui/... -run TestReporter -v`
Expected: FAIL — `Reporter`, `NewReporter`, `Sender` don't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/reporter.go`:

```go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/progress"
)

// Sender is the one method of *tea.Program that Reporter needs --
// defined as an interface (rather than depending on *tea.Program
// directly) so tests can inject a fake instead of running a real
// bubbletea program just to exercise Report.
type Sender interface {
	Send(tea.Msg)
}

// Reporter forwards progress.Events to a running bubbletea program.
// Report is called from whatever goroutine backup.Run executes on --
// separate from the TUI's own event-loop goroutine -- and
// (*tea.Program).Send is documented as safe to call from any goroutine,
// including after the program has already quit (a no-op in that case).
type Reporter struct {
	sender Sender
}

// NewReporter returns a Reporter that forwards every Report call to s.
func NewReporter(s Sender) Reporter {
	return Reporter{sender: s}
}

// Report implements progress.Reporter.
func (r Reporter) Report(e progress.Event) {
	r.sender.Send(eventMsg(e))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test so far.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/reporter.go internal/tui/reporter_test.go
git commit -m "feat(tui): add bubbletea-backed progress.Reporter"
```

---

### Task 5: `internal/tui` orchestration (RunInteractive)

**Files:**
- Create: `internal/tui/run.go`
- Test: `internal/tui/run_test.go`

**Interfaces:**
- Consumes: `newModel`, `Reporter`/`NewReporter` (this package), `progress.Reporter`, `*backup.Result`.
- Produces: `func RunInteractive(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error), extraOpts ...tea.ProgramOption) (*backup.Result, error)` — this is what `internal/cli/run.go` (Task 6) calls.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/run_test.go`:

```go
package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

func TestRunInteractive_Success_ReturnsResult(t *testing.T) {
	var out bytes.Buffer
	want := &backup.Result{ArchivePath: "/dest/myvm-x/archive.tar.zst"}

	backupFn := func(r progress.Reporter) (*backup.Result, error) {
		r.Report(progress.Event{Stage: progress.CheckingTools, Message: "checking VMware Tools state"})
		r.Report(progress.Event{Stage: progress.Done, Message: "backup complete"})
		return want, nil
	}

	got, err := RunInteractive(&out, "myvm", func() {}, backupFn, tea.WithInput(strings.NewReader("")))
	if err != nil {
		t.Fatalf("RunInteractive() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("RunInteractive() result = %v, want %v", got, want)
	}
	if !strings.Contains(out.String(), "backup complete: /dest/myvm-x/archive.tar.zst") {
		t.Errorf("output = %q, want the completion line rendered", out.String())
	}
}

func TestRunInteractive_Failure_ReturnsError(t *testing.T) {
	var out bytes.Buffer
	wantErr := &backup.RunError{Stage: progress.Merging, Err: errors.New("delete snapshot: boom")}

	backupFn := func(r progress.Reporter) (*backup.Result, error) {
		r.Report(progress.Event{Stage: progress.Merging, Message: "merging snapshot back"})
		return nil, wantErr
	}

	_, err := RunInteractive(&out, "myvm", func() {}, backupFn, tea.WithInput(strings.NewReader("")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunInteractive() error = %v, want it to be (or wrap) %v", err, wantErr)
	}
	if !strings.Contains(out.String(), "✗ merging") {
		t.Errorf("output = %q, want merging marked failed", out.String())
	}
}

```

`run_test.go` deliberately has no ctrl+c/cancellation test: `RunInteractive` has no way to synthesize a keypress into a headless `tea.Program` deterministically, and that behavior is already fully covered at the right layer by Task 2's `TestUpdate_CtrlC_CallsCancelAndSetsCancelling` (`Model.Update` is where the cancel logic actually lives).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run TestRunInteractive -v`
Expected: FAIL — `RunInteractive` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/run.go`:

```go
package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/progress"
)

// RunInteractive renders backupFn's progress as an interactive checklist
// written to out, and returns whatever backupFn returns. cancel is
// invoked if the user presses ctrl+c before the run finishes -- the
// caller is responsible for wiring cancel to the same context.Context
// backupFn's underlying backup.Run call actually respects (run.go does
// this via context.WithCancel(cmd.Context())). extraOpts is exposed
// purely for tests, to pass tea.WithInput on a non-terminal reader;
// production callers should leave it empty so bubbletea reads real
// keypresses (ctrl+c) from the real stdin.
func RunInteractive(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error), extraOpts ...tea.ProgramOption) (*backup.Result, error) {
	opts := append([]tea.ProgramOption{tea.WithOutput(out)}, extraOpts...)
	program := tea.NewProgram(newModel(vmName, cancel), opts...)
	reporter := NewReporter(program)

	go func() {
		result, err := backupFn(reporter)
		program.Send(resultMsg{result: result, err: err})
	}()

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}
	m, ok := finalModel.(Model)
	if !ok {
		return nil, fmt.Errorf("unexpected model type %T from bubbletea program", finalModel)
	}
	return m.result, m.err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/run.go internal/tui/run_test.go
git commit -m "feat(tui): add RunInteractive orchestration"
```

---

### Task 6: Wire `run` to the TTY check

**Files:**
- Modify: `internal/cli/deps.go`
- Modify: `internal/cli/run.go`

**Interfaces:**
- Consumes: `tui.RunInteractive`, `tui.Reporter`/`NewReporter` (indirectly, via `RunInteractive`), `progress.NewTerminalReporter` (existing), `backup.Run` (existing, unchanged signature).
- Produces: `type runDeps struct{ loadConfig ...; newController ...; isTerminal func(io.Writer) bool; runInteractive func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) }`, `func defaultRunDeps() runDeps`, `func defaultIsTerminal(w io.Writer) bool`.

- [ ] **Step 1: Remove the now-stale alias**

In `internal/cli/deps.go`, delete this line (keep everything else, including `cleanupDeps`):

```go
type runDeps = vmCmdDeps
```

Also update the comment directly above it (currently describing both `runDeps` and `cleanupDeps` as aliases) to describe only `cleanupDeps`:

```go
// cleanupDeps is an alias (not a distinct type) for vmCmdDeps -- cleanup
// needs exactly vmCmdDeps's shape and nothing more. runDeps used to be
// the same alias, but run.go now needs extra fields (isTerminal,
// runInteractive) that cleanup has no use for, so runDeps is its own
// struct defined in run.go.
type cleanupDeps = vmCmdDeps
```

- [ ] **Step 2: Run the build to confirm the expected breakage**

Run: `go build ./...`
Expected: FAIL — `internal/cli/run.go` and `internal/cli/run_internal_test.go` reference `runDeps` which no longer resolves to anything (compile error: `undefined: runDeps`). This confirms Step 1 actually removed the alias.

- [ ] **Step 3: Rewrite `internal/cli/run.go`**

Replace the full contents of `internal/cli/run.go` with:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/progress"
	"github.com/xortim/snapback/internal/tui"
	"github.com/xortim/snapback/internal/vm"
)

// runDeps groups run's external dependencies. It shares loadConfig and
// newController's shape with cleanupDeps/vmCmdDeps (deps.go) but is its
// own struct, not an alias -- run also needs isTerminal (decides
// interactive vs. plain rendering) and runInteractive (the interactive
// renderer itself), neither of which cleanup has any use for. A nil
// isTerminal is treated as "not a terminal": every existing test's
// runDeps{...} literal leaves it unset, and every existing test's
// stdout is a *bytes.Buffer (never a real terminal) anyway, so this
// keeps all of those tests exercising the plain-output path unchanged.
type runDeps struct {
	loadConfig     func(path string) (*config.Config, error)
	newController  func() (vm.Controller, error)
	isTerminal     func(w io.Writer) bool
	runInteractive func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error)
}

// defaultIsTerminal reports whether w is a real terminal. Only *os.File
// can be a terminal; any other io.Writer (a *bytes.Buffer in tests, a
// pipe, a redirected-to-file launchd invocation) is not.
func defaultIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func defaultRunDeps() runDeps {
	base := defaultVMCmdDeps()
	return runDeps{
		loadConfig:    base.loadConfig,
		newController: base.newController,
		isTerminal:    defaultIsTerminal,
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return tui.RunInteractive(out, vmName, cancel, backupFn)
		},
	}
}

func newRunCmd() *cobra.Command {
	return newRunCmdWithDeps(defaultRunDeps())
}

func newRunCmdWithDeps(deps runDeps) *cobra.Command {
	var vmName string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation (e.g. the required --vm flag) runs before RunE,
			// so this only suppresses usage for errors runVM itself returns --
			// flag-misuse errors still print usage.
			cmd.SilenceUsage = true
			return runVM(cmd, deps, vmName)
		},
	}
	addRequiredVMFlag(cmd, &vmName, "name of the VM to back up, as configured")

	return cmd
}

func runVM(cmd *cobra.Command, deps runDeps, vmName string) error {
	cfg, configPath, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}

	vmCfg, ok := findVMConfig(cfg.VMs, vmName)
	if !ok {
		return fmt.Errorf("no VM named %q in config %s", vmName, configPath)
	}

	ctrl, err := deps.newController()
	if err != nil {
		return fmt.Errorf("connect to VM controller: %w", err)
	}

	opts := backup.Options{
		VMName:      vmCfg.Name,
		VMXPath:     vmCfg.VMX,
		Comment:     vmCfg.CommentTemplate,
		Destination: cfg.Destination,
		Compression: cfg.Compression,
	}

	out := cmd.OutOrStdout()

	if deps.isTerminal != nil && deps.isTerminal(out) && deps.runInteractive != nil {
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		backupFn := func(r progress.Reporter) (*backup.Result, error) {
			return backup.Run(ctx, ctrl, r, opts)
		}
		_, err := deps.runInteractive(out, vmName, cancel, backupFn)
		if err != nil {
			warnIfMaybeOrphaned(cmd, vmName, err)
			return err
		}
		return nil
	}

	reporter := progress.NewTerminalReporter(out)
	result, err := backup.Run(cmd.Context(), ctrl, reporter, opts)
	if err != nil {
		warnIfMaybeOrphaned(cmd, vmName, err)
		return err
	}

	_, _ = fmt.Fprintf(out, "backup complete: %s\n", result.ArchivePath)
	return nil
}

// warnIfMaybeOrphaned prints a pointer to `snapback cleanup` on stderr
// if err is a *backup.RunError tagged at Stage: Snapshotting or later --
// see backup.Run's doc comment for why that Stage range specifically
// means a snapshot may have been left behind on the source VM.
func warnIfMaybeOrphaned(cmd *cobra.Command, vmName string, err error) {
	var runErr *backup.RunError
	if errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: a snapshot may remain on %q; run `snapback cleanup --vm %s` to remove it\n", vmName, vmName)
	}
}

func findVMConfig(vms []config.VM, name string) (config.VM, bool) {
	for _, v := range vms {
		if v.Name == name {
			return v, true
		}
	}
	return config.VM{}, false
}
```

Note: `warnIfMaybeOrphaned` is a straight extraction of the duplicate `errors.As(err, &runErr) && runErr.Stage >= progress.Snapshotting` check the old code had inline once; now that both branches (interactive and plain) need it, it's a shared helper instead of copy-pasted twice.

- [ ] **Step 4: Run existing run tests to confirm nothing broke**

Run: `go test ./internal/cli/... -run TestRunCmd -v`
Expected: PASS for every existing test (`TestRunCmd_MissingVMFlag_ReturnsError`, `TestRunCmd_DependencyError_IsWrappedAndSuppressesUsage`, `TestRunCmd_ConfigLoadError_DoesNotDuplicatePath`, `TestRunCmd_ConfigLoadError_MalformedYAML_NamesPath`, `TestRunCmd_UnknownVMName_ReturnsError`, `TestRunCmd_HappyPath_PrintsArchivePath`, `TestRunCmd_MergeFailure_WarnsAboutPossibleOrphanOnStderr`) -- none of their `runDeps{...}` literals set `isTerminal`, so they all still take the plain-output branch exactly as before.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/deps.go internal/cli/run.go
git commit -m "feat(cli): wire run to the interactive TUI on a real terminal"
```

---

### Task 7: Test the interactive branch in `internal/cli`

**Files:**
- Modify: `internal/cli/run_internal_test.go`

**Interfaces:** Consumes `runDeps.isTerminal`, `runDeps.runInteractive` (Task 6).

- [ ] **Step 1: Write the failing tests**

Add to `internal/cli/run_internal_test.go` (place after `TestRunCmd_HappyPath_PrintsArchivePath`, since these are close cousins of it):

```go
func TestRunCmd_InteractiveTerminal_UsesRunInteractiveAndSkipsPlainPrint(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	var calledWithVMName string
	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return true },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			calledWithVMName = vmName
			return backupFn(progress.NoOpReporter{})
		},
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if calledWithVMName != "myvm" {
		t.Errorf("runInteractive called with vmName = %q, want %q", calledWithVMName, "myvm")
	}
	if strings.Contains(out.String(), "backup complete:") {
		t.Errorf("stdout = %q, want no plain \"backup complete\" line -- the interactive renderer owns that", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
}

func TestRunCmd_InteractiveTerminal_MergeFailure_WarnsAboutPossibleOrphanOnStderr(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning
	fake.DeleteSnapshotErr = errBoom

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		isTerminal:    func(io.Writer) bool { return true },
		runInteractive: func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error) {
			return backupFn(progress.NoOpReporter{})
		},
	})
	var out, errOut bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&errOut)

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want the wrapped delete-snapshot failure")
	}
	if !strings.Contains(errOut.String(), "may remain") {
		t.Errorf("stderr = %q, want an orphaned-snapshot warning", errOut.String())
	}
}

func TestRunCmd_NilIsTerminal_UsesPlainOutput(t *testing.T) {
	vmxPath := writeVMBundle(t)
	fake := vm.NewFakeVMController()
	fake.ToolsState = vm.ToolsRunning

	root := newTestRoot(t, runDeps{
		loadConfig: func(path string) (*config.Config, error) {
			return &config.Config{
				Destination: t.TempDir(),
				Compression: "gzip",
				VMs:         []config.VM{{Name: "myvm", VMX: vmxPath}},
			}, nil
		},
		newController: func() (vm.Controller, error) { return fake, nil },
		// isTerminal and runInteractive both left nil.
	})
	var out bytes.Buffer
	root.SetArgs([]string{"run", "--vm", "myvm"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "backup complete:") {
		t.Errorf("stdout = %q, want the plain completion line when isTerminal is nil", out.String())
	}
}
```

Add the new imports these tests need to `run_internal_test.go`'s existing `import` block: `"context"` and `"io"` (for the `runInteractive`/`isTerminal` fakes' signatures), plus `"github.com/xortim/snapback/internal/backup"` and `"github.com/xortim/snapback/internal/progress"` (for `*backup.Result` and `progress.Reporter`/`progress.NoOpReporter`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run TestRunCmd -v`
Expected: The three new tests FAIL (or fail to compile, if `runDeps` doesn't yet have `isTerminal`/`runInteractive` -- it will, from Task 6, so this step should actually show these three passing already if Task 6 is done first). If Tasks 6 and 7 are executed in strict order as written, this step is really a confirmation step, not a red step -- run it anyway and confirm all `TestRunCmd_*` tests pass, old and new alike.

- [ ] **Step 3: Run the full test suite for this package**

Run: `go test ./internal/cli/... -v`
Expected: PASS, all tests.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/run_internal_test.go
git commit -m "test(cli): cover run's interactive-vs-plain branch"
```

---

### Task 8: Final verification

**Files:** `go.mod`, `go.sum` (possibly, via `go mod tidy`).

- [ ] **Step 1: Tidy go.mod now that everything added in Task 1 is actually imported**

```bash
go mod tidy
git diff go.mod go.sum
```

Expected: no diff, or a trivial one (e.g. an indirect-dependency comment shuffle) -- every package `go get` added in Task 1 is imported by now (`internal/tui`, `internal/cli`), so `tidy` should have nothing meaningful to prune. If it removes one of the four intentionally-added requirements, stop and figure out why before continuing -- it means some task's code doesn't actually import what it was supposed to.

- [ ] **Step 2: Run the full lint/test/build suite**

Run: `make lint`
Expected: `0 issues.`

Run: `make test`
Expected: all packages PASS, including the new `internal/tui` package.

Run: `make build`
Expected: succeeds, produces `dist/<goos-goarch>/snapback`.

- [ ] **Step 3: Note the manual-verification gap honestly**

There is no real VMware Fusion VM available in this environment to smoke-test the actual interactive checklist against a live backup end to end (production wiring always uses the real `vm.NewVMCLIController`, which shells out to `vmcli`/`vmrun`; CLAUDE.md gates that behind `SNAPBACK_INTEGRATION=1` and a scratch VM the user runs manually). Automated coverage here is `internal/tui`'s own tests (state machine, rendering, and a headless `tea.Program` integration test) plus `internal/cli`'s branch-selection tests -- not a real terminal render. Record this plainly rather than claiming a UI verification that didn't happen; the user should do one real `snapback run --vm <name>` at a real terminal against their actual config once this lands, to confirm the rendering looks right in practice (terminal width, color rendering, etc. -- exactly the two open risks the spec itself already flagged).

- [ ] **Step 4: Push the branch and open the PR**

```bash
git push -u origin feat/run-tui-renderer
gh pr create --title "feat(cli): interactive TUI checklist for run" --body "$(cat <<'EOF'
## Summary
- Adds internal/tui: a bubbletea Model rendering run's per-stage checklist (checking tools/snapshotting/copying/merging/compressing/checksumming), a progress bar during Copying/Compressing, and an elapsed timer, styled per docs/superpowers/specs/2026-08-23-cli-ux-design.md's semantic palette
- internal/cli/run.go now picks the interactive renderer when stdout is a real terminal (golang.org/x/term), falling back to the existing plain line-by-line output otherwise -- launchd-triggered `run --all` (no TTY) is unaffected
- ctrl+c cancels the in-flight backup.Run via context, same orphaned-snapshot semantics as today (backup.RunError.Stage >= Snapshotting still triggers the `snapback cleanup` pointer on stderr)

Implements the `run` slice of issue #17 (init's huh wizard and status --vm's card view are separate follow-on PRs, per the agreed build order).

## Test plan
- [x] `make lint`
- [x] `make test`
- [x] `make build`
- [ ] Manual: run `snapback run --vm <name>` at a real terminal against a real config, confirm the checklist/progress bar/colors render as expected -- not done in this environment (no real Fusion VM available here); needs a pass by the user
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** `run`'s checklist, progress bar (Copying/Compressing), elapsed timer, semantic palette (blue/green/red used; yellow repurposed for the cancelling notice since `Event` doesn't carry `tools_state` -- documented as a deliberate scope decision in Task 2/3, not an oversight), ctrl+c → `snapback cleanup` pointer, and the TTY-selection rule ("command wiring is the only place that decides") are all covered by a task above. `init`'s wizard, `status --vm`'s card, and `restore` are explicitly out of scope (separate plans).
- **Placeholder scan:** none found on re-read.
- **Type consistency:** `runDeps.runInteractive`'s signature (`func(out io.Writer, vmName string, cancel context.CancelFunc, backupFn func(progress.Reporter) (*backup.Result, error)) (*backup.Result, error)`) matches `tui.RunInteractive`'s actual signature exactly (Task 5 defines it, Task 6 assigns it, Task 7's tests use it) modulo the trailing `extraOpts ...tea.ProgramOption`, which is variadic and thus compatible with the deps field's fixed-arity type when wrapped in the closure Task 6's `defaultRunDeps` builds.
