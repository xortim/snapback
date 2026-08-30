# PR #34 Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every finding from the `/code-review 34` report on `feat/init-command` (PR #34, the `init` command) that warrants a code change.

**Architecture:** Each finding is fixed in place with a matching unit test, in small commits on top of the existing `feat/init-command` branch (PR #34 is already open against it). No new packages are introduced. One finding is closed with no code change (see Findings Disposition).

**Tech Stack:** Go 1.26.5 (`go.mod`), standard `testing` package, `github.com/spf13/cobra`, `github.com/knadh/koanf`.

**Spec:** `/code-review 34` findings (reproduced below).

```
1. internal/config/config.go:74 -- config.Load now unconditionally calls
   Validate, so a config.yaml that previously loaded fine (e.g. negative
   retention, blank destination, duplicate VM name) now hard-fails run/list.
2. internal/cli/init.go:162 -- config.Validate(cfg) only runs at the very
   end of runInit, after every prompt has been answered, so an early
   mistake (e.g. selecting the same VM twice) isn't caught until the whole
   interactive session is done.
3. internal/config/validate.go:38 -- duplicate-VM-name detection keys the
   `seen` map on the raw (untrimmed) name while the adjacent empty-name
   check uses TrimSpace, so whitespace-padded duplicates ("dev" vs. " dev")
   aren't caught.
4. internal/cli/vmdiscovery.go:38 -- entry.IsDir() doesn't follow symlinks,
   so a symlinked .vmwarevm bundle is silently skipped by auto-discovery.
5. internal/cli/vmdiscovery.go:38 -- the ".vmwarevm" suffix match via
   strings.HasSuffix is case-sensitive, so a differently-cased extension
   (e.g. "MyVM.VMwareVM") is silently skipped.
6. internal/cli/vmdiscovery.go:43 -- os.Stat confirms the .vmx path exists
   but never checks it's a regular file, not a directory.
7. README.md:17 -- claims `init`, `run`, and `list` "are wired up to that
   [backup] choreography," but `init` never touches internal/backup -- it
   only builds/validates/marshals config.yaml. Contradicts this same PR's
   own CLAUDE.md edit, which correctly describes `init` separately.
8. internal/cli/init.go:181 -- promptVMs, promptManualVMs, promptString,
   promptChoice, promptInt, and promptBool all thread the identical
   (out io.Writer, in *bufio.Scanner) pair through 6 signatures and 8+ call
   sites in runInit.
```

**Findings Disposition:**

| # | Finding | Disposition |
|---|---------|--------------|
| 1 | `config.Load` unconditional `Validate` | **No code change.** Confirmed with the user: `Validate` was added specifically to fail fast on bad configs (its own doc comment says so), and this is unreleased phase-1 software with no installed base to break. Closed as by-design. |
| 3 | Duplicate-name whitespace bug | Task 1 |
| 2 | Late validation in `runInit` | Task 2 (depends on Task 1's exported `ValidateVMs`) |
| 4, 5, 6 | `discoverVMs` symlink / case / regular-file bugs | Task 3 |
| 7 | README doc inconsistency | Task 4 |
| 8 | Repeated `(out, in)` parameter pair | Task 5 |

## Global Constraints

- Go 1.26.5 toolchain; build/test with the `go` on `PATH`.
- Verification gate for each task: `go build ./...`, `go vet ./...`, `gofmt -l .`, and the relevant `go test` run. Run `make lint` too if it succeeds in this environment; skip it (noting why) if it doesn't.
- Test files in `internal/cli` are `package cli` (white-box, `*_internal_test.go`); test files in `internal/config` are `package config_test` (black-box) except where noted. Match the existing file's package for any edit.
- Preserve all currently-passing behavior -- no task in this plan should regress an existing test.
- Commit messages follow this repo's convention: real Conventional Commit type (`fix`/`docs`/`refactor`/`test`), package name as scope.

---

## File Structure

- Modify `internal/config/validate.go` -- extract per-VM checks into an exported `ValidateVMs`, fixing the whitespace-dedup bug as part of the extraction.
- Modify `internal/config/validate_test.go` -- add whitespace-duplicate coverage and direct `ValidateVMs` tests.
- Modify `internal/cli/init.go` -- call `config.ValidateVMs` right after VM selection; extract the `(out, in)` pair into a `prompter` type.
- Modify `internal/cli/init_internal_test.go` -- add a test proving duplicate VM selection fails before the remaining prompts run.
- Modify `internal/cli/vmdiscovery.go` -- follow symlinked bundles, match `.vmwarevm` case-insensitively, require the `.vmx` path to be a regular file.
- Modify `internal/cli/vmdiscovery_internal_test.go` -- add coverage for all three fixes.
- Modify `README.md` -- correct the Status section's claim about what `init` is wired to.

## Task 1: `config.ValidateVMs` -- extract per-VM validation, fix whitespace-dedup bug

**Files:**
- Modify: `internal/config/validate.go`
- Test: `internal/config/validate_test.go`

**Interfaces:**
- Produces: `ValidateVMs(vms []VM) error` in package `config` -- exported so `internal/cli/init.go` (Task 2) can validate an in-progress VM list before the rest of `runInit`'s prompts run.
- `Validate(cfg *Config) error`'s signature and error-message wording are unchanged; it now delegates its per-VM checks to `ValidateVMs`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/validate_test.go`, after `TestValidate_RejectsDuplicateVMNames`:

```go
func TestValidate_RejectsDuplicateVMNames_IgnoringSurroundingWhitespace(t *testing.T) {
	cfg := validConfig()
	cfg.VMs = append(cfg.VMs, config.VM{Name: " dev", VMX: "/vms/dev2.vmx"})
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Validate() = %v, want an error about the duplicate VM name (whitespace-padded)", err)
	}
}

func TestValidateVMs_RejectsDuplicateNames(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "dev", VMX: "/vms/dev2.vmx"},
	}
	err := config.ValidateVMs(vms)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("ValidateVMs() = %v, want an error about the duplicate VM name", err)
	}
}

func TestValidateVMs_AcceptsDistinctNames(t *testing.T) {
	vms := []config.VM{
		{Name: "dev", VMX: "/vms/dev.vmx"},
		{Name: "prod", VMX: "/vms/prod.vmx"},
	}
	if err := config.ValidateVMs(vms); err != nil {
		t.Errorf("ValidateVMs() = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run 'TestValidate_RejectsDuplicateVMNames_IgnoringSurroundingWhitespace|TestValidateVMs' -v`
Expected: `TestValidate_RejectsDuplicateVMNames_IgnoringSurroundingWhitespace` FAILs (current `seen` map keys on the untrimmed name, so "dev" and " dev" don't collide); `TestValidateVMs_*` FAIL with `undefined: config.ValidateVMs`.

- [ ] **Step 3: Extract `ValidateVMs` and fix the whitespace bug**

In `internal/config/validate.go`, replace:

```go
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

with:

```go
	errs = append(errs, validateVMs(cfg.VMs)...)

	return errors.Join(errs...)
}

// ValidateVMs checks vms for the same per-VM problems Validate checks as
// part of a full config: a non-empty name, a non-empty vmx path, and no
// two VMs sharing a name once whitespace is trimmed. Exported separately
// from Validate so a caller that builds a VM list incrementally --
// `init`'s VM-selection prompt, in particular -- can fail fast on a bad
// selection before collecting the rest of the config.
func ValidateVMs(vms []VM) error {
	return errors.Join(validateVMs(vms)...)
}

func validateVMs(vms []VM) []error {
	var errs []error
	seen := make(map[string]bool, len(vms))
	for i, vm := range vms {
		name := strings.TrimSpace(vm.Name)
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("vms[%d]: name must not be empty", i))
		case seen[name]:
			errs = append(errs, fmt.Errorf("vms[%d]: duplicate VM name %q", i, vm.Name))
		default:
			seen[name] = true
		}
		if strings.TrimSpace(vm.VMX) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: vmx must not be empty", i))
		}
	}
	return errs
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -run 'TestValidate_RejectsDuplicateVMNames_IgnoringSurroundingWhitespace|TestValidateVMs' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/config/...`
Expected: PASS -- including every existing `TestValidate_*` test, since `Validate`'s external error wording and behavior are unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "fix(config): trim whitespace before comparing VM names for duplicates"
```

