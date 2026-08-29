# PR #26 Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all five findings from the `/code-review 26` report on `feat(cli): wire run --vm to the backup choreography` (PR #26): two correctness bugs, one message-quality bug, and two test-duplication cleanups.

**Architecture:** Each finding is fixed in place with a matching unit test, in five small commits. No new packages or abstractions are introduced — this is targeted cleanup of `internal/cli/run.go`, `internal/cli/root.go`, and `internal/cli/run_internal_test.go`.

**Tech Stack:** Go 1.26, `github.com/spf13/cobra` v1.10.2, standard `testing` package (table-driven tests, `t.Setenv`, `t.TempDir`).

**Spec:** `/code-review 26` findings (reproduced below; no separate spec doc exists for this fix set).

```
1. internal/cli/run.go:39 — run doesn't set SilenceUsage, so cobra dumps the
   full Usage/Flags block after every operational error, not just flag-misuse
   errors.
2. internal/cli/root.go:34 — defaultConfigPath() silently falls back to a
   CWD-relative "config.yaml" when os.UserHomeDir() fails, with no warning.
3. internal/cli/run.go:59 — config load error wraps the path a second time
   even though config.Load's underlying error already contains it.
4. internal/cli/run_internal_test.go:22 — newTestRoot hand-duplicates the
   --config persistent flag definition instead of building on
   cli.NewRootCmd().
5. internal/cli/run_internal_test.go:63 — TestRunCmd_ConfigLoadError_IsWrapped
   and TestRunCmd_ControllerConnectError_IsWrapped are near-identical
   copy-paste that could be a table-driven test.
```

## Global Constraints

- Go 1.26 toolchain (`go.mod` says `go 1.26.5`); build/test with the `go` on `PATH` (verified as go1.27.0, which satisfies the 1.26 floor).
- Run `golangci-lint run` (via `make lint`) before considering any task done, since CI enforces it — fix any new findings it raises rather than suppressing them.
- Test files in `internal/cli` use two package styles: `run_internal_test.go` is `package cli` (white-box, can call unexported functions); `root_test.go` is `package cli_test` (black-box). Any new test of an unexported function (`defaultConfigPathFor`) must go in a `package cli` file.
- Preserve all currently-passing behavior in `internal/cli/run_internal_test.go` and `internal/cli/root_test.go` — no test in this plan should regress an existing one.

---

## File Structure

- Modify `internal/cli/root.go` — add a testable, warn-on-failure `defaultConfigPathFor(io.Writer)` helper behind the existing `defaultConfigPath()`.
- Create `internal/cli/root_internal_test.go` — white-box tests for `defaultConfigPathFor`, mirroring the `_internal_test.go` naming convention already used for `run.go`.
- Modify `internal/cli/run.go` — set `cmd.SilenceUsage = true` at the top of `RunE`; stop double-wrapping the config path in the load-error message.
- Modify `internal/cli/run_internal_test.go` — rebuild `newTestRoot` on top of `NewRootCmd()`; add/adjust tests for the two `run.go` behavior fixes; collapse the two near-identical error-wrapping tests into one table-driven test.

## Task 1: `root.go` — warn when the home directory can't be resolved

**Files:**
- Modify: `internal/cli/root.go:29-37`
- Test: `internal/cli/root_internal_test.go` (new file)

