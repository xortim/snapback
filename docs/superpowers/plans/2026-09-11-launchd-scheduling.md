# launchd Scheduling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `snapback` unattended, scheduled backups: a `config.VM.Schedule` enum (`daily`/`weekly`/`monthly`), a new `internal/launchd` package that generates and installs/removes one `LaunchAgent` plist per scheduled VM, a `snapback schedule sync` command plus auto-sync wired into `vm add`/`vm remove`/`init`, matching wizard changes, and simple log rotation for scheduled-run output.

**Architecture:** `internal/launchd` isolates all `launchctl`/plist/filesystem work behind an `Installer` interface (mirroring `vm.Controller`/`vm.FakeVMController`), so the one piece of real choreography (`Sync`) is written and unit-tested against `FakeInstaller` with no real launchd interaction. `internal/cli` calls `Sync` from four places (`schedule sync`, `vm add`, `vm remove`, `init`) through one shared helper. Every plist always runs `snapback run --vm <name>` — there is no multi-VM batching and no new `run` flag.

**Tech Stack:** Go 1.26.5, standard library only for the new package (`text/template` for plist rendering, `os/exec` for `launchctl`, `encoding/xml` for escaping) — no new `go.mod` dependency.

**Spec:** `docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md` (ADR-005)

## Global Constraints

