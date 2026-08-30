# `snapback init` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `snapback init` (tracker #6) end-to-end — interactive VM
discovery, destination/retention/compression prompts, and a written
`config.yaml` — plus the two config-layer issues it depends on: tilde
expansion (#15) and field validation (#16).

**Architecture:** `internal/config` grows three small, focused additions:
`expandTilde` (wired into `Load`), `Validate` (also wired into `Load`, so
every command gets it for free), and `Marshal` (the write-side inverse of
`Load`, used only by `init`). `internal/cli` grows a filesystem-based VM
scanner (`discoverVMs`) and the `init` command itself, which reads
scripted prompts from `cmd.InOrStdin()` line-by-line (no TUI library —
`go.yaml.in/yaml/v3` and a `bufio.Scanner` are the only new surface),
builds a `config.Config` in memory, validates it, marshals it, and writes
it to `--config`'s path. Every new piece follows the repo's established
`xDeps` / `newXCmdWithDeps` / `swapSubcommand` pattern so tests substitute
fakes instead of touching the real filesystem or `$HOME`.

**Tech Stack:** Go 1.26.5, cobra, koanf (read), `go.yaml.in/yaml/v3`
(write — already an indirect dependency via koanf's YAML parser).

**Spec:** `docs/design.md` (`docs/design.md#command-reference` for
`init`'s one-line description, `docs/design.md#config-reference` for the
YAML shape `Marshal` must produce). Tracker issues: #6 (init), #15
(tilde expansion), #16 (config validation).

## Global Constraints

- Module path `github.com/xortim/snapback`, Go 1.26.5.
- Follow the existing `xDeps` struct + `newXCmdWithDeps(deps)` +
  `newXCmd()` (real deps) pattern used by `run.go`/`list.go` — every new
  command/helper with an external dependency (filesystem, `$HOME`, stdin)
  must be substitutable in tests.
- Use `swapSubcommand` (`internal/cli/testcmd_internal_test.go`) to build
  test root commands, and the shared `errBoom` sentinel for
  dependency-failure tests — don't redeclare either.
- Unit tests only, no real VM or Fusion install required — this is all
  filesystem/stdin/stdout, consistent with the repo's fake-controller
  test philosophy.
- Verify with `make lint` and `make test` (or `go test ./... -run
  <Name> -v` for a single test) after every task; `make build` at the end
  of the plan.
- Branch naming: `feat/`-prefixed (e.g. `feat/init-command`), never
  `issue-<n>-...`. Never commit directly to `main`.
- Commits: conventional-commit types (`feat`, `test`, `docs`, ...) with
  the package as scope (e.g. `feat(config): ...`, `test(cli): ...`), not
  the package name as a bare prefix.
- Before starting Task 1, create the feature branch off latest `main`:
  `git checkout main && git pull && git checkout -b feat/init-command`.

---

### Task 1: Tilde expansion in config paths (#15)

**Files:**
- Create: `internal/config/expand.go`
- Create: `internal/config/expand_internal_test.go` (package `config` — `expandTilde` is unexported)
- Modify: `internal/config/config.go:38-53` (`Load`)
- Modify: `internal/config/config_test.go:12-59` (`TestLoad_ParsesFullConfig` — its fixture currently asserts `cfg.VMs[0].VMX` stays the literal, unexpanded `"~/Virtual Machines/..."` string; that assertion becomes wrong once `Load` expands it)

**Interfaces:**
- Produces: `expandTilde(path string) (string, error)` in package `config` — expands a leading `"~"` (alone) or `"~/"` prefix using `os.UserHomeDir()`; any other path (including `"~otheruser/..."`) is returned unchanged. Called only from `Load` in this task.

- [ ] **Step 1: Write the failing test for `expandTilde`**

```go
// internal/config/expand_internal_test.go
package config

import (
	"path/filepath"
	"testing"
)

func TestExpandTilde_ExpandsBareTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := expandTilde("~")
	if err != nil {
		t.Fatalf("expandTilde(\"~\") error = %v", err)
	}
	if got != home {
		t.Errorf("expandTilde(\"~\") = %q, want %q", got, home)
	}
}

func TestExpandTilde_ExpandsTildeSlashPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := expandTilde("~/Virtual Machines/dev.vmwarevm/dev.vmx")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	want := filepath.Join(home, "Virtual Machines/dev.vmwarevm/dev.vmx")
	if got != want {
		t.Errorf("expandTilde(...) = %q, want %q", got, want)
	}
}

func TestExpandTilde_LeavesAbsolutePathUnchanged(t *testing.T) {
	got, err := expandTilde("/Volumes/Backups/snapback")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "/Volumes/Backups/snapback" {
		t.Errorf("expandTilde(absolute) = %q, want it unchanged", got)
	}
}

func TestExpandTilde_LeavesOtherUserTildeUnchanged(t *testing.T) {
	got, err := expandTilde("~otheruser/foo")
	if err != nil {
		t.Fatalf("expandTilde error = %v", err)
	}
	if got != "~otheruser/foo" {
		t.Errorf("expandTilde(~otheruser/foo) = %q, want it unchanged (this package only resolves the current user's home)", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/... -run TestExpandTilde -v`
Expected: FAIL — `undefined: expandTilde`

- [ ] **Step 3: Implement `expandTilde`**

```go
// internal/config/expand.go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandTilde expands a leading "~" (the current user's home directory
// alone) or "~/..." prefix in path using os.UserHomeDir. Any other
// leading-tilde form (e.g. "~otheruser/...") is left untouched -- this
// package only resolves the current user's home, not arbitrary user
// lookups.
func expandTilde(path string) (string, error) {
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

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/... -run TestExpandTilde -v`
Expected: PASS (all 4 subtests)

- [ ] **Step 5: Wire `expandTilde` into `Load`, and update the fixture test it breaks**

Edit `internal/config/config.go` (`Load`, currently returns right after
`k.Unmarshal`):

```go
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	expandedDest, err := expandTilde(cfg.Destination)
	if err != nil {
		return nil, fmt.Errorf("parse %s: expand destination: %w", path, err)
	}
	cfg.Destination = expandedDest

	for i, vm := range cfg.VMs {
		expandedVMX, err := expandTilde(vm.VMX)
		if err != nil {
			return nil, fmt.Errorf("parse %s: expand vms[%d].vmx: %w", path, i, err)
		}
		cfg.VMs[i].VMX = expandedVMX
	}

	return &cfg, nil
```

Then edit `internal/config/config_test.go`'s `TestLoad_ParsesFullConfig`:
change the fixture's two `vmx:` lines from `~/Virtual Machines/...` to
absolute paths that `expandTilde` passes through unchanged (keeping this
test focused on parsing, not home-dir resolution — expansion gets its own
test next):

```yaml
    vmx: /Users/testuser/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
```
```yaml
    vmx: /Users/testuser/Virtual Machines/win-testbed.vmwarevm/win-testbed.vmx
```

and update the two assertions that reference the old literal value:

```go
	if cfg.VMs[0].Name != "dev-ubuntu" || cfg.VMs[0].VMX != "/Users/testuser/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx" || cfg.VMs[0].Schedule != "0 2 * * *" || cfg.VMs[0].CommentTemplate != "nightly auto-backup" {
```

Add a new test in `internal/config/config_test.go` covering the
integration (Load actually calls expandTilde on both fields):

```go
func TestLoad_ExpandsTildeInDestinationAndVMX(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := writeTempConfig(t, `
destination: ~/backups
compression: zstd
retention:
  keep_last: 1
  keep_daily: 1
  keep_weekly: 1
vms:
  - name: dev
    vmx: ~/Virtual Machines/dev.vmwarevm/dev.vmx
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	wantDest := filepath.Join(home, "backups")
	if cfg.Destination != wantDest {
		t.Errorf("Destination = %q, want %q", cfg.Destination, wantDest)
	}
	wantVMX := filepath.Join(home, "Virtual Machines/dev.vmwarevm/dev.vmx")
	if cfg.VMs[0].VMX != wantVMX {
		t.Errorf("VMs[0].VMX = %q, want %q", cfg.VMs[0].VMX, wantVMX)
	}
}
```

- [ ] **Step 6: Run the full config package test suite**

Run: `go test ./internal/config/... -v`
Expected: PASS — including the updated `TestLoad_ParsesFullConfig` and the new `TestLoad_ExpandsTildeInDestinationAndVMX`.

- [ ] **Step 7: Commit**

```bash
git add internal/config/expand.go internal/config/expand_internal_test.go internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): expand ~ in destination and vmx paths on load"
```

---

### Task 2: Config field validation (#16)

**Files:**
- Create: `internal/config/validate.go`
- Create: `internal/config/validate_test.go` (package `config_test` — `Validate` is exported)
- Modify: `internal/config/config.go` (`Load`, to call `Validate` before returning)

**Interfaces:**
- Consumes: nothing new (operates on the already-defined `Config`/`Retention`/`VM` types).
- Produces: `Validate(cfg *Config) error` in package `config`, exported. Consumed by `Load` (this task) and later by `snapback init` (Task 5) before it writes a freshly-built config.

- [ ] **Step 1: Write the failing tests**

```go
// internal/config/validate_test.go
package config_test

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func validConfig() *config.Config {
	return &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "zstd",
		Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
		VMs: []config.VM{
			{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
		},
	}
}