## Task 2: `runInit` -- validate the VM selection before the remaining prompts

**Files:**
- Modify: `internal/cli/init.go` (`runInit`)
- Test: `internal/cli/init_internal_test.go`

**Interfaces:**
- Consumes: `config.ValidateVMs(vms []config.VM) error` (Task 1).
- Produces: no signature change to `runInit`. New behavior: a duplicate or blank VM name is now reported right after VM selection, wrapped as `"invalid VM selection: %w"`, instead of only at the end via the existing `config.Validate(cfg)` call.

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/init_internal_test.go`, after `TestInitCmd_NoDiscoveredVMs_PromptsManualEntry`:

```go
func TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts(t *testing.T) {
	var written []byte
	var writtenPath string
	deps := fakeInitDeps([]discoveredVM{
		{Name: "dev", VMX: "/vms/dev.vmwarevm/dev.vmx"},
	}, false, &written, &writtenPath)

	root := newTestRootForInit(t, deps)
	root.SetArgs([]string{"init", "--config", "/cfg/config.yaml"})
	// Select the one candidate twice. If init deferred validation to the
	// end, it would next try to read the destination prompt, find no more
	// input, and fail with an "unexpected end of input" error instead --
	// this test only passes if the duplicate is caught immediately after
	// selection.
	root.SetIn(strings.NewReader("1,1\n"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Execute() error = %v, want an error about the duplicate VM selection", err)
	}
	if written != nil {
		t.Errorf("writeFile was called, want init to fail before writing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/... -run TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts -v`
Expected: FAIL -- `Execute() error = read input: unexpected end of input`, not an error mentioning "duplicate".

- [ ] **Step 3: Validate the VM list right after selection**

In `internal/cli/init.go`, change:

```go
	vms, err := promptVMs(out, in, candidates)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}

	destination, err := promptString(out, in, "Backup destination", "/Volumes/Backups/snapback")
```

to:

```go
	vms, err := promptVMs(out, in, candidates)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}
	if err := config.ValidateVMs(vms); err != nil {
		return fmt.Errorf("invalid VM selection: %w", err)
	}

	destination, err := promptString(out, in, "Backup destination", "/Volumes/Backups/snapback")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/... -run TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS -- in particular every existing `TestInitCmd_*` test, since none of their fixtures select duplicate or blank VM names.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/init.go internal/cli/init_internal_test.go
git commit -m "fix(cli): validate init's VM selection before the remaining prompts"
```

## Task 3: `discoverVMs` -- follow symlinks, match case-insensitively, require a regular `.vmx` file

**Files:**
- Modify: `internal/cli/vmdiscovery.go`
- Test: `internal/cli/vmdiscovery_internal_test.go`

**Interfaces:**
- Produces: new unexported helper `isVMBundleDir(dir string, entry os.DirEntry) bool` in `internal/cli/vmdiscovery.go`, used only by `discoverVMs`.
- `discoverVMs(searchDirs []string) ([]discoveredVM, error)`'s signature is unchanged.

- [ ] **Step 1: Write the failing tests**

Add to `internal/cli/vmdiscovery_internal_test.go`, after `TestDiscoverVMs_SkipsBundleWithoutMatchingVMX`:

```go
func TestDiscoverVMs_FollowsSymlinkedBundle(t *testing.T) {
	realParent := t.TempDir()
	makeVMwareVM(t, realParent, "myvm", true)

	searchDir := t.TempDir()
	link := filepath.Join(searchDir, "myvm.vmwarevm")
	if err := os.Symlink(filepath.Join(realParent, "myvm.vmwarevm"), link); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	got, err := discoverVMs([]string{searchDir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "myvm" || got[0].VMX != filepath.Join(link, "myvm.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for myvm via the symlink", got)
	}
}

func TestDiscoverVMs_MatchesCaseInsensitiveSuffix(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "MyVM.VMwareVM")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("failed to create bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "MyVM.vmx"), []byte(""), 0o644); err != nil {
		t.Fatalf("failed to create .vmx: %v", err)
	}

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "MyVM" || got[0].VMX != filepath.Join(bundle, "MyVM.vmx") {
		t.Errorf("discoverVMs = %+v, want one entry for MyVM despite the differently-cased extension", got)
	}
}

func TestDiscoverVMs_SkipsVMXThatIsADirectory(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "myvm.vmwarevm")
	if err := os.MkdirAll(filepath.Join(bundle, "myvm.vmx"), 0o755); err != nil {
		t.Fatalf("failed to create myvm.vmx as a directory: %v", err)
	}

	got, err := discoverVMs([]string{dir})
	if err != nil {
		t.Fatalf("discoverVMs returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("discoverVMs = %+v, want no entries (myvm.vmx is a directory, not a file)", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/... -run 'TestDiscoverVMs_FollowsSymlinkedBundle|TestDiscoverVMs_MatchesCaseInsensitiveSuffix|TestDiscoverVMs_SkipsVMXThatIsADirectory' -v`
Expected: all three FAIL -- the symlink and case-insensitive cases currently return zero entries; the directory-as-`.vmx` case currently returns one (bogus) entry.

- [ ] **Step 3: Fix `discoverVMs`**

In `internal/cli/vmdiscovery.go`, replace:

```go
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
```

with:

```go
		for _, entry := range entries {
			if !strings.HasSuffix(strings.ToLower(entry.Name()), ".vmwarevm") || !isVMBundleDir(dir, entry) {
				continue
			}
			name := entry.Name()[:len(entry.Name())-len(".vmwarevm")]
			vmx := filepath.Join(dir, entry.Name(), name+".vmx")
			info, err := os.Stat(vmx)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			found = append(found, discoveredVM{Name: name, VMX: vmx})
		}
```

Add this function after `discoverVMs`:

```go
// isVMBundleDir reports whether entry (a child of dir) is a directory,
// following a symlink if entry itself is one -- os.DirEntry.IsDir()
// reflects only the dirent's own type and returns false for a symlink
// even when it resolves to a directory, which would otherwise hide a
// .vmwarevm bundle stored on other media and symlinked into the search
// directory to keep it visible in Fusion's library.
func isVMBundleDir(dir string, entry os.DirEntry) bool {
	if entry.Type()&os.ModeSymlink == 0 {
		return entry.IsDir()
	}
	info, err := os.Stat(filepath.Join(dir, entry.Name()))
	return err == nil && info.IsDir()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/... -run 'TestDiscoverVMs_FollowsSymlinkedBundle|TestDiscoverVMs_MatchesCaseInsensitiveSuffix|TestDiscoverVMs_SkipsVMXThatIsADirectory' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go test ./internal/cli/...`
Expected: PASS -- including `TestDiscoverVMs_FindsBundleWithMatchingVMX`, `TestDiscoverVMs_SkipsBundleWithoutMatchingVMX`, `TestDiscoverVMs_SkipsNonexistentDir`, `TestDiscoverVMs_SortsByNameAcrossMultipleDirs`.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/vmdiscovery.go internal/cli/vmdiscovery_internal_test.go
git commit -m "fix(cli): follow symlinked VM bundles and validate .vmx more strictly in discoverVMs"
```

## Task 4: README -- correct what `init` is wired to

**Files:**
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Fix the Status section**

In `README.md`, change:

```markdown
Phase 1 (core CLI) is in progress. The backup choreography (snapshot →
sync → copy → merge → archive → checksum), config loading, and the
`VMController` interface — with both a fake for unit tests and a real
`vmcli`-backed implementation — are implemented; `init`, `run`, and
`list` are wired up to that choreography, while `status` is still
scaffolded, returning "not yet implemented", and `cleanup` doesn't exist
yet. See
```

to:

```markdown
Phase 1 (core CLI) is in progress. The backup choreography (snapshot →
sync → copy → merge → archive → checksum), config loading, and the
`VMController` interface — with both a fake for unit tests and a real
`vmcli`-backed implementation — are implemented; `run` and `list` are
wired up to that choreography, `init` is an interactive config bootstrap
(discovers VMs, prompts for destination/retention, writes config.yaml)
that doesn't touch the choreography itself, while `status` is still
scaffolded, returning "not yet implemented", and `cleanup` doesn't exist
yet. See
```

- [ ] **Step 2: Verify the fix**

```bash
grep -n "wired up to that choreography" README.md
```

Expected: the matching line lists only `run` and `list`, not `init`.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): clarify that init does not touch the backup choreography"
```

## Task 5: `init.go` -- extract a `prompter` to stop threading `(out, in)` everywhere

**Files:**
- Modify: `internal/cli/init.go`

**Interfaces:**
- Produces: `prompter` struct (`out io.Writer`, `in *bufio.Scanner`) with methods `promptVMs`, `promptManualVMs`, `promptString`, `promptChoice`, `promptInt`, `promptBool` in `internal/cli/init.go`, replacing the free functions of the same names.
- No behavior change and no test-visible signature change -- `internal/cli/init_internal_test.go` exercises `runInit` only through the cobra command, so no test edits are needed for this task.

- [ ] **Step 1: Confirm the starting point compiles and tests pass**

Run: `go build ./... && go test ./internal/cli/...`
Expected: PASS (this task is a pure refactor of Task 2's `runInit`; there is no failing-test step since no new externally-visible behavior is being added).

- [ ] **Step 2: Add the `prompter` type**

In `internal/cli/init.go`, add this type directly above `func runInit`:

```go
// prompter bundles the stdin/stdout pair every init prompt needs, so each
// prompt method below takes only the arguments specific to that prompt
// instead of repeating the (out, in) pair on every call.
type prompter struct {
	out io.Writer
	in  *bufio.Scanner
}
```

- [ ] **Step 3: Rewrite `runInit` to build and use a `prompter`**

Change:

```go
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
	if len(vms) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}
	if err := config.ValidateVMs(vms); err != nil {
		return fmt.Errorf("invalid VM selection: %w", err)
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
```

to:

```go
	p := prompter{out: cmd.OutOrStdout(), in: bufio.NewScanner(cmd.InOrStdin())}

	candidates, err := deps.discoverVMs(deps.searchDirs())
	if err != nil {
		return fmt.Errorf("discover VMs: %w", err)
	}

	vms, err := p.promptVMs(candidates)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: no VMs configured; `snapback run --all` will have nothing to back up"); err != nil {
			return err
		}
	}
	if err := config.ValidateVMs(vms); err != nil {
		return fmt.Errorf("invalid VM selection: %w", err)
	}

	destination, err := p.promptString("Backup destination", "/Volumes/Backups/snapback")
	if err != nil {
		return err
	}
	compression, err := p.promptChoice("Compression (zstd/gzip)", "zstd", []string{"zstd", "gzip"})
	if err != nil {
		return err
	}
	keepLast, err := p.promptInt("Keep last N backups", 5)
	if err != nil {
		return err
	}
	keepDaily, err := p.promptInt("Keep daily backups for N days", 7)
	if err != nil {
		return err
	}
	keepWeekly, err := p.promptInt("Keep weekly backups for N weeks", 4)
	if err != nil {
		return err
	}
	notify, err := p.promptBool("Enable notifications", true)
	if err != nil {
		return err
	}
```

Further down in the same function, change:

```go
	_, err = fmt.Fprintf(out, "wrote config to %s\n", configPath)
	return err
}
```

to:

```go
	_, err = fmt.Fprintf(p.out, "wrote config to %s\n", configPath)
	return err
}
```

- [ ] **Step 4: Convert the six prompt functions into `prompter` methods**

Replace the entire block from `func promptVMs` through the end of `func promptBool` with:

```go
// promptVMs lists candidates (found by discoverVMs) and asks the user
// which to include, or -- if none were found -- falls back to prompting
// for VMs one at a time by name and .vmx path.
func (p prompter) promptVMs(candidates []discoveredVM) ([]config.VM, error) {
	if len(candidates) == 0 {
		if _, err := fmt.Fprintln(p.out, "no VMs found automatically; enter them manually (blank name to stop)"); err != nil {
			return nil, err
		}
		return p.promptManualVMs()
	}

	if _, err := fmt.Fprintln(p.out, "discovered VMs:"); err != nil {
		return nil, err
	}
	for i, c := range candidates {
		if _, err := fmt.Fprintf(p.out, "  %d) %s (%s)\n", i+1, c.Name, c.VMX); err != nil {
			return nil, err
		}
	}

	selection, err := p.promptString(`Include which VMs? (comma-separated numbers, or "all")`, "all")
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

func (p prompter) promptManualVMs() ([]config.VM, error) {
	var vms []config.VM
	for {
		name, err := p.promptString("VM name (blank to stop)", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return vms, nil
		}
		vmx, err := p.promptString("  .vmx path for "+name, "")
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
// p.in, and returns it trimmed, or defaultVal if the line is blank.
// Returns an error if p.in runs out of input or fails to read -- a
// truncated interactive session means the resulting config was never
// actually reviewed by the user, so init treats that as a hard failure
// rather than silently falling back to defaults.
func (p prompter) promptString(label, defaultVal string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "%s [%s]: ", label, defaultVal); err != nil {
		return "", err
	}
	if !p.in.Scan() {
		if err := p.in.Err(); err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return "", fmt.Errorf("read input: unexpected end of input")
	}
	line := strings.TrimSpace(p.in.Text())
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

func (p prompter) promptChoice(label, defaultVal string, choices []string) (string, error) {
	val, err := p.promptString(label, defaultVal)
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

func (p prompter) promptInt(label string, defaultVal int) (int, error) {
	val, err := p.promptString(label, strconv.Itoa(defaultVal))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", val)
	}
	return n, nil
}

func (p prompter) promptBool(label string, defaultVal bool) (bool, error) {
	defaultStr := "y"
	if !defaultVal {
		defaultStr = "n"
	}
	val, err := p.promptString(label+" (y/n)", defaultStr)
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

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `go build ./... && go test ./internal/cli/... -v`
Expected: PASS -- every existing `TestInitCmd_*` test (including Task 2's new `TestInitCmd_DuplicateVMSelection_FailsBeforeRemainingPrompts`) still passes unchanged, since this task only changes how `runInit` calls the prompt logic, not what it does.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/init.go
git commit -m "refactor(cli): extract a prompter type instead of threading (out, in) through every init prompt"
```

---

## Final Verification

- [ ] **Run the full suite one more time end to end**

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./...
```

Expected: all green, `gofmt -l .` prints nothing. At this point every code-change finding from `/code-review 34` is fixed:
1. Duplicate-VM-name whitespace bug (Task 1).
2. `init`'s late VM-selection validation (Task 2).
3. `discoverVMs` symlink, case-sensitivity, and regular-file bugs (Task 3).
4. README's doc inconsistency about `init` (Task 4).
5. Repeated `(out, in)` parameter pair in `init.go` (Task 5).

The remaining finding (`config.Load`'s unconditional `Validate` call) is deliberately left as-is -- confirmed with the user as intended fail-fast behavior for unreleased phase-1 software, not a bug.

- [ ] **Push the fix commits to the existing PR branch**

```bash
git push origin feat/init-command
```

Expected: PR #34 picks up the five new commits automatically (same branch, no new PR needed).