**Interfaces:**
- Produces: `defaultConfigPathFor(warnOut io.Writer) string` — same resolution logic as today, but writes a one-line warning to `warnOut` and returns `"config.yaml"` when `os.UserHomeDir()` fails, instead of failing silently. `defaultConfigPath()` keeps its existing no-arg signature and calls `defaultConfigPathFor(os.Stderr)`, so `NewRootCmd()` (Task 4's `newTestRoot` included) needs no changes.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/root_internal_test.go`:

```go
package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigPathFor_HomeDirResolves(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)

	var warn bytes.Buffer
	path := defaultConfigPathFor(&warn)

	want := filepath.Join(home, ".config", "snapback", "config.yaml")
	if path != want {
		t.Errorf("defaultConfigPathFor() = %q, want %q", path, want)
	}
	if warn.Len() != 0 {
		t.Errorf("defaultConfigPathFor() wrote a warning = %q, want none when HOME resolves", warn.String())
	}
}

func TestDefaultConfigPathFor_HomeDirUnresolved_WarnsAndFallsBack(t *testing.T) {
	t.Setenv("HOME", "")

	var warn bytes.Buffer
	path := defaultConfigPathFor(&warn)

	if path != "config.yaml" {
		t.Errorf("defaultConfigPathFor() = %q, want fallback %q", path, "config.yaml")
	}
	if !strings.Contains(warn.String(), "warning") {
		t.Errorf("defaultConfigPathFor() warning = %q, want it to mention the fallback", warn.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/... -run TestDefaultConfigPathFor -v`
Expected: FAIL with `undefined: defaultConfigPathFor`

- [ ] **Step 3: Implement `defaultConfigPathFor`**

Replace `internal/cli/root.go` lines 1-37 with:

```go
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "snapback",
		Short: "Zero-downtime backup manager for VMware Fusion VMs",
	}

	root.PersistentFlags().String("config", defaultConfigPath(), "path to config file")

	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newListCmd(),
		newStatusCmd(),
	)

	return root
}

// defaultConfigPath returns ~/.config/snapback/config.yaml, falling back to
// a relative path (with a warning on stderr) if the home directory can't be
// determined.
func defaultConfigPath() string {
	return defaultConfigPathFor(os.Stderr)
}

// defaultConfigPathFor implements defaultConfigPath, taking the warning
// output as a parameter so tests can capture it without touching os.Stderr.
func defaultConfigPathFor(warnOut io.Writer) string {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(warnOut, "warning: could not determine home directory (%v); using relative config.yaml as the default --config path\n", err)
		return "config.yaml"
	}
	return filepath.Join(home, ".config", "snapback", "config.yaml")
}
```

Leave the rest of `root.go` (`newInitCmd`, `newListCmd`, `newStatusCmd`, `errNotImplemented`) unchanged below this block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run TestDefaultConfigPathFor -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS (all existing tests, including `TestNewRootCmd_HasExpectedSubcommands` and `TestNewRootCmd_Name`, still pass)

- [ ] **Step 6: Commit**

```bash
git add internal/cli/root.go internal/cli/root_internal_test.go
git commit -m "fix(cli): warn instead of silently falling back when HOME can't be resolved"
```

## Task 2: `run.go` — stop double-wrapping the config path in load errors

**Files:**
- Modify: `internal/cli/run.go:57-60`
- Test: `internal/cli/run_internal_test.go`

**Interfaces:**
- Consumes: `deps.loadConfig func(path string) (*config.Config, error)` (existing `runDeps` field, unchanged).
- Produces: no new symbols; `runVM`'s error message for a config-load failure changes from `"load config %s: %w"` (duplicating the path already embedded in most real errors) to `"load config: %w"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/run_internal_test.go` (near the other `ConfigLoadError` test; needs `path/filepath`, already imported):

```go
func TestRunCmd_ConfigLoadError_DoesNotDuplicatePath(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: config.Load,
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	root.SetArgs([]string{"run", "--vm", "myvm", "--config", missingPath})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want a config load error")
	}
	if got := strings.Count(err.Error(), missingPath); got != 1 {
		t.Errorf("Execute() error = %q, want the config path to appear exactly once, got %d occurrences", err.Error(), got)
	}
}
```

This uses the real `config.Load` (not a stub) so the underlying error genuinely embeds the path, reproducing what the reviewer saw.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestRunCmd_ConfigLoadError_DoesNotDuplicatePath -v`
Expected: FAIL — `got 2 occurrences` (path appears once from `deps.loadConfig`'s underlying `fs.PathError` and once from the `%s` in `runVM`'s wrap)

- [ ] **Step 3: Fix the wrap in `runVM`**

In `internal/cli/run.go`, change:

```go
	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", configPath, err)
	}
```

to:

```go
	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/... -run TestRunCmd_ConfigLoadError_DoesNotDuplicatePath -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS — in particular `TestRunCmd_ConfigLoadError_IsWrapped` still passes, since it only asserts `errors.Is(err, errBoom)`, not the message text.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/run.go internal/cli/run_internal_test.go