func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := config.Validate(validConfig()); err != nil {
		t.Errorf("Validate(valid config) = %v, want nil", err)
	}
}

func TestValidate_RejectsEmptyDestination(t *testing.T) {
	cfg := validConfig()
	cfg.Destination = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Errorf("Validate() = %v, want an error mentioning \"destination\"", err)
	}
}

func TestValidate_RejectsUnknownCompression(t *testing.T) {
	cfg := validConfig()
	cfg.Compression = "bogus"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "compression") || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Validate() = %v, want an error naming the bad compression value", err)
	}
}

func TestValidate_RejectsNegativeRetention(t *testing.T) {
	cfg := validConfig()
	cfg.Retention.KeepLast = -1
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "keep_last") {
		t.Errorf("Validate() = %v, want an error naming keep_last", err)
	}
}

func TestValidate_RejectsVMMissingName(t *testing.T) {
	cfg := validConfig()
	cfg.VMs[0].Name = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("Validate() = %v, want an error about the missing VM name", err)
	}
}

func TestValidate_RejectsVMMissingVMX(t *testing.T) {
	cfg := validConfig()
	cfg.VMs[0].VMX = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "vmx") {
		t.Errorf("Validate() = %v, want an error about the missing vmx path", err)
	}
}

func TestValidate_RejectsDuplicateVMNames(t *testing.T) {
	cfg := validConfig()
	cfg.VMs = append(cfg.VMs, config.VM{Name: "dev", VMX: "/vms/dev2.vmx"})
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Validate() = %v, want an error about the duplicate VM name", err)
	}
}