- Go 1.26.5, module `github.com/xortim/snapback`.
- No `sudo`/root-owned files anywhere in this feature (no `/etc/newsyslog.d`) — matches every other part of this tool running unprivileged.
- `launchctl bootstrap`/`bootout` target the modern per-user GUI domain (`gui/<uid>`), never the deprecated `load`/`unload` subcommands.
- Unit tests never shell out to real `launchctl` or touch the real `~/Library/LaunchAgents` — choreography (`Sync`) is tested only against `FakeInstaller`. Real `launchctl` execution is exercised only behind the existing `SNAPBACK_INTEGRATION=1` + `-tags=integration` gate, same split as `vmcli`/`vmrun`.
- `config.VM.Schedule` is a closed enum: `""`, `"daily"`, `"weekly"`, `"monthly"`. No raw cron syntax anywhere after this plan lands.
- `Day` and `Weekday` must never both appear in one `StartCalendarInterval` dict (Apple's documented OR-semantics gotcha — see ADR-005's Architecture section).
- Run `make lint` and `make test` (CI's own targets — see `Makefile`) before considering any task's commit done; run `make build` at the end of the whole plan.
- Every new/changed file must pass `gofmt`/`golangci-lint fmt` — match existing tab indentation.

---

### Task 1: `config.VM.Schedule` becomes a closed enum

**Files:**
- Modify: `internal/config/validate.go` (the `validateVMs` loop)
- Modify: `internal/config/config.go:30` (doc comment on `Schedule` only, no type change)
- Modify: `internal/config/validate_test.go`
- Modify: `internal/config/config_test.go:23,26,49` (fixture used raw cron; narrow to the new enum)
- Modify: `internal/config/marshal_test.go:17,36`
- Test: `internal/config/validate_test.go` (extends existing table)

**Interfaces:**
- Consumes: nothing new — `config.VM.Schedule` stays a plain `string` field, only `Validate`'s rules change.
- Produces: from here on, every other task can assume any `config.VM` that passed `Validate`/`ValidateVMs` has `Schedule` in `{"", "daily", "weekly", "monthly"}`.

- [ ] **Step 1: Write the failing validation tests**

Append to `internal/config/validate_test.go`:

```go
func TestValidate_RejectsUnknownSchedule(t *testing.T) {
	cfg := validConfig()
	cfg.VMs[0].Schedule = "0 2 * * *"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "schedule") || !strings.Contains(err.Error(), "0 2 * * *") {
		t.Errorf("Validate() = %v, want an error naming the bad schedule value", err)
	}
}

func TestValidate_AcceptsEveryScheduleEnumValue(t *testing.T) {
	for _, sched := range []string{"", "daily", "weekly", "monthly"} {
		cfg := validConfig()
		cfg.VMs[0].Schedule = sched
		if err := config.Validate(cfg); err != nil {
			t.Errorf("Validate() with schedule %q = %v, want nil", sched, err)
		}
	}
}

func TestValidateVMs_RejectsUnknownSchedule(t *testing.T) {
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "nightly"}}
	err := config.ValidateVMs(vms)
	if err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Errorf("ValidateVMs() = %v, want an error naming the bad schedule value", err)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/config/... -run 'TestValidate_RejectsUnknownSchedule|TestValidate_AcceptsEveryScheduleEnumValue|TestValidateVMs_RejectsUnknownSchedule' -v`
Expected: `TestValidate_RejectsUnknownSchedule` and `TestValidateVMs_RejectsUnknownSchedule` FAIL (no error currently returned for a bad schedule); `TestValidate_AcceptsEveryScheduleEnumValue` PASSes already (nothing rejects yet) — that's fine, it's here to pin the accept-side behavior once the reject-side check exists.

- [ ] **Step 3: Add the enum check to `validateVMs`**

In `internal/config/validate.go`, inside the `for i, vm := range vms` loop in `validateVMs` (after the existing `vmx` check), add:

```go
		switch vm.Schedule {
		case "", "daily", "weekly", "monthly":
		default:
			errs = append(errs, fmt.Errorf("vms[%d]: schedule must be \"\", \"daily\", \"weekly\", or \"monthly\", got %q", i, vm.Schedule))
		}
```

Also update the doc comment on `Schedule` in `internal/config/config.go:30`:

```go
	// Schedule is one of "", "daily", "weekly", "monthly" -- see ADR-005
	// (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md).
	// "" means unscheduled: no LaunchAgent is generated for this VM.
	Schedule string `koanf:"schedule" yaml:"schedule,omitempty"`
```

- [ ] **Step 4: Run the tests again to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS, including all three new tests and every pre-existing test in the package (the next two steps fix the two that will currently fail because their fixtures use raw cron strings).

- [ ] **Step 5: Fix `config_test.go`'s fixture, which now fails validation**

In `internal/config/config_test.go`, change the inline YAML (lines ~23, 26):

```go
    schedule: daily
```
```go
    schedule: weekly
```

(replacing `schedule: "0 2 * * *"` and `schedule: "0 2 * * 0"` respectively), and the assertion at line 49:

```go
	if cfg.VMs[0].Name != "dev-ubuntu" || cfg.VMs[0].VMX != "/Users/testuser/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx" || cfg.VMs[0].Schedule != "daily" {
```

- [ ] **Step 6: Fix `marshal_test.go`'s fixture**

In `internal/config/marshal_test.go:17`, change `Schedule: "0 2 * * *"` to `Schedule: "daily"`, and the expected YAML at line 36 from `schedule: 0 2 * * *` to `schedule: daily`.

- [ ] **Step 7: Run the whole package's tests to confirm everything passes together**

Run: `go test ./internal/config/... -v`
Expected: PASS, no failures.

- [ ] **Step 8: Commit**

```bash
git add internal/config/validate.go internal/config/config.go internal/config/validate_test.go internal/config/config_test.go internal/config/marshal_test.go
git commit -m "feat(config): narrow VM.Schedule to a daily/weekly/monthly enum"
```

---

### Task 2: `internal/launchd` — VM-name sanitization and collision detection (#84)

Called out in ADR-005's Risks as a hard dependency: without this, two VMs whose names collapse to the same label would silently share one `LaunchAgent`.

**Files:**
- Create: `internal/launchd/sanitize.go`
- Test: `internal/launchd/sanitize_test.go`

**Interfaces:**
- Consumes: `config.VM` (just `.Name`).
- Produces: `sanitizeLabel(name string) string` and `DetectCollisions(vms []config.VM) error`, both consumed by Task 4 (`buildAgent`) and Task 7 (`Sync`).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/sanitize_test.go`:

```go
package launchd

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"dev-ubuntu", "dev-ubuntu"},
		{"My VM!", "my-vm"},
		{"win_testbed 2", "win-testbed-2"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"!!!", ""},
		{"A", "a"},
		{"a--b", "a-b"},
	}
	for _, tt := range tests {
		if got := sanitizeLabel(tt.name); got != tt.want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDetectCollisions_NoCollision(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	if err := DetectCollisions(vms); err != nil {
		t.Errorf("DetectCollisions() = %v, want nil", err)
	}
}

func TestDetectCollisions_TwoNamesSanitizeToSameLabel(t *testing.T) {
	vms := []config.VM{
		{Name: "My VM!", VMX: "/vms/a.vmx"},
		{Name: "My VM?", VMX: "/vms/b.vmx"},
	}
	err := DetectCollisions(vms)
	if err == nil || !strings.Contains(err.Error(), "My VM!") || !strings.Contains(err.Error(), "My VM?") {
		t.Errorf("DetectCollisions() = %v, want an error naming both colliding VMs", err)
	}
}

func TestDetectCollisions_AllPunctuationName_RejectsEmptyLabel(t *testing.T) {
	vms := []config.VM{{Name: "!!!", VMX: "/vms/a.vmx"}}
	err := DetectCollisions(vms)
	if err == nil || !strings.Contains(err.Error(), "!!!") {
		t.Errorf("DetectCollisions() = %v, want an error naming the all-punctuation VM", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -v`
Expected: FAIL to build (package `launchd` and its functions don't exist yet).

- [ ] **Step 3: Implement `sanitizeLabel` and `DetectCollisions`**

Create `internal/launchd/sanitize.go`:

```go
// Package launchd generates and installs/removes the per-VM launchd
// LaunchAgents that drive scheduled backups. See ADR-005
// (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md).
package launchd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xortim/snapback/internal/config"
)

// sanitizeLabel converts a VM name into a value safe to embed in a
// launchd label (com.tim.snapback.<label>) and a filesystem path
// segment: lowercase, every run of characters that isn't a-z or 0-9
// collapsed to a single "-", with no leading or trailing "-". An
// all-punctuation name sanitizes to "" -- callers must reject that (see
// DetectCollisions) rather than generate a malformed label.
func sanitizeLabel(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		default:
			pendingDash = true
		}
	}
	return b.String()
}

// DetectCollisions reports an error naming every VM name that either
// sanitizes to "" (all-punctuation, e.g. "!!!") or collides with another
// VM's sanitized name (e.g. "My VM!" and "My VM?" both -> "my-vm") --
// either case would otherwise make two VMs silently share one plist
// file/label, or produce an unusable one. Checked across every VM in
// vms regardless of Schedule, since adding a schedule to a
// currently-unscheduled VM later would surface the same collision then
// instead of now.
func DetectCollisions(vms []config.VM) error {
	bySanitized := make(map[string][]string)
	for _, v := range vms {
		s := sanitizeLabel(v.Name)
		bySanitized[s] = append(bySanitized[s], v.Name)
	}

	var errs []error
	for s, names := range bySanitized {
		switch {
		case s == "":
			errs = append(errs, fmt.Errorf("VM name(s) %v sanitize to an empty launchd label; rename them", names))
		case len(names) > 1:
			errs = append(errs, fmt.Errorf("VM names %v all sanitize to the same launchd label %q; rename one", names, s))
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchd/sanitize.go internal/launchd/sanitize_test.go
git commit -m "feat(launchd): add VM-name sanitization and collision detection (#84)"
```

---

### Task 3: `internal/launchd` — the Day/Weekday-safe `StartCalendarInterval` builder

This is the task that directly guards against the bug caught while scoping ADR-005: a naive shared struct would leak a zero-valued `Day`/`Weekday` key into a dict that didn't intend to set it, and Apple's launchd triggers OR-semantics (not AND) whenever both keys are present.

**Files:**
- Create: `internal/launchd/calendar.go`
- Test: `internal/launchd/calendar_test.go`

**Interfaces:**
- Consumes: nothing (pure function of a schedule string).
- Produces: `calendarKey{Name string, Value int}` and `calendarInterval(schedule string) []calendarKey`, consumed by Task 4 (`buildAgent`/`renderPlist`).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/calendar_test.go`:

```go
package launchd

import (
	"testing"
	"time"
)

func TestCalendarInterval_ExactKeySets(t *testing.T) {
	tests := []struct {
		schedule string
		want     []calendarKey
	}{
		{"daily", []calendarKey{{"Hour", 0}, {"Minute", 0}}},
		{"weekly", []calendarKey{{"Weekday", 0}, {"Hour", 0}, {"Minute", 0}}},
		{"monthly", []calendarKey{{"Day", 1}, {"Hour", 0}, {"Minute", 0}}},
		{"", nil},
	}
	for _, tt := range tests {
		got := calendarInterval(tt.schedule)
		if len(got) != len(tt.want) {
			t.Fatalf("calendarInterval(%q) = %+v, want %+v", tt.schedule, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("calendarInterval(%q)[%d] = %+v, want %+v", tt.schedule, i, got[i], tt.want[i])
			}
		}
	}
}

func TestCalendarInterval_NeverCombinesDayAndWeekday(t *testing.T) {
	for _, schedule := range []string{"daily", "weekly", "monthly"} {
		var hasDay, hasWeekday bool
		for _, k := range calendarInterval(schedule) {
			if k.Name == "Day" {
				hasDay = true
			}
			if k.Name == "Weekday" {
				hasWeekday = true
			}
		}
		if hasDay && hasWeekday {
			t.Errorf("calendarInterval(%q) includes both Day and Weekday -- Apple's launchd.plist(5) OR-semantics for that combination would make this fire far more often than intended", schedule)
		}
	}
}

// intervalMatches reproduces launchd's own StartCalendarInterval
// matching rule for the dicts this package generates: every present key
// must match t exactly (AND semantics). This package never emits a dict
// with both Day and Weekday present (see the test above), so the
// separate OR-semantics Apple documents for that combination never
// applies to output from calendarInterval -- this helper does not need
// to implement it.
func intervalMatches(interval []calendarKey, t time.Time) bool {
	for _, k := range interval {
		var actual int
		switch k.Name {
		case "Minute":
			actual = t.Minute()
		case "Hour":
			actual = t.Hour()
		case "Day":
			actual = t.Day()
		case "Weekday":
			actual = int(t.Weekday())
		case "Month":
			actual = int(t.Month())
		}
		if actual != k.Value {
			return false
		}
	}
	return true
}

func countMatchesOverYear(t *testing.T, schedule string, year int) int {
	t.Helper()
	interval := calendarInterval(schedule)
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, time.UTC)
	count := 0
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		if intervalMatches(interval, day) {
			count++
		}
	}
	return count
}

func TestCalendarInterval_DailyFiresOncePerDay(t *testing.T) {
	if got := countMatchesOverYear(t, "daily", 2027); got != 365 { // 2027 is not a leap year
		t.Errorf("daily preset matched %d times in 2027, want 365", got)
	}
	if got := countMatchesOverYear(t, "daily", 2028); got != 366 { // 2028 is a leap year
		t.Errorf("daily preset matched %d times in leap year 2028, want 366", got)
	}
}

func TestCalendarInterval_WeeklyFiresOnceAWeek(t *testing.T) {
	got := countMatchesOverYear(t, "weekly", 2027)
	if got != 52 && got != 53 {
		t.Errorf("weekly preset matched %d times in 2027, want 52 or 53 (any Gregorian year has that many occurrences of a given weekday)", got)
	}
}

func TestCalendarInterval_MonthlyFiresOnceAMonth(t *testing.T) {
	if got := countMatchesOverYear(t, "monthly", 2027); got != 12 {
		t.Errorf("monthly preset matched %d times in 2027, want 12", got)
	}
}

// TestCalendarInterval_RegressionForSharedStructBug reproduces the exact
// failure mode a naive shared all-fields struct would have caused if
// this package had used one: an implicit Weekday: 0 leaking into the
// monthly dict, or an implicit Day: 0 leaking into weekly's, engaging
// launchd's documented Day+Weekday OR-semantics and firing several
// times a month instead of once. If calendarInterval regresses to
// including the other axis's key, this test's monthly/weekly match
// counts above would jump well past 12 / 52-53 -- this test exists
// specifically so that jump is caught here, in-process, rather than
// discovered as unwanted 2am backups in production.
func TestCalendarInterval_RegressionForSharedStructBug(t *testing.T) {
	monthly := countMatchesOverYear(t, "monthly", 2027)
	if monthly > 12 {
		t.Fatalf("monthly preset matched %d times in 2027 (want exactly 12) -- Day+Weekday OR-semantics bug has regressed", monthly)
	}
	weekly := countMatchesOverYear(t, "weekly", 2027)
	if weekly > 53 {
		t.Fatalf("weekly preset matched %d times in 2027 (want 52 or 53) -- Day+Weekday OR-semantics bug has regressed", weekly)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestCalendarInterval -v`
Expected: FAIL to build (`calendarKey`/`calendarInterval` don't exist yet).

- [ ] **Step 3: Implement `calendarInterval`**

Create `internal/launchd/calendar.go`:

```go
package launchd

// calendarKey is one key/value pair of a StartCalendarInterval plist
// dict (e.g. {"Hour", 0}). Represented as an ordered slice, not a map or
// a fixed all-fields struct -- see calendarInterval's doc comment for
// why the ordering and the "only include keys this preset needs" rule
// both matter.
type calendarKey struct {
	Name  string
	Value int
}

// calendarInterval returns the StartCalendarInterval keys for schedule
// ("daily", "weekly", or "monthly"; nil for "" or any other value),
// matching cron's own @daily/@weekly/@monthly meta-schedules: fixed
// midnight-based times, no time-of-day override.
//
// Each preset includes *only* the keys it needs -- daily never mentions
// Day/Weekday/Month at all, weekly never mentions Day, monthly never
// mentions Weekday. This is deliberate, not an oversight: Apple's
// launchd.plist(5) documents that missing keys are wildcards, but if
// both Day and Weekday are present in the same dict they're combined
// with OR, not AND ("the job will be started if either one matches").
// A shared Go struct covering all five calendar fields would make this
// trivial to get wrong by accident -- a plain int field's zero value is
// indistinguishable from an intentionally-set 0, so a monthly dict built
// from such a struct would carry an implicit, un-intended Weekday: 0
// alongside its real Day: 1, and OR-semantics would then fire the job
// every Sunday in addition to the 1st. Returning a hand-built slice per
// preset, containing only the relevant keys, makes that combination
// structurally impossible to introduce by accident.
func calendarInterval(schedule string) []calendarKey {
	switch schedule {
	case "daily":
		return []calendarKey{{"Hour", 0}, {"Minute", 0}}
	case "weekly":
		return []calendarKey{{"Weekday", 0}, {"Hour", 0}, {"Minute", 0}} // Sunday
	case "monthly":
		return []calendarKey{{"Day", 1}, {"Hour", 0}, {"Minute", 0}} // 1st of the month
	default:
		return nil
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -run TestCalendarInterval -v`
Expected: PASS, all six test functions.

- [ ] **Step 5: Commit**

```bash
git add internal/launchd/calendar.go internal/launchd/calendar_test.go
git commit -m "feat(launchd): add Day/Weekday-safe StartCalendarInterval builder"
```

---

### Task 4: `internal/launchd` — `Agent`, `LogPath`, and plist rendering

**Files:**
- Create: `internal/launchd/plist.go`
- Test: `internal/launchd/plist_test.go`

**Interfaces:**
- Consumes: `sanitizeLabel` (Task 2), `calendarInterval`/`calendarKey` (Task 3), `config.VM`.
- Produces: `Agent` struct, `buildAgent(v config.VM, binaryPath string) (Agent, error)`, `LogPath(vmName string) (string, error)`, `renderPlist(agent Agent) ([]byte, error)` -- all consumed by Task 6 (`LaunchctlInstaller.Write`) and Task 7 (`FakeInstaller.Write`, `Sync`).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/plist_test.go`:

```go
package launchd

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestBuildAgent(t *testing.T) {
	v := config.VM{Name: "My VM!", VMX: "/vms/my-vm.vmx", Schedule: "weekly"}
	agent, err := buildAgent(v, "/usr/local/bin/snapback")
	if err != nil {
		t.Fatalf("buildAgent() error = %v", err)
	}
	if agent.Label != "com.tim.snapback.my-vm" {
		t.Errorf("Label = %q, want %q", agent.Label, "com.tim.snapback.my-vm")
	}
	if agent.VMName != "My VM!" {
		t.Errorf("VMName = %q, want the original, unsanitized name", agent.VMName)
	}
	if agent.BinaryPath != "/usr/local/bin/snapback" {
		t.Errorf("BinaryPath = %q, want %q", agent.BinaryPath, "/usr/local/bin/snapback")
	}
	if !strings.HasSuffix(agent.LogPath, "/Library/Logs/snapback/my-vm.log") {
		t.Errorf("LogPath = %q, want it to end in /Library/Logs/snapback/my-vm.log", agent.LogPath)
	}
	if len(agent.Interval) == 0 {
		t.Error("Interval is empty, want the weekly preset's keys")
	}
}

func TestBuildAgent_UnrecognizedSchedule_Errors(t *testing.T) {
	v := config.VM{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "bogus"}
	if _, err := buildAgent(v, "/usr/local/bin/snapback"); err == nil {
		t.Error("buildAgent() error = nil, want an error for an unrecognized schedule")
	}
}

func TestBuildAgent_EmptySanitizedName_Errors(t *testing.T) {
	v := config.VM{Name: "!!!", VMX: "/vms/a.vmx", Schedule: "daily"}
	if _, err := buildAgent(v, "/usr/local/bin/snapback"); err == nil {
		t.Error("buildAgent() error = nil, want an error for a name that sanitizes to empty")
	}
}

func TestRenderPlist_Daily(t *testing.T) {
	agent := Agent{
		Label:      "com.tim.snapback.dev",
		VMName:     "dev",
		BinaryPath: "/usr/local/bin/snapback",
		LogPath:    "/Users/tim/Library/Logs/snapback/dev.log",
		Interval:   calendarInterval("daily"),
	}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.tim.snapback.dev</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/snapback</string>
		<string>run</string>
		<string>--vm</string>
		<string>dev</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>0</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>/Users/tim/Library/Logs/snapback/dev.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/tim/Library/Logs/snapback/dev.log</string>
</dict>
</plist>
`
	if string(got) != want {
		t.Errorf("renderPlist() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderPlist_Weekly_NeverContainsDayKey(t *testing.T) {
	agent := Agent{Label: "l", VMName: "v", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("weekly")}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "<key>Day</key>") {
		t.Error("weekly plist contains a Day key -- this is the exact Day+Weekday OR-semantics bug ADR-005 exists to prevent")
	}
	if !strings.Contains(string(got), "<key>Weekday</key>") {
		t.Error("weekly plist is missing its Weekday key")
	}
}

func TestRenderPlist_Monthly_NeverContainsWeekdayKey(t *testing.T) {
	agent := Agent{Label: "l", VMName: "v", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("monthly")}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "<key>Weekday</key>") {
		t.Error("monthly plist contains a Weekday key -- this is the exact Day+Weekday OR-semantics bug ADR-005 exists to prevent")
	}
	if !strings.Contains(string(got), "<key>Day</key>") {
		t.Error("monthly plist is missing its Day key")
	}
}

func TestRenderPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	agent := Agent{
		Label:      "l",
		VMName:     `dev & <test>`,
		BinaryPath: "/bin/snapback",
		LogPath:    "/log",
		Interval:   calendarInterval("daily"),
	}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "dev & <test>") {
		t.Error("renderPlist() did not escape XML special characters in VMName")
	}
	if !strings.Contains(string(got), "dev &amp; &lt;test&gt;") {
		t.Errorf("renderPlist() = %s, want the VMName XML-escaped", got)
	}
}

func TestLogPath_UsesSanitizedName(t *testing.T) {
	path, err := LogPath("My VM!")
	if err != nil {
		t.Fatalf("LogPath() error = %v", err)
	}
	if !strings.HasSuffix(path, "/Library/Logs/snapback/my-vm.log") {
		t.Errorf("LogPath() = %q, want it to end in /Library/Logs/snapback/my-vm.log", path)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run 'TestBuildAgent|TestRenderPlist|TestLogPath' -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement `Agent`, `buildAgent`, `LogPath`, `renderPlist`**

Create `internal/launchd/plist.go`:

```go
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// labelPrefix is prepended to every sanitized VM name to form a launchd
// label, following reverse-DNS convention (matches the existing
// com.tim.snapback.plist name docs/design.md already documents for the
// pre-this-ADR single global plist).
const labelPrefix = "com.tim.snapback."

// Agent describes one VM's scheduled backup job -- everything
// renderPlist needs to produce that VM's LaunchAgent plist.
type Agent struct {
	Label      string
	VMName     string // original, unsanitized config.VM.Name
	BinaryPath string
	LogPath    string
	Interval   []calendarKey
}

// buildAgent resolves v's plist inputs. binaryPath is the running
// snapback binary's path (os.Executable(), resolved by the caller) --
// see ADR-005's Risks for the known gap if the binary later moves
// without a resync.
func buildAgent(v config.VM, binaryPath string) (Agent, error) {
	sanitized := sanitizeLabel(v.Name)
	if sanitized == "" {
		return Agent{}, fmt.Errorf("VM %q sanitizes to an empty launchd label; rename it", v.Name)
	}
	interval := calendarInterval(v.Schedule)
	if interval == nil {
		return Agent{}, fmt.Errorf("VM %q has no recognized schedule (got %q)", v.Name, v.Schedule)
	}
	logPath, err := LogPath(v.Name)
	if err != nil {
		return Agent{}, err
	}
	return Agent{
		Label:      labelPrefix + sanitized,
		VMName:     v.Name,
		BinaryPath: binaryPath,
		LogPath:    logPath,
		Interval:   interval,
	}, nil
}

// LogPath returns the deterministic path a scheduled run's stdout/stderr
// gets redirected to via the generated plist's StandardOutPath/
// StandardErrorPath. internal/cli/run.go's own log rotation (Task 11)
// computes this same path independently rather than threading it
// through config, so both sides always agree on one location.
func LogPath(vmName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", "snapback", sanitizeLabel(vmName)+".log"), nil
}

func xmlEscapeString(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{
	"esc": xmlEscapeString,
}).Parse(plistTmplSrc))

const plistTmplSrc = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{esc .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{esc .BinaryPath}}</string>
		<string>run</string>
		<string>--vm</string>
		<string>{{esc .VMName}}</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
{{- range .Interval}}
		<key>{{.Name}}</key>
		<integer>{{.Value}}</integer>
{{- end}}
	</dict>
	<key>StandardOutPath</key>
	<string>{{esc .LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{esc .LogPath}}</string>
</dict>
</plist>
`

// renderPlist renders agent as a complete LaunchAgent plist document.
func renderPlist(agent Agent) ([]byte, error) {
	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, agent); err != nil {
		return nil, fmt.Errorf("render plist for %q: %w", agent.VMName, err)
	}
	return buf.Bytes(), nil
}
```

Add the missing import: `buildAgent` takes a `config.VM`, so add `"github.com/xortim/snapback/internal/config"` to this file's import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -run 'TestBuildAgent|TestRenderPlist|TestLogPath' -v`
Expected: PASS, all eight test functions. Pay special attention to `TestRenderPlist_Daily`'s exact byte comparison — if it fails on whitespace, check the `{{- range}}`/`{{- end}}` trim markers against the template source above character-for-character before changing the expected string.

- [ ] **Step 5: Commit**

```bash
git add internal/launchd/plist.go internal/launchd/plist_test.go
git commit -m "feat(launchd): add Agent, LogPath, and plist rendering"
```

---

### Task 5: `internal/launchd` — log rotation

**Files:**
- Create: `internal/launchd/rotate.go`
- Test: `internal/launchd/rotate_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `RotateIfOversized(path string) error`, called from Task 11 (`internal/cli/run.go`).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/rotate_test.go`:

```go
package launchd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateIfOversized_MissingFile_NoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.log")
	if err := RotateIfOversized(path); err != nil {
		t.Errorf("RotateIfOversized() on a missing file = %v, want nil", err)
	}
}

func TestRotateIfOversized_SmallFile_NoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.log")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := RotateIfOversized(path); err != nil {
		t.Fatalf("RotateIfOversized() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "small" {
		t.Errorf("file content = %q, err = %v; want untouched \"small\"", data, err)
	}
}

func TestRotateIfOversized_ShiftsGenerationsAndDropsTheOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.log")
	oversized := make([]byte, maxLogBytes)

	mustWrite := func(p string, content []byte) {
		t.Helper()
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	mustWrite(path, oversized)
	mustWrite(path+".1", []byte("generation-1"))
	mustWrite(path+".2", []byte("generation-2-should-be-deleted"))

	if err := RotateIfOversized(path); err != nil {
		t.Fatalf("RotateIfOversized() error = %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("current log path still exists after rotation, want it renamed away")
	}
	gen1, err := os.ReadFile(path + ".1")
	if err != nil || len(gen1) != maxLogBytes {
		t.Errorf(".1 content = %d bytes, err = %v; want the just-rotated oversized content", len(gen1), err)
	}
	gen2, err := os.ReadFile(path + ".2")
	if err != nil || string(gen2) != "generation-1" {
		t.Errorf(".2 content = %q, err = %v; want the old .1's content shifted up", gen2, err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Error("a .3 generation exists, want the oldest generation deleted rather than kept")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestRotateIfOversized -v`
Expected: FAIL to build (`RotateIfOversized`/`maxLogBytes` don't exist yet).

- [ ] **Step 3: Implement `RotateIfOversized`**

Create `internal/launchd/rotate.go`:

```go
package launchd

import (
	"fmt"
	"os"
)

const (
	// maxLogBytes is the size threshold at which a scheduled run's log
	// file gets rotated before this run writes anything else to it.
	maxLogBytes = 5 * 1024 * 1024
	// maxArchivedGenerations is how many rotated-away generations
	// (.log.1, .log.2) are kept alongside the live .log file -- 3
	// generations total, matching ADR-005.
	maxArchivedGenerations = 2
)

// RotateIfOversized renames path to path+".1" (after shifting any
// existing ".1".."maxArchivedGenerations-1" up by one and deleting
// anything at or beyond maxArchivedGenerations), if path's current size
// is at least maxLogBytes. A missing path is not an error -- there's
// nothing to rotate yet, e.g. this VM's first-ever scheduled run.
//
// Safe to call unconditionally at the top of every `run`, interactive or
// scheduled. For an interactive run (a real terminal, not launchd),
// rotating the on-disk file at this path has no effect on this
// process's own output, which goes to the terminal via
// cmd.OutOrStdout(), never to this path. For a launchd-invoked run, this
// process's stdout/stderr file descriptor was already dup2'd from this
// exact path by launchd before exec -- renaming the file now doesn't
// redirect *this* run's own output (it keeps writing into whatever
// inode it already has open, now named path+".1" after this call), but
// that's fine: launchd opens StandardOutPath fresh for every new
// process it spawns, so the *next* scheduled run gets a missing (and
// therefore freshly created, empty) file at path -- which is the actual
// goal. This run's own output ending up archived into path+".1" instead
// of a fresh path is an accepted, simple consequence of not needing any
// dup2/fd-reopening trickery to achieve it.
func RotateIfOversized(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() < maxLogBytes {
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", path, maxArchivedGenerations)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", oldest, err)
	}
	for n := maxArchivedGenerations - 1; n >= 1; n-- {
		src := fmt.Sprintf("%s.%d", path, n)
		dst := fmt.Sprintf("%s.%d", path, n+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rename %s to %s: %w", src, dst, err)
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rename %s to %s.1: %w", path, path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -run TestRotateIfOversized -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchd/rotate.go internal/launchd/rotate_test.go
git commit -m "feat(launchd): add built-in log rotation, no sudo/newsyslog needed"
```

---

### Task 6: `internal/launchd` — `Installer` interface and `LaunchctlInstaller`

**Files:**
- Create: `internal/launchd/installer.go`
- Create: `internal/launchd/launchctl_integration_test.go`

**Interfaces:**
- Consumes: `Agent`/`renderPlist` (Task 4).
- Produces: `Installer` interface and `*LaunchctlInstaller`, consumed by Task 7 (`Sync` is written against the `Installer` interface) and Task 8 (`internal/cli/schedule.go` constructs the real one).

```go
type Installer interface {
	Write(agent Agent) (plistPath string, changed bool, err error)
	Bootstrap(plistPath string) error
	Bootout(label string) error
	Remove(label string) error
	List() ([]string, error)
}
```

- [ ] **Step 1: Write the failing unit test (no real launchctl)**

Create `internal/launchd/installer_test.go`:

```go
package launchd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchctlInstaller_WriteThenList(t *testing.T) {
	dir := t.TempDir()
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}

	path, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !changed {
		t.Error("Write() changed = false on first write, want true")
	}
	if path != filepath.Join(dir, "com.tim.snapback.dev.plist") {
		t.Errorf("Write() path = %q, want %q", path, filepath.Join(dir, "com.tim.snapback.dev.plist"))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("plist file not written: %v", err)
	}

	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 1 || labels[0] != "com.tim.snapback.dev" {
		t.Errorf("List() = %v, want [\"com.tim.snapback.dev\"]", labels)
	}
}

func TestLaunchctlInstaller_Write_UnchangedOnIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}

	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	_, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if changed {
		t.Error("second Write() with identical content changed = true, want false")
	}
}

func TestLaunchctlInstaller_Write_ChangedWhenScheduleDiffers(t *testing.T) {
	dir := t.TempDir()
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}

	agent.Interval = calendarInterval("weekly")
	_, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if !changed {
		t.Error("second Write() with a different schedule changed = false, want true")
	}
}

func TestLaunchctlInstaller_Remove_MissingFileIsNotAnError(t *testing.T) {
	inst := &LaunchctlInstaller{Dir: t.TempDir()}
	if err := inst.Remove("com.tim.snapback.never-existed"); err != nil {
		t.Errorf("Remove() on a nonexistent plist = %v, want nil", err)
	}
}

func TestLaunchctlInstaller_List_EmptyDir(t *testing.T) {
	inst := &LaunchctlInstaller{Dir: t.TempDir()}
	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() = %v, want empty", labels)
	}
}

func TestLaunchctlInstaller_List_IgnoresNonSnapbackPlists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "com.apple.something.plist"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	inst := &LaunchctlInstaller{Dir: dir}
	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() = %v, want it to ignore a non-snapback plist", labels)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestLaunchctlInstaller -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement `Installer` and `LaunchctlInstaller`**

Create `internal/launchd/installer.go`:

```go
package launchd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Installer is the shell-out/filesystem boundary for launchd plist
// management -- Sync (Task 7) is written and tested against
// FakeInstaller, mirroring vm.Controller/vm.FakeVMController's split, so
// its choreography never needs a real launchctl or a real
// ~/Library/LaunchAgents to run its unit tests.
type Installer interface {
	// Write renders agent's plist and writes it to plistPath (creating
	// the parent directory if needed), returning the path written and
	// whether the content differs from whatever was already at that
	// path (true if the file didn't exist before).
	Write(agent Agent) (plistPath string, changed bool, err error)
	// Bootstrap loads the plist at plistPath into the current user's GUI
	// launchd domain.
	Bootstrap(plistPath string) error
	// Bootout unloads the job with the given label from the GUI domain.
	// Not an error if the job isn't currently loaded.
	Bootout(label string) error
	// Remove deletes the plist file for label from disk. Not an error if
	// it's already gone.
	Remove(label string) error
	// List returns the labels of every snapback-managed plist file
	// currently on disk (matching "com.tim.snapback.*"), regardless of
	// whether it's currently bootstrapped.
	List() ([]string, error)
}

// LaunchctlInstaller is the real Installer, shelling out to launchctl
// against plist files under Dir.
type LaunchctlInstaller struct {
	// Dir is where plist files are read/written -- normally
	// ~/Library/LaunchAgents. Exported and settable so integration tests
	// can point it at a scratch directory instead of the real one.
	Dir string
}

// NewLaunchctlInstaller returns a LaunchctlInstaller rooted at
// ~/Library/LaunchAgents.
func NewLaunchctlInstaller() (*LaunchctlInstaller, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	return &LaunchctlInstaller{Dir: filepath.Join(home, "Library", "LaunchAgents")}, nil
}

func (l *LaunchctlInstaller) plistPath(label string) string {
	return filepath.Join(l.Dir, label+".plist")
}

func (l *LaunchctlInstaller) Write(agent Agent) (string, bool, error) {
	path := l.plistPath(agent.Label)
	data, err := renderPlist(agent)
	if err != nil {
		return "", false, err
	}
	existing, readErr := os.ReadFile(path)
	changed := readErr != nil || !bytes.Equal(existing, data)
	if !changed {
		return path, false, nil
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create %s: %w", l.Dir, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", false, fmt.Errorf("write %s: %w", path, err)
	}
	return path, true, nil
}

func (l *LaunchctlInstaller) Bootstrap(plistPath string) error {
	return runLaunchctl("bootstrap", guiDomain(), plistPath)
}

func (l *LaunchctlInstaller) Bootout(label string) error {
	err := runLaunchctl("bootout", guiDomain()+"/"+label)
	// "Could not find service" means the label isn't currently loaded --
	// Sync's contract is idempotent removal, so that's not an error here.
	if err != nil && strings.Contains(err.Error(), "Could not find service") {
		return nil
	}
	return err
}

func (l *LaunchctlInstaller) Remove(label string) error {
	err := os.Remove(l.plistPath(label))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *LaunchctlInstaller) List() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(l.Dir, labelPrefix+"*.plist"))
	if err != nil {
		return nil, err
	}
	labels := make([]string, len(matches))
	for i, m := range matches {
		labels[i] = strings.TrimSuffix(filepath.Base(m), ".plist")
	}
	return labels, nil
}

func guiDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -run TestLaunchctlInstaller -v`
Expected: PASS. These exercise real file I/O (via `t.TempDir()`) but no real `launchctl` call, so they run on CI's `ubuntu-latest` too.

- [ ] **Step 5: Write the integration test (real launchctl, gated)**

Create `internal/launchd/launchctl_integration_test.go`, following the exact gating convention `internal/vm/vmcli_integration_test.go` already uses:

```go
//go:build integration

package launchd_test

// Real launchctl bootstrap/bootout execution, per README.md/CLAUDE.md:
//
//	SNAPBACK_INTEGRATION=1 go test ./... -tags=integration
//
// Uses a scratch directory (t.TempDir()) as Dir, not the real
// ~/Library/LaunchAgents, and a label distinct from any real snapback
// schedule -- this never touches a real backup schedule.

import (
	"os"
	"testing"

	"github.com/xortim/snapback/internal/launchd"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("SNAPBACK_INTEGRATION") != "1" {
		t.Skip("set SNAPBACK_INTEGRATION=1 to run launchd integration tests")
	}
}

func TestIntegration_BootstrapThenBootout(t *testing.T) {
	requireIntegration(t)
	inst := &launchd.LaunchctlInstaller{Dir: t.TempDir()}
	agent := launchd.Agent{
		Label:      "com.tim.snapback.integration-test",
		VMName:     "integration-test",
		BinaryPath: "/bin/echo",
		LogPath:    t.TempDir() + "/integration-test.log",
	}

	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Bootout(agent.Label); err != nil {
			t.Errorf("cleanup Bootout() error = %v", err)
		}
	})
}
```

- [ ] **Step 6: Run `go vet`/build check on the integration file (it won't run without the tag)**

Run: `go build -tags=integration ./...`
Expected: builds cleanly (the integration test file compiles even though it doesn't execute without `SNAPBACK_INTEGRATION=1`).

- [ ] **Step 7: Commit**

```bash
git add internal/launchd/installer.go internal/launchd/installer_test.go internal/launchd/launchctl_integration_test.go
git commit -m "feat(launchd): add Installer interface and LaunchctlInstaller"
```

---

### Task 7: `internal/launchd` — `FakeInstaller` and `Sync`

**Files:**
- Create: `internal/launchd/fake.go`
- Create: `internal/launchd/sync.go`
- Test: `internal/launchd/sync_test.go`

**Interfaces:**
- Consumes: `Installer` (Task 6), `buildAgent`/`Agent` (Task 4), `DetectCollisions` (Task 2).
- Produces: `*FakeInstaller` (satisfies `Installer`), `SyncResult`, `Sync(installer Installer, vms []config.VM, binaryPath string) (SyncResult, error)` -- consumed by Task 8/9/10 (every CLI call site).

- [ ] **Step 1: Write the failing tests**

Create `internal/launchd/sync_test.go`:

```go
package launchd

import (
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestSync_InstallsNewlyScheduledVM(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(result.Installed) != 1 || result.Installed[0] != "dev" {
		t.Errorf("result.Installed = %v, want [\"dev\"]", result.Installed)
	}
	if len(inst.BootstrapCalls) != 1 {
		t.Errorf("BootstrapCalls = %v, want exactly one call", inst.BootstrapCalls)
	}
}

func TestSync_UnscheduledVM_NeverWritten(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}} // Schedule == ""

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !result.IsEmpty() {
		t.Errorf("result = %+v, want empty", result)
	}
	if len(inst.BootstrapCalls) != 0 {
		t.Errorf("BootstrapCalls = %v, want none for an unscheduled VM", inst.BootstrapCalls)
	}
}

func TestSync_AlreadyInSync_IsANoOp(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if !result.IsEmpty() {
		t.Errorf("second Sync() result = %+v, want empty (already in sync)", result)
	}
	if len(inst.BootstrapCalls) != 1 {
		t.Errorf("BootstrapCalls after two Syncs = %v, want still exactly one (from the first Sync only)", inst.BootstrapCalls)
	}
}

func TestSync_ScheduleChanged_UpdatesAndRebootstraps(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms[0].Schedule = "weekly"
	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "dev" {
		t.Errorf("result.Updated = %v, want [\"dev\"]", result.Updated)
	}
	if len(inst.BootoutCalls) != 1 {
		t.Errorf("BootoutCalls = %v, want the stale plist booted out before re-bootstrapping", inst.BootoutCalls)
	}
	if len(inst.BootstrapCalls) != 2 {
		t.Errorf("BootstrapCalls = %v, want 2 (initial install + the update)", inst.BootstrapCalls)
	}
}

func TestSync_ScheduleCleared_RemovesPlist(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	vms[0].Schedule = ""
	result, err := Sync(inst, vms, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "com.tim.snapback.dev" {
		t.Errorf("result.Removed = %v, want [\"com.tim.snapback.dev\"]", result.Removed)
	}
	if len(inst.RemoveCalls) != 1 {
		t.Errorf("RemoveCalls = %v, want exactly one", inst.RemoveCalls)
	}
}

func TestSync_VMNoLongerInList_RemovesPlist(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}
	if _, err := Sync(inst, vms, "/bin/snapback"); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}

	result, err := Sync(inst, nil, "/bin/snapback")
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if len(result.Removed) != 1 {
		t.Errorf("result.Removed = %v, want the now-gone VM's plist removed", result.Removed)
	}
}

func TestSync_CollidingNames_ErrorsBeforeWritingAnything(t *testing.T) {
	inst := NewFakeInstaller()
	vms := []config.VM{
		{Name: "My VM!", VMX: "/vms/a.vmx", Schedule: "daily"},
		{Name: "My VM?", VMX: "/vms/b.vmx", Schedule: "weekly"},
	}
	_, err := Sync(inst, vms, "/bin/snapback")
	if err == nil {
		t.Fatal("Sync() error = nil, want the collision rejected")
	}
	if len(inst.BootstrapCalls) != 0 {
		t.Errorf("BootstrapCalls = %v, want none -- collision must be caught before any write", inst.BootstrapCalls)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/launchd/... -run TestSync -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement `FakeInstaller`**

Create `internal/launchd/fake.go`:

```go
package launchd

import (
	"bytes"
	"sort"
)

// FakeInstaller is an in-memory Installer for unit tests, mirroring
// vm.FakeVMController's role: Sync's choreography is written and tested
// against this, never against LaunchctlInstaller directly.
type FakeInstaller struct {
	WriteErr     error
	BootstrapErr error
	BootoutErr   error
	RemoveErr    error
	ListErr      error

	// BootstrapCalls/BootoutCalls/RemoveCalls record every call, in
	// order, so tests can assert not just that something happened but
	// exactly what and how many times.
	BootstrapCalls []string // plistPaths passed to Bootstrap
	BootoutCalls   []string // labels passed to Bootout
	RemoveCalls    []string // labels passed to Remove

	plists map[string][]byte // label -> last-written plist content
}

// NewFakeInstaller returns a FakeInstaller with nothing installed.
func NewFakeInstaller() *FakeInstaller {
	return &FakeInstaller{plists: make(map[string][]byte)}
}

func (f *FakeInstaller) Write(agent Agent) (string, bool, error) {
	if f.WriteErr != nil {
		return "", false, f.WriteErr
	}
	data, err := renderPlist(agent)
	if err != nil {
		return "", false, err
	}
	existing, ok := f.plists[agent.Label]
	changed := !ok || !bytes.Equal(existing, data)
	f.plists[agent.Label] = data
	return "/fake/LaunchAgents/" + agent.Label + ".plist", changed, nil
}

func (f *FakeInstaller) Bootstrap(plistPath string) error {
	if f.BootstrapErr != nil {
		return f.BootstrapErr
	}
	f.BootstrapCalls = append(f.BootstrapCalls, plistPath)
	return nil
}

func (f *FakeInstaller) Bootout(label string) error {
	if f.BootoutErr != nil {
		return f.BootoutErr
	}
	f.BootoutCalls = append(f.BootoutCalls, label)
	return nil
}

func (f *FakeInstaller) Remove(label string) error {
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.RemoveCalls = append(f.RemoveCalls, label)
	delete(f.plists, label)
	return nil
}

func (f *FakeInstaller) List() ([]string, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	labels := make([]string, 0, len(f.plists))
	for l := range f.plists {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels, nil
}
```

- [ ] **Step 4: Implement `Sync` and `SyncResult`**

Create `internal/launchd/sync.go`:

```go
package launchd

import (
	"fmt"

	"github.com/xortim/snapback/internal/config"
)

// SyncResult records what Sync changed, for callers to report to the
// user (internal/cli's `schedule sync`, `vm add`, `vm remove`, `init`
// all print this the same way).
type SyncResult struct {
	Installed []string // VM names newly given a LaunchAgent
	Updated   []string // VM names whose LaunchAgent was rewritten and re-bootstrapped
	Removed   []string // labels booted out and deleted
}

// IsEmpty reports whether Sync found nothing to do.
func (r SyncResult) IsEmpty() bool {
	return len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Removed) == 0
}

// Sync reconciles installer's on-disk/loaded state with vms: every VM
// with a non-empty Schedule gets its plist written and bootstrapped (if
// new) or re-bootstrapped (if its content changed since last sync);
// every plist installer already knows about that no longer corresponds
// to a scheduled VM in vms gets booted out and removed. Safe to call
// repeatedly -- an already-in-sync config produces an empty SyncResult
// and no Installer calls beyond the one List().
//
// binaryPath is embedded into each plist's ProgramArguments as the
// snapback binary to invoke (os.Executable(), resolved by the caller) --
// see ADR-005's Risks for the known gap if the binary is later moved
// without a resync.
func Sync(installer Installer, vms []config.VM, binaryPath string) (SyncResult, error) {
	if err := DetectCollisions(vms); err != nil {
		return SyncResult{}, err
	}

	var scheduled []Agent
	desiredLabels := make(map[string]bool)
	for _, v := range vms {
		if v.Schedule == "" {
			continue
		}
		agent, err := buildAgent(v, binaryPath)
		if err != nil {
			return SyncResult{}, err
		}
		scheduled = append(scheduled, agent)
		desiredLabels[agent.Label] = true
	}

	existingLabels, err := installer.List()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list installed schedules: %w", err)
	}
	existing := make(map[string]bool, len(existingLabels))
	for _, l := range existingLabels {
		existing[l] = true
	}

	var result SyncResult
	for _, agent := range scheduled {
		plistPath, changed, err := installer.Write(agent)
		if err != nil {
			return result, fmt.Errorf("write plist for %q: %w", agent.VMName, err)
		}
		switch {
		case !existing[agent.Label]:
			if err := installer.Bootstrap(plistPath); err != nil {
				return result, fmt.Errorf("bootstrap %q: %w", agent.VMName, err)
			}
			result.Installed = append(result.Installed, agent.VMName)
		case changed:
			if err := installer.Bootout(agent.Label); err != nil {
				return result, fmt.Errorf("bootout stale %q: %w", agent.VMName, err)
			}
			if err := installer.Bootstrap(plistPath); err != nil {
				return result, fmt.Errorf("bootstrap %q: %w", agent.VMName, err)
			}
			result.Updated = append(result.Updated, agent.VMName)
		}
	}

	for _, label := range existingLabels {
		if desiredLabels[label] {
			continue
		}
		if err := installer.Bootout(label); err != nil {
			return result, fmt.Errorf("bootout %q: %w", label, err)
		}
		if err := installer.Remove(label); err != nil {
			return result, fmt.Errorf("remove plist %q: %w", label, err)
		}
		result.Removed = append(result.Removed, label)
	}

	return result, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/launchd/... -v`
Expected: PASS, the entire `internal/launchd` package (all tasks 2-7's tests together).

- [ ] **Step 6: Commit**

```bash
git add internal/launchd/fake.go internal/launchd/sync.go internal/launchd/sync_test.go
git commit -m "feat(launchd): add FakeInstaller and the Sync reconciliation choreography"
```

---

### Task 8: `snapback schedule sync` CLI command

**Files:**
- Create: `internal/cli/schedule.go`
- Modify: `internal/cli/root.go` (register the command)
- Test: `internal/cli/schedule_internal_test.go`

**Interfaces:**
- Consumes: `launchd.Sync`, `launchd.SyncResult`, `launchd.Installer`, `launchd.NewLaunchctlInstaller` (Task 6/7).
- Produces: `printSyncResult(out io.Writer, result launchd.SyncResult) error` and `syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), vms []config.VM) error`, both reused by Task 9 (`vm add`/`vm remove`) and Task 10 (`init`).

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/schedule_internal_test.go`:

```go
// Package cli (internal test package) so this file can call
// newScheduleSyncCmdWithDeps directly with fake deps -- no real config
// file or launchctl needed.
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
)

func newTestRootForSchedule(t *testing.T, deps scheduleDeps) *cobra.Command {
	t.Helper()
	scheduleCmd := &cobra.Command{Use: "schedule"}
	scheduleCmd.AddCommand(newScheduleSyncCmdWithDeps(deps))
	return swapSubcommand(t, "schedule", scheduleCmd)
}

func TestScheduleSyncCmd_NothingScheduled_PrintsNothingToDo(t *testing.T) {
	deps := scheduleDeps{
		loadConfig:   func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("stdout = %q, want \"nothing to do\"", out.String())
	}
}

func TestScheduleSyncCmd_InstallsNewSchedule(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}}}, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "installed: dev") {
		t.Errorf("stdout = %q, want \"installed: dev\"", out.String())
	}
}