git commit -m "fix(cli): stop duplicating the config path in load-config errors"
```

## Task 3: `run_internal_test.go` — build `newTestRoot` on `NewRootCmd()`

**Files:**
- Modify: `internal/cli/run_internal_test.go:22-31`

**Interfaces:**
- Produces: `newTestRoot(deps runDeps) *cobra.Command` — same signature and same externally-observable behavior (a root command with a `--config` persistent flag and a `run` subcommand wired to `deps`), but now built from the real `NewRootCmd()` (with its real `init`/`list`/`status` subcommands and real default `--config` value) instead of a hand-rolled stand-in, so it can't drift from `root.go`.

- [ ] **Step 1: Replace `newTestRoot`**

In `internal/cli/run_internal_test.go`, replace:

```go
// newTestRoot builds a minimal parent command carrying the persistent
// --config flag NewRootCmd() normally registers, with the run subcommand
// (built from deps) attached -- mirrors how run.go's runVM reads --config
// via cmd.Flags().GetString, without needing every other real subcommand.
func newTestRoot(deps runDeps) *cobra.Command {
	root := &cobra.Command{Use: "snapback"}
	root.PersistentFlags().String("config", "unused.yaml", "path to config file")
	root.AddCommand(newRunCmdWithDeps(deps))
	return root
}
```

with:

```go
// newTestRoot builds the real root command via NewRootCmd() -- so the
// --config persistent flag and everything else run.go's runVM depends on
// can't drift from root.go -- then swaps in a run subcommand wired to a
// fake deps.
func newTestRoot(deps runDeps) *cobra.Command {
	root := NewRootCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "run" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newRunCmdWithDeps(deps))
	return root
}
```

- [ ] **Step 2: Run the full package test suite to verify no regressions**

Run: `go test ./internal/cli/... -v`
Expected: PASS — every existing `TestRunCmd_*` test still passes unchanged, since `newTestRoot`'s external behavior (a `--config` flag plus a `run` subcommand bound to `deps`) is preserved.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/run_internal_test.go
git commit -m "test(cli): build newTestRoot on NewRootCmd() instead of duplicating flag setup"
```

## Task 4: `run.go` — suppress usage output for operational errors

**Files:**
- Modify: `internal/cli/run.go:39-42`
- Test: `internal/cli/run_internal_test.go`

**Interfaces:**
- Produces: no new symbols. `newRunCmdWithDeps`'s `RunE` now sets `cmd.SilenceUsage = true` as its first statement, before calling `runVM`. Because cobra checks required-flag validation *before* invoking `RunE`, a missing `--vm` still prints usage (that error path never reaches the `SilenceUsage = true` line); any error `runVM` itself returns (bad config, unknown VM, controller connect failure, backup failure) now suppresses usage since it happens after the line runs.

- [ ] **Step 1: Write the failing tests**

In `internal/cli/run_internal_test.go`, change `TestRunCmd_MissingVMFlag_ReturnsError` to capture and assert on stdout:

```go
func TestRunCmd_MissingVMFlag_ReturnsError(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig:    func(string) (*config.Config, error) { t.Fatal("loadConfig should not be called"); return nil, nil },
		newController: func() (vm.Controller, error) { t.Fatal("newController should not be called"); return nil, nil },
	})
	root.SetArgs([]string{"run"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing required --vm flag")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout = %q, want usage text for a flag-misuse error", out.String())
	}
}
```

Add a new test right after it:

```go
func TestRunCmd_ConfigLoadError_SuppressesUsage(t *testing.T) {
	root := newTestRoot(runDeps{
		loadConfig: func(path string) (*config.Config, error) { return nil, errBoom },
		newController: func() (vm.Controller, error) {
			t.Fatal("newController should not be called")
			return nil, nil
		},
	})
	root.SetArgs([]string{"run", "--vm", "myvm"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want a wrapped config load error")
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout = %q, want no usage text for an operational error", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify the new/changed assertions fail**

Run: `go test ./internal/cli/... -run 'TestRunCmd_MissingVMFlag_ReturnsError|TestRunCmd_ConfigLoadError_SuppressesUsage' -v`
Expected: `TestRunCmd_MissingVMFlag_ReturnsError` PASSes already (usage was always printed for flag errors); `TestRunCmd_ConfigLoadError_SuppressesUsage` FAILs because `out` currently contains `Usage:`.

- [ ] **Step 3: Set `SilenceUsage` in `RunE`**

In `internal/cli/run.go`, change:

```go
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a backup",
		Long:  "Run a zero-downtime backup of one VM named on the command line. Backing up every configured VM (`run --all`) is not yet implemented.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVM(cmd, deps, vmName)
		},
	}