func TestValidate_ReportsMultipleProblemsAtOnce(t *testing.T) {
	cfg := validConfig()
	cfg.Destination = ""
	cfg.Compression = "bogus"
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination") || !strings.Contains(err.Error(), "compression") {
		t.Errorf("Validate() = %v, want it to report both problems, not just the first", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run TestValidate -v`
Expected: FAIL — `undefined: config.Validate`

- [ ] **Step 3: Implement `Validate`**

```go
// internal/config/validate.go
package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks that cfg's fields are internally consistent -- valid
// enum values, no negative retention counts, every VM has the fields
// run/list/status need to identify and back it up. Load calls this on
// every parsed config so a malformed config.yaml fails fast at load
// time with every problem listed at once, instead of surfacing as a
// confusing failure deep in the backup choreography.
func Validate(cfg *Config) error {
	var errs []error

	if strings.TrimSpace(cfg.Destination) == "" {
		errs = append(errs, errors.New("destination must not be empty"))
	}

	switch cfg.Compression {
	case "zstd", "gzip":
	default:
		errs = append(errs, fmt.Errorf("compression must be \"zstd\" or \"gzip\", got %q", cfg.Compression))
	}

	if cfg.Retention.KeepLast < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_last must not be negative, got %d", cfg.Retention.KeepLast))
	}
	if cfg.Retention.KeepDaily < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_daily must not be negative, got %d", cfg.Retention.KeepDaily))
	}
	if cfg.Retention.KeepWeekly < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_weekly must not be negative, got %d", cfg.Retention.KeepWeekly))
	}

	seen := make(map[string]bool, len(cfg.VMs))
	for i, vm := range cfg.VMs {
		if strings.TrimSpace(vm.Name) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: name must not be empty", i))
		} else if seen[vm.Name] {
			errs = append(errs, fmt.Errorf("vms[%d]: duplicate VM name %q", i, vm.Name))
		} else {
			seen[vm.Name] = true
		}
		if strings.TrimSpace(vm.VMX) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: vmx must not be empty", i))
		}
	}

	return errors.Join(errs...)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/... -run TestValidate -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Wire `Validate` into `Load`**

Edit `internal/config/config.go`, immediately before `Load`'s final `return &cfg, nil`:

```go
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
```

- [ ] **Step 6: Run the full config package suite**

Run: `go test ./internal/config/... -v`
Expected: PASS — confirms the existing `TestLoad_ParsesFullConfig` fixture (already valid) still loads cleanly now that `Load` validates.