func TestScheduleSyncCmd_ConfigLoadError_IsWrapped(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) { return nil, errBoom },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"load config\" context", err, errBoom)
	}
}

func TestScheduleSyncCmd_CollisionError_IsPropagated(t *testing.T) {
	deps := scheduleDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{
				{Name: "My VM!", VMX: "/vms/a.vmx", Schedule: "daily"},
				{Name: "My VM?", VMX: "/vms/b.vmx", Schedule: "weekly"},
			}}, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForSchedule(t, deps)
	root.SetArgs([]string{"schedule", "sync", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want the name collision propagated")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/... -run TestScheduleSyncCmd -v`
Expected: FAIL to build (`scheduleDeps`, `newScheduleSyncCmdWithDeps` don't exist yet).

- [ ] **Step 3: Implement `internal/cli/schedule.go`**

```go
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/launchd"
)

// scheduleDeps groups schedule sync's external dependencies so tests can
// substitute a fake config loader and a fake launchd.Installer instead
// of touching the real filesystem or launchctl.
type scheduleDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	newInstaller func() (launchd.Installer, error)
	executable   func() (string, error)
}

func defaultScheduleDeps() scheduleDeps {
	return scheduleDeps{
		loadConfig:   config.Load,
		newInstaller: defaultNewInstaller,
		executable:   os.Executable,
	}
}

// defaultNewInstaller constructs the real, launchctl-backed Installer.
// Shared by scheduleDeps, vmDeps (Task 9), and initDeps (Task 10) so all
// three call sites construct it identically.
func defaultNewInstaller() (launchd.Installer, error) {
	return launchd.NewLaunchctlInstaller()
}

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage launchd scheduling for configured VMs",
	}
	cmd.AddCommand(newScheduleSyncCmdWithDeps(defaultScheduleDeps()))
	return cmd
}

func newScheduleSyncCmdWithDeps(deps scheduleDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile installed launchd schedules with config.yaml",
		Long:  "Installs, updates, or removes each VM's LaunchAgent to match its config.yaml `schedule` field. Safe to re-run any time -- the main use is recovering after `schedule` is hand-edited outside `vm add`/`vm remove`/`init`, which auto-sync on their own.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runScheduleSync(cmd, deps)
		},
	}
	return cmd
}

func runScheduleSync(cmd *cobra.Command, deps scheduleDeps) error {
	cfg, _, err := loadConfigForCmd(cmd, deps.loadConfig)
	if err != nil {
		return err
	}
	return syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs)
}

// syncSchedules connects to launchd, resolves the running binary's path,
// runs launchd.Sync, and prints the result -- shared by `schedule sync`
// and the auto-sync call sites in `vm add`/`vm remove`/`init` (Tasks
// 9-10) so there's exactly one place that does this, not four.
func syncSchedules(cmd *cobra.Command, newInstaller func() (launchd.Installer, error), executable func() (string, error), vms []config.VM) error {
	installer, err := newInstaller()
	if err != nil {
		return fmt.Errorf("connect to launchd: %w", err)
	}
	binaryPath, err := executable()
	if err != nil {
		return fmt.Errorf("resolve snapback binary path: %w", err)
	}

	result, err := launchd.Sync(installer, vms, binaryPath)
	if err != nil {
		return fmt.Errorf("sync launchd schedules: %w", err)
	}
	return printSyncResult(cmd.OutOrStdout(), result)
}

func printSyncResult(out io.Writer, result launchd.SyncResult) error {
	if result.IsEmpty() {
		_, err := fmt.Fprintln(out, "nothing to do")
		return err
	}
	for _, name := range result.Installed {
		if _, err := fmt.Fprintf(out, "installed: %s\n", name); err != nil {
			return err
		}
	}
	for _, name := range result.Updated {
		if _, err := fmt.Fprintf(out, "updated: %s\n", name); err != nil {
			return err
		}
	}
	for _, label := range result.Removed {
		if _, err := fmt.Fprintf(out, "removed: %s\n", label); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Register `schedule` on the root command**

In `internal/cli/root.go`, add `newScheduleCmd(),` to the `root.AddCommand(...)` list (alongside `newRestoreCmd()`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run TestScheduleSyncCmd -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/schedule.go internal/cli/root.go internal/cli/schedule_internal_test.go
git commit -m "feat(cli): add snapback schedule sync"
```

---

### Task 9: Auto-sync in `vm add` / `vm remove`

**Files:**
- Modify: `internal/cli/vm.go`
- Modify: `internal/cli/vm_internal_test.go`

**Interfaces:**
- Consumes: `syncSchedules` (Task 8).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Add `newInstaller`/`executable` to `vmDeps` and wire the calls**

In `internal/cli/vm.go`, add two fields to `vmDeps`:

```go
type vmDeps struct {
	loadConfig   func(path string) (*config.Config, error)
	marshal      func(cfg *config.Config) ([]byte, error)
	writeFile    func(path string, data []byte) error
	searchDirs   func() []string
	discoverVMs  func(searchDirs []string) ([]discoveredVM, error)
	isTerminal   func(w io.Writer) bool
	isTerminalIn func(r io.Reader) bool
	addVMs       func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate) ([]config.VM, error)
	newInstaller func() (launchd.Installer, error)
	executable   func() (string, error)
}
```

Add `"github.com/xortim/snapback/internal/launchd"` to the import block.

In `defaultVMDeps()`, add:

```go
		newInstaller: defaultNewInstaller,
		executable:   os.Executable,
```

(add `"os"` to the import block too).

In `runVMAdd`, right after the existing `_, err = fmt.Fprintf(out, "added %d VM(s), wrote config to %s\n", len(added), configPath)` line's preceding `writeFile` success (i.e., insert *before* that final `Fprintf`, immediately after the `deps.writeFile(configPath, data)` error check):

```go
	if deps.newInstaller != nil {
		if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs); err != nil {
			return err
		}
	}
```

The `deps.newInstaller != nil` guard is deliberate, not a placeholder: it mirrors the existing nil-safe-optional-dependency pattern `run.go` already uses for `isTerminal`/`runInteractive` (`internal/cli/run.go`'s `if deps.isTerminal != nil && ... && deps.runInteractive != nil`). `defaultVMDeps()` always sets it in production; leaving it nil in a test that doesn't care about scheduling behavior (most of `vm_internal_test.go`'s existing tests) means that test's `vmDeps{...}` literal doesn't need touching at all.

In `runVMRemove`, make the identical addition right after the `deps.writeFile(configPath, data)` error check, before the final `_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %q, wrote config to %s\n", name, configPath)` line, passing `cfg.VMs` (already the post-removal list).

- [ ] **Step 2: Add `newInstaller`/`executable` to the two existing tests that reach the write path**

In `internal/cli/vm_internal_test.go`, add the import `"github.com/xortim/snapback/internal/launchd"`.

In `fakeVMDeps` (used by `TestVMAddCmd_AppendsAddedVMsAndWrites`, among others), add two fields:

```go
func fakeVMDeps(cfg *config.Config, candidates []discoveredVM, added []config.VM, written *[]byte, writtenPath *string) vmDeps {
	return vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return cfg, nil },
		marshal:     config.Marshal,
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return candidates, nil },
		writeFile: func(path string, data []byte) error {
			*writtenPath = path
			*written = data
			return nil
		},
		isTerminal: func(io.Writer) bool { return false },
		addVMs: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) ([]config.VM, error) {
			return added, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
}
```

In `TestVMRemoveCmd_RemovesNamedVMAndWrites`'s `vmDeps{...}` literal, add the same two fields:

```go
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
```

- [ ] **Step 3: Run the existing suite to confirm nothing else broke**

Run: `go test ./internal/cli/... -run 'TestVMAddCmd|TestVMRemoveCmd' -v`
Expected: PASS, every existing test (the ones that never reach the write path still have `newInstaller == nil` and skip sync entirely; the two updated ones now also sync against a `FakeInstaller`).

- [ ] **Step 4: Write new tests proving sync actually runs on the happy path**

Append to `internal/cli/vm_internal_test.go`:

```go
func TestVMAddCmd_SyncsLaunchdScheduleForAddedVM(t *testing.T) {
	added := []config.VM{{Name: "new-vm", VMX: "/vms/new-vm.vmx", Schedule: "daily"}}
	deps := vmDeps{
		loadConfig:  func(string) (*config.Config, error) { return &config.Config{Destination: "/dest"}, nil },
		marshal:     config.Marshal,
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		writeFile:   func(string, []byte) error { return nil },
		isTerminal:  func(io.Writer) bool { return false },
		addVMs: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate) ([]config.VM, error) {
			return added, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "add", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "installed: new-vm") {
		t.Errorf("stdout = %q, want \"installed: new-vm\" from the auto-sync", out.String())
	}
}

func TestVMRemoveCmd_SyncsLaunchdScheduleAfterRemoval(t *testing.T) {
	deps := vmDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{
				Destination: "/dest",
				VMs:         []config.VM{{Name: "remove-me", VMX: "/vms/remove.vmx", Schedule: "daily"}},
			}, nil
		},
		marshal:      config.Marshal,
		writeFile:    func(string, []byte) error { return nil },
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForVM(t, deps)
	root.SetArgs([]string{"vm", "remove", "remove-me", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("stdout = %q, want \"nothing to do\" -- the removed VM was never bootstrapped by this fake installer, so there's nothing to boot out", out.String())
	}
}
```

Note on the second test: it deliberately asserts `"nothing to do"`, not a `"removed:"` line -- a fresh `FakeInstaller` has nothing in its `List()` yet (no prior `Sync` populated it), so removing a VM whose plist was never actually installed on *this* installer is correctly a no-op. This exercises the same code path a real removal would (the sync call happens and completes without error) without needing to fabricate pre-existing installer state.

- [ ] **Step 5: Run to verify the new tests pass**

Run: `go test ./internal/cli/... -run 'TestVMAddCmd_SyncsLaunchdScheduleForAddedVM|TestVMRemoveCmd_SyncsLaunchdScheduleAfterRemoval' -v`
Expected: PASS.

- [ ] **Step 6: Run the whole `internal/cli` suite**

Run: `go test ./internal/cli/... -v`
Expected: PASS, no regressions.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/vm.go internal/cli/vm_internal_test.go
git commit -m "feat(cli): auto-sync launchd schedules from vm add/vm remove"
```

---

### Task 10: Auto-sync in `init`

**Files:**
- Modify: `internal/cli/init.go`
- Modify: `internal/cli/init_internal_test.go`

**Interfaces:**
- Consumes: `syncSchedules` (Task 8).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Add `newInstaller`/`executable` to `initDeps` and wire the call**

In `internal/cli/init.go`, add two fields to `initDeps` (same shape as Task 9's `vmDeps` additions):

```go
type initDeps struct {
	searchDirs   func() []string
	discoverVMs  func(searchDirs []string) ([]discoveredVM, error)
	loadConfig   func(path string) (*config.Config, error)
	marshal      func(cfg *config.Config) ([]byte, error)
	writeFile    func(path string, data []byte) error
	fileExists   func(path string) bool
	isTerminal   func(w io.Writer) bool
	isTerminalIn func(r io.Reader) bool
	runWizard    func(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []tui.VMCandidate, prior *config.Config) (*config.Config, error)
	newInstaller func() (launchd.Installer, error)
	executable   func() (string, error)
}
```

Add `"github.com/xortim/snapback/internal/launchd"` and `"os"` (if not already imported -- `init.go` already imports `"os"`) to the import block.

In `newInitCmd()`'s `initDeps{...}` literal, add:

```go
		newInstaller: defaultNewInstaller,
		executable:   os.Executable,
```

In `runInit`, right after the `deps.writeFile(configPath, data)` error check and before the final `_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)`:

```go
	if deps.newInstaller != nil {
		if err := syncSchedules(cmd, deps.newInstaller, deps.executable, cfg.VMs); err != nil {
			return err
		}
	}
```

Same nil-guard rationale as Task 9: every pre-existing test in `init_internal_test.go` that reaches the write-success path (there are several -- `fakeInitDeps`, plus a number of literal `initDeps{...}` constructions across the file) leaves `newInstaller` unset and continues exercising the pre-this-feature no-sync behavior unchanged. Only the one new test below needs to set it.

- [ ] **Step 2: Run the existing suite to confirm nothing broke**

Run: `go test ./internal/cli/... -run TestInitCmd -v`
Expected: PASS, every existing test unchanged.

- [ ] **Step 3: Write a new test proving sync runs on the happy path**

Append to `internal/cli/init_internal_test.go`:

```go
func TestInitCmd_SyncsLaunchdSchedules(t *testing.T) {
	cfg := &config.Config{
		Destination: "/dest",
		Compression: "zstd",
		VMs:         []config.VM{{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "daily"}},
	}
	var written []byte
	var writtenPath string
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		loadConfig:  func(string) (*config.Config, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile: func(path string, data []byte) error {
			writtenPath = path
			written = data
			return nil
		},
		fileExists:   func(string) bool { return false },
		isTerminal:   func(io.Writer) bool { return false },
		isTerminalIn: func(io.Reader) bool { return false },
		runWizard: func(context.Context, io.Reader, io.Writer, bool, []tui.VMCandidate, *config.Config) (*config.Config, error) {
			return cfg, nil
		},
		newInstaller: func() (launchd.Installer, error) { return launchd.NewFakeInstaller(), nil },
		executable:   func() (string, error) { return "/bin/snapback", nil },
	}
	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Fatalf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	_ = written
	if !strings.Contains(out.String(), "installed: dev") {
		t.Errorf("stdout = %q, want \"installed: dev\" from the auto-sync", out.String())
	}
}
```

(`newTestRootForInit` is defined at `internal/cli/init_internal_test.go:20`, mirroring `vm_internal_test.go`'s `newTestRootForVM` -- both wrap `swapSubcommand`.)

- [ ] **Step 4: Run to verify the new test passes**

Run: `go test ./internal/cli/... -run TestInitCmd_SyncsLaunchdSchedules -v`
Expected: PASS.

- [ ] **Step 5: Run the whole `internal/cli` suite**

Run: `go test ./internal/cli/... -v`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/init.go internal/cli/init_internal_test.go
git commit -m "feat(cli): auto-sync launchd schedules from init"
```

---

### Task 11: Log rotation wired into `run`

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_internal_test.go`

**Interfaces:**
- Consumes: `launchd.LogPath`, `launchd.RotateIfOversized` (Task 4/5).
- Produces: nothing new for later tasks.

- [ ] **Step 1: Add rotation to `runVM`**

In `internal/cli/run.go`, add `"github.com/xortim/snapback/internal/launchd"` to the import block. In `runVM`, right after `vmCfg, ok := findVMConfig(cfg.VMs, vmName)` resolves successfully (before `ctrl, err := deps.newController()`), add:

```go
	if logPath, err := launchd.LogPath(vmCfg.Name); err == nil {
		if err := launchd.RotateIfOversized(logPath); err != nil {
			// Rotation failing is never a reason to skip the actual
			// backup -- warn and continue, same posture warnIfMaybeOrphaned
			// already takes for a non-fatal, best-effort side channel.
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not rotate log %s: %v\n", logPath, err)
		}
	}
```

`launchd.LogPath`'s own error (home directory undeterminable) is likewise non-fatal here -- silently skipping rotation in that edge case rather than failing the whole backup over a housekeeping step.

- [ ] **Step 2: Run the existing suite to confirm nothing broke**

Run: `go test ./internal/cli/... -run TestRunCmd -v`
Expected: PASS. `launchd.LogPath`/`RotateIfOversized` touch `os.UserHomeDir()` and a real (but tiny, and almost always absent) file under `~/Library/Logs/snapback/` -- confirm this doesn't fail in CI's sandboxed `$HOME`; if `os.UserHomeDir()` errors in that environment, the code above already treats it as a no-op, so this should be a non-issue, but verify by actually running the suite rather than assuming.

- [ ] **Step 3: Write a test confirming rotation is attempted**

Add to `internal/cli/run_internal_test.go`, which already imports `"os"` and `"path/filepath"`:

```go
func TestRunCmd_RotatesOversizedLogBeforeBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logDir := filepath.Join(home, "Library", "Logs", "snapback")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	logPath := filepath.Join(logDir, "dev.log")
	if err := os.WriteFile(logPath, make([]byte, 6*1024*1024), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deps := runDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Destination: "/dest", VMs: []config.VM{{Name: "dev", VMX: "/vms/dev.vmx"}}}, nil
		},
		newController: func() (vm.Controller, error) { return vm.NewFakeVMController(), nil },
		isTerminal:    func(io.Writer) bool { return false },
	}
	root := newTestRoot(t, deps)
	root.SetArgs([]string{"run", "--vm", "dev", "--config", "/cfg/config.yaml"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	_ = root.Execute() // the fake controller/backup.Run may or may not succeed fully; rotation happens before that regardless

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("oversized log at %s still exists after run, want it rotated to %s.1", logPath, logPath)
	}
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Errorf(".1 rotated log missing: %v", err)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/... -run TestRunCmd_RotatesOversizedLogBeforeBackup -v`
Expected: PASS.

- [ ] **Step 5: Run the whole `internal/cli` suite**

Run: `go test ./internal/cli/... -v`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/run.go internal/cli/run_internal_test.go
git commit -m "feat(cli): rotate scheduled-run logs before each run"
```

---

### Task 12: Wizard changes — drop nightly/custom, add monthly

**Files:**
- Modify: `internal/tui/init_schedule.go` (full rewrite, it's small)
- Modify: `internal/tui/init_schedule_test.go` (full rewrite)
- Modify: `internal/tui/init.go` (`promptSchedules`)
- Modify: `internal/tui/init_test.go` (several existing tests)
- Modify: `internal/tui/vmadd_test.go` (several existing tests)
- Modify: `internal/tui/init_validate.go` (delete `validateCronExpression`)
- Modify: `internal/tui/init_validate_test.go` (delete `TestValidateCronExpression`)

**Interfaces:**
- Consumes: nothing from earlier tasks (independent of `internal/launchd` entirely).
- Produces: `config.VM.Schedule` values from the wizard now always match Task 1's enum.

- [ ] **Step 1: Rewrite `internal/tui/init_schedule.go`**

```go
package tui

const (
	scheduleChoiceNone    = "none"
	scheduleChoiceDaily   = "daily"
	scheduleChoiceWeekly  = "weekly"
	scheduleChoiceMonthly = "monthly"
)

// scheduleChoices lists the schedule presets offered per VM, in display
// order. Each choice's string value doubles as the config.VM.Schedule
// value it resolves to (see resolveSchedule) -- ADR-005
// (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md)
// narrowed Schedule from free-form cron syntax to this closed enum,
// matching cron's own @daily/@weekly/@monthly meta-schedules (fixed
// midnight-based times, no time-of-day override). "none" is the only
// choice that isn't also a valid Schedule value; resolveSchedule maps it
// to "".
var scheduleChoices = []string{scheduleChoiceNone, scheduleChoiceDaily, scheduleChoiceWeekly, scheduleChoiceMonthly}

// resolveSchedule turns a schedule preset choice (one of scheduleChoices)
// into the string stored on config.VM.Schedule: a direct passthrough for
// every choice except "none", which resolves to "" (unscheduled).
func resolveSchedule(choice string) string {
	if choice == scheduleChoiceNone {
		return ""
	}
	return choice
}
```

- [ ] **Step 2: Rewrite `internal/tui/init_schedule_test.go`**

```go
package tui

import "testing"

func TestResolveSchedule(t *testing.T) {
	tests := []struct {
		name   string
		choice string
		want   string
	}{
		{"none", scheduleChoiceNone, ""},
		{"daily", scheduleChoiceDaily, "daily"},
		{"weekly", scheduleChoiceWeekly, "weekly"},
		{"monthly", scheduleChoiceMonthly, "monthly"},
	}
	for _, tt := range tests {
		if got := resolveSchedule(tt.choice); got != tt.want {
			t.Errorf("%s: resolveSchedule(%q) = %q, want %q", tt.name, tt.choice, got, tt.want)
		}
	}
}

func TestScheduleChoices_ListsAllFourPresetsInOrder(t *testing.T) {
	want := []string{scheduleChoiceNone, scheduleChoiceDaily, scheduleChoiceWeekly, scheduleChoiceMonthly}
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

- [ ] **Step 3: Update `promptSchedules` in `internal/tui/init.go`**

Replace the existing `promptSchedules` function (around line 303) with:

```go
// promptSchedules asks a schedule preset for each VM in vms, in order,
// mutating vms[i].Schedule in place.
func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM) error {
	for i := range vms {
		choice := scheduleChoiceNone

		err := runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Schedule for %s", vms[i].Name)).
					Options(huh.NewOptions(scheduleChoices...)...).
					Value(&choice),
			),
		)
		if err != nil {
			return err
		}
		vms[i].Schedule = resolveSchedule(choice)
	}
	return nil
}
```

This drops the second `huh.NewGroup` (the always-asked custom-cron field) entirely -- there's no "custom" choice left for it to serve, in either the interactive or accessible-mode form runner.

- [ ] **Step 4: Delete `validateCronExpression` from `internal/tui/init_validate.go`**

Remove the `validateCronExpression` function and its doc comment entirely (nothing else in the package calls it after Step 3).

- [ ] **Step 5: Delete `TestValidateCronExpression` from `internal/tui/init_validate_test.go`**

Remove the whole `TestValidateCronExpression` function.

- [ ] **Step 6: Update `internal/tui/init_test.go`'s schedule-prompt tests**

`TestPromptSchedules_DefaultIsNone` (around line 383): change `in := strings.NewReader("\n\n")` to `strings.NewReader("\n")` and its comment to `// schedule select blank (default "none").`

`TestPromptSchedules_Nightly` (around line 397): rename to `TestPromptSchedules_Daily`, change `in := strings.NewReader("2\n\n")` to `strings.NewReader("2\n")`, its comment to `// "2" selects "daily" (scheduleChoices[1]).`, and the assertion `vms[0].Schedule != cronNightly` to `vms[0].Schedule != "daily"`.

`TestPromptSchedules_CustomCron_InvalidThenValid` (around line 411): delete this whole test function -- there's no more "custom" choice or cron validation path for it to exercise.

`TestPromptSchedules_MultipleVMs_AskedInOrder` (around line 429): change `in := strings.NewReader("\n\n3\n\n")` to `strings.NewReader("\n3\n")`, its comment to `// dev: blank (none). prod: "3" (weekly).`, and the assertion `vms[1].Schedule != cronWeekly` to `vms[1].Schedule != "weekly"`.

`TestRunInitWizard_EndToEnd_DiscoveredVMWithDefaults` (around line 487): the schedule step now consumes one blank instead of two, so the scripted input string loses exactly one `\n`. Change:

```go
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n\n")
```

to:

```go
	in := strings.NewReader("0\nn\n\n\n\n\n\n\n\n\n")
```

and update the comment above it from `// Schedule (1 VM): 2 blanks (choice=none, custom=unused).` to `// Schedule (1 VM): 1 blank (choice=none).`. After editing, run the test (Step 8 below) -- if the exact blank count is off by one, the test will fail clearly (wrong `cfg.VMs`/error) rather than silently passing wrong, so treat that as the ground truth over the count reasoning here.

- [ ] **Step 7: Update `internal/tui/vmadd_test.go`'s schedule-prompt tests**

`TestAddVMs_SelectsDiscoveredAndSetsSchedule`: change `in := strings.NewReader("0\nn\n2\n\n")` to `strings.NewReader("0\nn\n2\n")`, comment to mention "daily" instead of "nightly".

`TestAddVMs_NoCandidates_PromptsManualEntry`: change `in := strings.NewReader("\ndevbox\n/vms/devbox.vmx\nn\n\n\n")` to `strings.NewReader("\ndevbox\n/vms/devbox.vmx\nn\n\n")` (drop the trailing custom-cron blank).

`TestAddVMs_ReturnsPlainConfigVMs`: change `in := strings.NewReader("0\nn\n\n\n")` to `strings.NewReader("0\nn\n\n")`.

`TestAddVMs_DuplicateSelection_FailsBeforeSchedulePrompt` and `TestAddVMs_SelectVMsError_IsPropagated`: no change (both fail before reaching the schedule prompt).

- [ ] **Step 8: Run the whole `internal/tui` suite**

Run: `go test ./internal/tui/... -v`
Expected: PASS. If any of the accessible-mode input-string edits in Steps 6-7 are off by one blank line, the specific failing test's error output will show the actual vs. expected `Schedule`/error, making the exact fix obvious -- adjust the `\n` count for that one test and re-run rather than re-deriving all of them from scratch.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/init_schedule.go internal/tui/init_schedule_test.go internal/tui/init.go internal/tui/init_test.go internal/tui/vmadd_test.go internal/tui/init_validate.go internal/tui/init_validate_test.go
git commit -m "feat(tui): replace nightly/custom schedule presets with daily/weekly/monthly"
```

---

### Final verification (whole repo)

- [ ] Run `make lint` — expect no findings.
- [ ] Run `make test` — expect all packages PASS, including `internal/config`, `internal/launchd`, `internal/cli`, `internal/tui`.
- [ ] Run `make build` — expect a binary at `dist/<goos-goarch>/snapback`.
- [ ] Run `go build -tags=integration ./...` — expect a clean build (integration test files compile even though they don't execute without `SNAPBACK_INTEGRATION=1`).
- [ ] Manually smoke-test on a real macOS machine (not CI): `snapback vm add` a VM with a `daily` schedule, confirm a plist appears at `~/Library/LaunchAgents/com.tim.snapback.<name>.plist` and `launchctl print gui/$(id -u)/com.tim.snapback.<name>` shows it loaded; `snapback vm remove <name>` and confirm both are gone; `snapback schedule sync` after hand-editing `schedule:` in `config.yaml` and confirm it picks up the change.