```

to:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run 'TestRunCmd_MissingVMFlag_ReturnsError|TestRunCmd_ConfigLoadError_SuppressesUsage' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS

- [ ] **Step 6: Manually reproduce the original failure scenario is fixed**

Run: `go run ./cmd/snapback run --vm doesnotexist --config /nonexistent/config.yaml`
Expected: prints only the error (e.g. `Error: load config: open /nonexistent/config.yaml: no such file or directory`), with no trailing `Usage:`/`Flags:` block.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/run.go internal/cli/run_internal_test.go
git commit -m "fix(cli): silence usage output for run's operational errors"
```

## Task 5: `run_internal_test.go` — collapse the two wrapped-error tests into one table-driven test

**Files:**
- Modify: `internal/cli/run_internal_test.go`

**Interfaces:**
- Consumes: `newTestRoot` (Task 3), `errBoom`, `runDeps`, `config.Config`, `config.VM`, `vm.Controller` — all pre-existing.
- Produces: no new exported symbols; replaces `TestRunCmd_ConfigLoadError_IsWrapped`, `TestRunCmd_ControllerConnectError_IsWrapped`, and Task 4's `TestRunCmd_ConfigLoadError_SuppressesUsage` with a single `TestRunCmd_DependencyError_IsWrappedAndSuppressesUsage` covering both scenarios and both assertions (wrapped error + suppressed usage) via subtests.

- [ ] **Step 1: Replace the three tests with one table-driven test**

Remove `TestRunCmd_ConfigLoadError_IsWrapped`, `TestRunCmd_ControllerConnectError_IsWrapped`, and `TestRunCmd_ConfigLoadError_SuppressesUsage` from `internal/cli/run_internal_test.go`, and add in their place:

```go
func TestRunCmd_DependencyError_IsWrappedAndSuppressesUsage(t *testing.T) {
	tests := []struct {
		name string
		deps func(t *testing.T) runDeps
	}{
		{
			name: "config load error",
			deps: func(t *testing.T) runDeps {
				return runDeps{
					loadConfig: func(path string) (*config.Config, error) { return nil, errBoom },
					newController: func() (vm.Controller, error) {
						t.Fatal("newController should not be called")
						return nil, nil
					},
				}
			},
		},
		{
			name: "controller connect error",
			deps: func(t *testing.T) runDeps {
				return runDeps{
					loadConfig: func(path string) (*config.Config, error) {
						return &config.Config{VMs: []config.VM{{Name: "myvm"}}}, nil
					},
					newController: func() (vm.Controller, error) { return nil, errBoom },
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRoot(tt.deps(t))
			root.SetArgs([]string{"run", "--vm", "myvm"})
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want a wrapped error")
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("Execute() error = %v, want it to wrap errBoom", err)
			}
			if strings.Contains(out.String(), "Usage:") {
				t.Errorf("stdout = %q, want no usage text for an operational error", out.String())
			}
		})
	}
}
```

- [ ] **Step 2: Run the new test**

Run: `go test ./internal/cli/... -run TestRunCmd_DependencyError_IsWrappedAndSuppressesUsage -v`
Expected: PASS, with both subtests (`config_load_error`, `controller_connect_error`) shown passing.

- [ ] **Step 3: Run the full package test suite**

Run: `go test ./internal/cli/...`
Expected: PASS

- [ ] **Step 4: Run the full repo build, vet, and lint**

Run: `go build ./... && go vet ./... && golangci-lint run`
Expected: all clean (no new findings)

- [ ] **Step 5: Commit**

```bash
git add internal/cli/run_internal_test.go
git commit -m "test(cli): collapse duplicate wrapped-error tests into a table-driven test"
```

---

## Final Verification

- [ ] **Run the full suite one more time end to end**

```bash
go build ./...
go vet ./...
golangci-lint run
go test ./...
```

Expected: all green. At this point all five `/code-review 26` findings are fixed:
1. `run.go` operational errors no longer dump usage (Task 4).
2. `defaultConfigPath()` warns instead of failing silently (Task 1).
3. Config-load errors no longer duplicate the path (Task 2).
4. `newTestRoot` can't drift from `root.go`'s real flag wiring (Task 3).
5. The two near-identical wrapped-error tests are one table-driven test (Task 5).