- [ ] **Step 7: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go internal/config/config.go
git commit -m "feat(config): validate compression, retention, and VM fields on load"
```

---

### Task 3: `config.Marshal` — YAML write support

**Files:**
- Create: `internal/config/marshal.go`
- Create: `internal/config/marshal_test.go` (package `config_test`)
- Modify: `internal/config/config.go` (add `yaml:"..."` struct tags; rename the existing koanf-parser import to avoid colliding with the direct `go.yaml.in/yaml/v3` import `Marshal` needs)
- Modify: `go.mod` / `go.sum` (via `go mod tidy` — promotes `go.yaml.in/yaml/v3` from indirect to direct)

**Interfaces:**
- Consumes: `Config`/`Retention`/`VM`/`Notifications` (this task adds `yaml` tags to them, changing no existing behavior — koanf's `Load` path only reads the `koanf` tags).
- Produces: `Marshal(cfg *Config) ([]byte, error)` in package `config`, exported. Consumed by `snapback init` (Task 5).

- [ ] **Step 1: Write the failing tests**

```go
// internal/config/marshal_test.go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestMarshal_ProducesExpectedYAML(t *testing.T) {
	cfg := &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "zstd",
		Retention:   config.Retention{KeepLast: 5, KeepDaily: 7, KeepWeekly: 4},
		VMs: []config.VM{
			{Name: "dev-ubuntu", VMX: "/vms/dev-ubuntu.vmwarevm/dev-ubuntu.vmx", Schedule: "0 2 * * *", CommentTemplate: "nightly auto-backup"},
			{Name: "win-testbed", VMX: "/vms/win-testbed.vmwarevm/win-testbed.vmx"},
		},
		Notifications: config.Notifications{Enabled: true},
	}

	got, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	want := `destination: /Volumes/Backups/snapback
compression: zstd
retention:
    keep_last: 5
    keep_daily: 7
    keep_weekly: 4
vms:
    - name: dev-ubuntu
      vmx: /vms/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
      schedule: 0 2 * * *
      comment_template: nightly auto-backup
    - name: win-testbed
      vmx: /vms/win-testbed.vmwarevm/win-testbed.vmx
notifications:
    enabled: true
`
	if string(got) != want {
		t.Errorf("Marshal produced:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarshal_RoundTripsThroughLoad(t *testing.T) {
	cfg := &config.Config{
		Destination: "/Volumes/Backups/snapback",
		Compression: "gzip",
		Retention:   config.Retention{KeepLast: 3, KeepDaily: 2, KeepWeekly: 1},
		VMs: []config.VM{
			{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
		},
		Notifications: config.Notifications{Enabled: false},
	}

	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("failed to write marshaled config: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(marshaled config) returned error: %v", err)
	}
	if loaded.Destination != cfg.Destination || loaded.Compression != cfg.Compression || loaded.Retention != cfg.Retention {
		t.Errorf("Load(Marshal(cfg)) = %+v, want it to match the original scalar/retention fields", loaded)
	}
	if len(loaded.VMs) != 1 || loaded.VMs[0] != cfg.VMs[0] {
		t.Errorf("Load(Marshal(cfg)).VMs = %+v, want %+v", loaded.VMs, cfg.VMs)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run TestMarshal -v`
Expected: FAIL — `undefined: config.Marshal`

- [ ] **Step 3: Add `yaml` struct tags and implement `Marshal`**

Edit `internal/config/config.go`'s type definitions to add `yaml` tags alongside the existing `koanf` ones (koanf's `Load` path is untouched by this — it only reads `koanf` tags):

```go
type Config struct {
	Destination   string        `koanf:"destination" yaml:"destination"`
	Compression   string        `koanf:"compression" yaml:"compression"`
	Retention     Retention     `koanf:"retention" yaml:"retention"`
	VMs           []VM          `koanf:"vms" yaml:"vms"`
	Notifications Notifications `koanf:"notifications" yaml:"notifications"`
}

type Retention struct {
	KeepLast   int `koanf:"keep_last" yaml:"keep_last"`
	KeepDaily  int `koanf:"keep_daily" yaml:"keep_daily"`
	KeepWeekly int `koanf:"keep_weekly" yaml:"keep_weekly"`
}

type VM struct {
	Name            string `koanf:"name" yaml:"name"`
	VMX             string `koanf:"vmx" yaml:"vmx"`
	Schedule        string `koanf:"schedule" yaml:"schedule,omitempty"`
	CommentTemplate string `koanf:"comment_template" yaml:"comment_template,omitempty"`
}

type Notifications struct {
	Enabled bool `koanf:"enabled" yaml:"enabled"`
}
```

`config.go` currently imports the koanf YAML parser unaliased as `yaml`
(`"github.com/knadh/koanf/parsers/yaml"`, used as `yaml.Parser()`).
`Marshal` needs the *other* YAML library (`go.yaml.in/yaml/v3`, which
marshals arbitrary structs by reflection — the koanf parser's `Marshal`
only accepts `map[string]any`). Alias the koanf one to avoid a collision:

```go
import (
	"errors"
	"fmt"
	"io/fs"

	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)
```

and update its one call site in `Load`: `yaml.Parser()` → `koanfyaml.Parser()`.

```go
// internal/config/marshal.go
package config

import "go.yaml.in/yaml/v3"

// Marshal renders cfg as YAML in the shape Load parses, matching the
// config.yaml documented in docs/design.md#config-reference. Used by
// `snapback init` to write a freshly-built Config to disk.
func Marshal(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
```

- [ ] **Step 4: Tidy modules**

Run: `go mod tidy`
Expected: `go.mod` gains `go.yaml.in/yaml/v3` under the direct `require`
block (it was already present as an indirect dependency of the koanf
YAML parser, so no new module is downloaded).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — all of Tasks 1-3's tests, plus the pre-existing `TestLoad_*` tests (confirming the `koanfyaml` rename didn't break parsing).

- [ ] **Step 6: Commit**

```bash
git add internal/config/marshal.go internal/config/marshal_test.go internal/config/config.go go.mod go.sum
git commit -m "feat(config): add Marshal for writing config.yaml back out"
```

---

### Task 4: VM discovery (filesystem scan)

**Files:**
- Create: `internal/cli/vmdiscovery.go`
- Create: `internal/cli/vmdiscovery_internal_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `discoveredVM{Name, VMX string}`, `discoverVMs(searchDirs []string) ([]discoveredVM, error)`, `defaultVMSearchDirs() []string` in package `cli`. Consumed by `snapback init` (Task 5).

- [ ] **Step 1: Write the failing tests**

```go
// internal/cli/vmdiscovery_internal_test.go
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func makeVMwareVM(t *testing.T, dir, name string, withMatchingVMX bool) {
	t.Helper()
	bundle := filepath.Join(dir, name+".vmwarevm")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("failed to create bundle dir: %v", err)
	}
	if withMatchingVMX {
		if err := os.WriteFile(filepath.Join(bundle, name+".vmx"), []byte(""), 0o644); err != nil {
			t.Fatalf("failed to create .vmx: %v", err)
		}
	}
}

func TestDiscoverVMs_FindsBundleWithMatchingVMX(t *testing.T) {
	dir := t.TempDir()
	makeVMwareVM(t, dir, "myvm", true)

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "myvm" || got[0].VMX != filepath.Join(dir, "myvm.vmwarevm", "myvm.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for myvm", got)
	}
}

func TestDiscoverVMs_SkipsBundleWithoutMatchingVMX(t *testing.T) {
	dir := t.TempDir()
	makeVMwareVM(t, dir, "myvm", false)

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries (bundle has no matching .vmx)", got)
	}
}

func TestDiscoverVMs_SkipsNonexistentDir(t *testing.T) {
	got, err := discoverVMs([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v, want nil (missing dir is not an error)", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries", got)
	}
}

func TestDiscoverVMs_SortsByNameAcrossMultipleDirs(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	makeVMwareVM(t, dirA, "zebra", true)
	makeVMwareVM(t, dirB, "alpha", true)

	got, err := discoverVMs([]string{dirA, dirB})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zebra" {
		t.Errorf("discoverVMs = %+v, want [alpha, zebra] sorted", got)
	}
}

func TestDefaultVMSearchDirs_IncludesVirtualMachinesUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dirs := defaultVMSearchDirs()
	want := filepath.Join(home, "Virtual Machines")
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("defaultVMSearchDirs() = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run 'TestDiscoverVMs|TestDefaultVMSearchDirs' -v`
Expected: FAIL — `undefined: discoverVMs` / `undefined: defaultVMSearchDirs`

- [ ] **Step 3: Implement discovery**

```go
// internal/cli/vmdiscovery.go
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoveredVM is one candidate VM found by discoverVMs, offered to the
// user during `snapback init` for inclusion in config.yaml.
type discoveredVM struct {
	Name string
	VMX  string
}

// discoverVMs scans each directory in searchDirs (one level down, not
// recursively -- matching how Fusion lays out ~/Virtual Machines) for
// *.vmwarevm bundles, returning one discoveredVM per bundle that
// contains a .vmx file matching the bundle's own name -- the layout
// every Fusion-created VM follows. A directory that doesn't exist is
// skipped, not an error: ~/Virtual Machines may not exist if Fusion was
// never run or VMs live elsewhere, and the caller can still fall back to
// manual entry. Results are sorted by Name for deterministic output.
func discoverVMs(searchDirs []string) ([]discoveredVM, error) {
	var found []discoveredVM
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan %s: %w", dir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".vmwarevm") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".vmwarevm")
			vmx := filepath.Join(dir, entry.Name(), name+".vmx")
			if _, err := os.Stat(vmx); err != nil {
				continue
			}
			found = append(found, discoveredVM{Name: name, VMX: vmx})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, nil
}

// defaultVMSearchDirs returns the directories discoverVMs scans by
// default: just ~/Virtual Machines, Fusion's standard location (see
// docs/design.md's config reference example). Returns nil (not an
// error) if the home directory can't be determined -- init falls back to
// manual VM entry in that case, same as when the directory doesn't
// exist.
func defaultVMSearchDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, "Virtual Machines")}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -run 'TestDiscoverVMs|TestDefaultVMSearchDirs' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli/vmdiscovery.go internal/cli/vmdiscovery_internal_test.go
git commit -m "feat(cli): scan ~/Virtual Machines for VM candidates"
```

---

### Task 5: `snapback init` command

**Files:**
- Create: `internal/cli/init.go`
- Create: `internal/cli/init_internal_test.go`
- Modify: `internal/cli/root.go:48-56` (remove the stub `newInitCmd`, replaced by this task's real one)

**Interfaces:**
- Consumes:
  - `discoverVMs(searchDirs []string) ([]discoveredVM, error)`, `defaultVMSearchDirs() []string`, `discoveredVM{Name, VMX string}` (Task 4)
  - `config.Validate(cfg *config.Config) error` (Task 2)
  - `config.Marshal(cfg *config.Config) ([]byte, error)` (Task 3)
  - `swapSubcommand`, `errBoom` (`internal/cli/testcmd_internal_test.go`, pre-existing)
- Produces: `newInitCmd() *cobra.Command`, `newInitCmdWithDeps(deps initDeps) *cobra.Command`, `initDeps` struct. Consumed by `root.go` (Task 6).

- [ ] **Step 1: Write the failing tests**

```go
// internal/cli/init_internal_test.go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

func newTestRootForInit(t *testing.T, deps initDeps) *cobra.Command {
	t.Helper()
	return swapSubcommand(t, "init", newInitCmdWithDeps(deps))
}

// fakeInitDeps returns an initDeps whose writeFile captures its argument
// into written, and whose fileExists/discoverVMs are controlled by the
// caller -- covers the common case where a test only cares about what
// init would have written, not real disk I/O.
func fakeInitDeps(candidates []discoveredVM, exists bool, written *[]byte, writtenPath *string) initDeps {
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
	}
}

func TestInitCmd_WritesConfigFromDiscoveredVMAndDefaults(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"}}, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// One blank line per prompt, in order: VM selection, destination,
	// compression, keep_last, keep_daily, keep_weekly, notifications --
	// every prompt accepts its default.
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writtenPath != "/cfg/config.yaml" {
		t.Errorf("writeFile path = %q, want %q", writtenPath, "/cfg/config.yaml")
	}
	got := string(written)
	for _, want := range []string{"name: dev", "vmx: /vms/dev.vmwarevm/dev.vmx", "destination: /Volumes/Backups/snapback", "compression: zstd"} {
		if !strings.Contains(got, want) {
			t.Errorf("written config = %q, want it to contain %q", got, want)
		}
	}
	if !strings.Contains(out.String(), "wrote config to /cfg/config.yaml") {
		t.Errorf("stdout = %q, want a confirmation naming the config path", out.String())
	}
}

func TestInitCmd_NoDiscoveredVMs_PromptsManualEntry(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// name "devbox", vmx "/vms/devbox.vmx", blank name to stop manual
	// entry, then defaults for destination/compression/retention/notify.
	root.SetIn(strings.NewReader("devbox\n/vms/devbox.vmx\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := string(written)
	if !strings.Contains(got, "name: devbox") || !strings.Contains(got, "vmx: /vms/devbox.vmx") {
		t.Errorf("written config = %q, want the manually-entered devbox VM", got)
	}
}

func TestInitCmd_ExistingConfig_WithoutForce_Errors(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, true, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Execute() error = %v, want a message about the existing config and --force", err)
	}
	if written != nil {
		t.Errorf("writeFile was called, want init to refuse before writing")
	}
}

func TestInitCmd_ExistingConfig_WithForce_Overwrites(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{{Name: "dev", VMX: "/vms/dev.vmx"}}, true, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml", "--force"})
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want --force to allow overwriting", err)
	}
	if written == nil {
		t.Errorf("writeFile was not called, want --force to allow the write")
	}
}

func TestInitCmd_InvalidCompressionChoice_Errors(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps(nil, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// blank name to skip manual VM entry, blank destination, then an
	// invalid compression choice.
	root.SetIn(strings.NewReader("\nbogus\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("Execute() error = %v, want an error naming the invalid compression choice", err)
	}
}

func TestInitCmd_DiscoverVMsError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, errBoom },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { t.Fatal("writeFile should not be called"); return nil },
		fileExists:  func(string) bool { return false },
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "discover VMs") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"discover VMs\" context", err, errBoom)
	}
}

func TestInitCmd_WriteFileError_IsWrapped(t *testing.T) {
	deps := initDeps{
		searchDirs:  func() []string { return nil },
		discoverVMs: func([]string) ([]discoveredVM, error) { return nil, nil },
		marshal:     config.Marshal,
		writeFile:   func(string, []byte) error { return errBoom },
		fileExists:  func(string) bool { return false },
	}

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	root.SetIn(strings.NewReader("\n\n\n\n\n\n\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "write config") || !strings.Contains(err.Error(), errBoom.Error()) {
		t.Fatalf("Execute() error = %v, want it to wrap %q with \"write config\" context", err, errBoom)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/... -run TestInitCmd -v`
Expected: FAIL — `undefined: initDeps` / `undefined: newInitCmdWithDeps`

- [ ] **Step 3: Implement prompt helpers and the command**

```go
// internal/cli/init.go
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

// initDeps groups init's external dependencies so tests can substitute a
// fake VM scanner, a config writer that captures its argument, and a
// fake existing-file check instead of touching the real filesystem or
// ~/Virtual Machines.
type initDeps struct {
	searchDirs  func() []string
	discoverVMs func(searchDirs []string) ([]discoveredVM, error)
	marshal     func(cfg *config.Config) ([]byte, error)
	writeFile   func(path string, data []byte) error
	fileExists  func(path string) bool
}

func newInitCmd() *cobra.Command {
	return newInitCmdWithDeps(initDeps{
		searchDirs:  defaultVMSearchDirs,
		discoverVMs: discoverVMs,
		marshal:     config.Marshal,
		writeFile:   func(path string, data []byte) error { return os.WriteFile(path, data, 0o644) },
		fileExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	})
}

func newInitCmdWithDeps(deps initDeps) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive config bootstrap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runInit(cmd, deps, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

func runInit(cmd *cobra.Command, deps initDeps, force bool) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}
	if !force && deps.fileExists(configPath) {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", configPath)
	}

	out := cmd.OutOrStdout()
	in := bufio.NewScanner(cmd.InOrStdin())

	candidates, err := deps.discoverVMs(deps.searchDirs())
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}

	vms, err := promptVMs(out, in, candidates)
	if err != nil {
		return err
	}

	destination, err := promptString(out, in, "Backup destination", "/Volumes/Backups/snapback")
	if err != nil {
		return err
	}
	compression, err := promptChoice(out, in, "Compression (zstd/gzip)", "zstd", []string{"zstd", "gzip"})
	if err != nil {
		return err
	}
	keepLast, err := promptInt(out, in, "Keep last N backups", 5)
	if err != nil {
		return err
	}
	keepDaily, err := promptInt(out, in, "Keep daily backups for N days", 7)
	if err != nil {
		return err
	}
	keepWeekly, err := promptInt(out, in, "Keep weekly backups for N weeks", 4)
	if err != nil {
		return err
	}
	notify, err := promptBool(out, in, "Enable notifications", true)
	if err != nil {
		return err
	}

	cfg := &config.Config{
		Destination: destination,
		Compression: compression,
		Retention: config.Retention{
			KeepLast:   keepLast,
			KeepDaily:  keepDaily,
			KeepWeekly: keepWeekly,
		},
		VMs:           vms,
		Notifications: config.Notifications{Enabled: notify},
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("built an invalid config: %w", err)
	}

	data, err := deps.marshal(cfg)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := deps.writeFile(configPath, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
}

// promptVMs lists candidates (found by discoverVMs) and asks the user
// which to include, or -- if none were found -- falls back to prompting
// for VMs one at a time by name and .vmx path.
func promptVMs(out io.Writer, in *bufio.Scanner, candidates []discoveredVM) ([]config.VM, error) {
	if len(candidates) == 0 {
		if _, err := fmt.Fprintln(out, "no VMs found automatically; enter them manually (blank name to stop)"); err != nil {
			return nil, err
		}
		return promptManualVMs(out, in)
	}

	if _, err := fmt.Fprintln(out, "discovered VMs:"); err != nil {
		return nil, err
	}
	for i, c := range candidates {
		if _, err := fmt.Fprintf(out, "  %d) %s (%s)\n", i+1, c.Name, c.VMX); err != nil {
			return nil, err
		}
	}

	selection, err := promptString(out, in, `Include which VMs? (comma-separated numbers, or "all")`, "all")
	if err != nil {
		return nil, err
	}

	var selected []discoveredVM
	if strings.EqualFold(selection, "all") {
		selected = candidates
	} else {
		for _, tok := range strings.Split(selection, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 1 || idx > len(candidates) {
				return nil, fmt.Errorf("%q is not a valid VM number (1-%d)", tok, len(candidates))
			}
			selected = append(selected, candidates[idx-1])
		}
	}

	vms := make([]config.VM, len(selected))
	for i, c := range selected {
		vms[i] = config.VM{Name: c.Name, VMX: c.VMX}
	}
	return vms, nil
}

func promptManualVMs(out io.Writer, in *bufio.Scanner) ([]config.VM, error) {
	var vms []config.VM
	for {
		name, err := promptString(out, in, "VM name (blank to stop)", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return vms, nil
		}
		vmx, err := promptString(out, in, "  .vmx path for "+name, "")
		if err != nil {
			return nil, err
		}
		if vmx == "" {
			return nil, fmt.Errorf("VM %q needs a .vmx path", name)
		}
		vms = append(vms, config.VM{Name: name, VMX: vmx})
	}
}

// promptString prints label with defaultVal shown, reads one line from
// in, and returns it trimmed, or defaultVal if the line is blank.
// Returns an error if in runs out of input or fails to read -- a
// truncated interactive session means the resulting config was never
// actually reviewed by the user, so init treats that as a hard failure
// rather than silently falling back to defaults.
func promptString(out io.Writer, in *bufio.Scanner, label, defaultVal string) (string, error) {
	if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultVal); err != nil {
		return "", err
	}
	if !in.Scan() {
		if err := in.Err(); err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return "", fmt.Errorf("read input: unexpected end of input")
	}
	line := strings.TrimSpace(in.Text())
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

func promptChoice(out io.Writer, in *bufio.Scanner, label, defaultVal string, choices []string) (string, error) {
	val, err := promptString(out, in, label, defaultVal)
	if err != nil {
		return "", err
	}
	for _, c := range choices {
		if val == c {
			return val, nil
		}
	}
	return "", fmt.Errorf("%q is not one of %v", val, choices)
}

func promptInt(out io.Writer, in *bufio.Scanner, label string, defaultVal int) (int, error) {
	val, err := promptString(out, in, label, strconv.Itoa(defaultVal))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", val)
	}
	return n, nil
}

func promptBool(out io.Writer, in *bufio.Scanner, label string, defaultVal bool) (bool, error) {
	defaultStr := "y"
	if !defaultVal {
		defaultStr = "n"
	}
	val, err := promptString(out, in, label+" (y/n)", defaultStr)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(val) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not y/n", val)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/... -run TestInitCmd -v`
Expected: PASS (all 7 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/cli/init.go internal/cli/init_internal_test.go
git commit -m "feat(cli): implement snapback init"
```

---

### Task 6: Wire `init` into `root.go`, update the design doc, final verification

**Files:**
- Modify: `internal/cli/root.go:48-56` (delete the stub `newInitCmd` — the real one now lives in `internal/cli/init.go` from Task 5)
- Modify: `docs/design.md:94` (the command-reference line for `init` currently says `discovers VMs via vmrun list`, which was never accurate for non-running VMs and doesn't match what this plan built)

**Interfaces:**
- Consumes: `newInitCmd` (Task 5, package-level, already visible to `root.go` with no import changes needed).
- Produces: nothing new — this task only removes the now-duplicate stub and finishes documentation.

- [ ] **Step 1: Delete the stub `newInitCmd` from `root.go`**

Remove these lines (48-56) from `internal/cli/root.go` — `internal/cli/init.go` (Task 5) now defines the real `newInitCmd`:

```go
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactive config bootstrap",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented(cmd)
		},
	}
}
```

(`errNotImplemented` stays — `newStatusCmd` still uses it.)

- [ ] **Step 2: Run the full package build and test suite**

Run: `go build ./... && go test ./... -v`
Expected: PASS — no duplicate `newInitCmd` symbol, no regressions in `internal/cli` or `internal/config`. `root_test.go`'s command-name list (`init, run, list, status`) is unaffected since the command name doesn't change.

- [ ] **Step 3: Update the command-reference line in `docs/design.md`**

Change line 94 from:

```
| `snapback init`                 | Interactive config bootstrap — discovers VMs via `vmrun list`, prompts for destination/retention |
```

to:

```
| `snapback init`                 | Interactive config bootstrap — discovers VMs by scanning `~/Virtual Machines` for `.vmwarevm` bundles, prompts for destination/retention (falls back to manual entry if none are found) |
```

- [ ] **Step 4: Run lint and the full test suite**

Run: `make lint && make test`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/root.go docs/design.md
git commit -m "feat(cli): wire snapback init into the root command"
```

- [ ] **Step 6: Build**

Run: `make build`
Expected: PASS, produces `dist/<goos-goarch>/snapback` with `init` fully working end-to-end (manually try `dist/*/snapback init --config /tmp/smoke-config.yaml` against a scratch `~/Virtual Machines`-like temp dir if you want a real-terminal smoke test, though it's not required for the automated suite).

---

## Self-Review Notes

- **Spec coverage:** #15 (Task 1), #16 (Task 2), #6's `init` command (Tasks 3-6, since `Marshal` is `init`-only) are each covered by a task. The `docs/design.md` command-reference table and config-reference YAML shape are both targeted directly (Task 3's `Marshal` test asserts the exact YAML shape; Task 6 corrects the stale `vmrun list` wording).
- **Placeholder scan:** every step has runnable code; no "add validation"-style stand-ins.
- **Type consistency:** `discoveredVM{Name, VMX string}` (Task 4) is used identically in Task 5's `initDeps.discoverVMs` signature and `promptVMs`. `config.Validate(cfg *Config) error` (Task 2) and `config.Marshal(cfg *Config) ([]byte, error)` (Task 3) are called with matching signatures in Task 5's `runInit`. `expandTilde` (Task 1) is unexported and used only inside `Load`, consistent with its "Produces" note.
