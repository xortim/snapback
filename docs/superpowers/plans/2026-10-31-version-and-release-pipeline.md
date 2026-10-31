# Version Embedding + goreleaser Release Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `snapback --version` reports the real tag + commit, and pushing a `v*` tag publishes a GitHub Release with darwin binaries and checksums.

**Architecture:** A new `internal/version` package holds three ldflags-settable vars (`Version`, `Commit`, `Date`) and a pure `resolve` function that falls back to `runtime/debug.ReadBuildInfo()` when ldflags weren't set (`go install …@vX`, plain `go build`). Cobra's built-in `root.Version` surfaces it as `--version`. The Makefile and a new `.goreleaser.yaml` both stamp the same three symbols; a new tag-triggered `release.yml` reuses `ci.yml` (via `workflow_call`) as its gate, then runs goreleaser.

**Tech Stack:** Go 1.26.5, cobra, goreleaser v2, GitHub Actions (`goreleaser/goreleaser-action@v6`).

**Spec:** GitHub issue #108 ("Embed version in binary (--version) and add goreleaser release pipeline"). There is no separate design doc; the issue's Scope + Acceptance sections are the spec.

## Global Constraints

- Module path: `github.com/xortim/snapback`; version package path: `github.com/xortim/snapback/internal/version`.
- Release targets: `darwin/arm64` and `darwin/amd64` only (macOS-only tool: VMware Fusion, launchd, osascript). `CGO_ENABLED=0`.
- ldflags must be identical in shape between Makefile and goreleaser: `-s -w -X …/internal/version.Version=<v-prefixed tag> -X …/internal/version.Commit=<short sha> -X …/internal/version.Date=<RFC3339 UTC>`.
- The `Version` value always carries the leading `v` (e.g. `v1.0.0-rc.2`), matching git tags.
- Changelog source: **goreleaser's built-in changelog** (decision for issue's "pick one"): it needs no extra tool in CI, and commits already follow conventional-commit prefixes it can group on. git-cliff (`make changelog`, the `tools` note) is removed; no `cliff.toml`, no `CHANGELOG.md`.
- Prerelease tags (`-rc.N`) must be marked prerelease on GitHub (`release.prerelease: auto`).
- Homebrew tap (`brews`): **out of scope** (optional in the issue; needs a separate tap repo + token). No xbar plugin script exists yet (Phase 4), so archives ship `README.md` + `LICENSE` only.
- Conventional commits with package scope (`feat(version):`, `build(make):`, `ci:`, `docs:`). Run `make lint test build` before every push.

## Review Focus

- **Plain `go build ./cmd/snapback`** (README's documented install, no ldflags) must report the VCS-derived module version and commit, with `-dirty` when the tree is modified — not a bare `dev`. Pinned in Task 1 (`TestResolve_NoLdflags_UsesBuildInfo*`).
- **`go install github.com/xortim/snapback/cmd/snapback@v1.0.0`** has a module version but no `vcs.*` settings: must print `v1.0.0` with no empty `(commit , built )` noise. Pinned in Task 1 (`TestResolve_GoInstall_NoVCS` + `TestInfoString_*`).
- **Stale binary after tagging:** `$(ARTIFACT)` only rebuilds on `.go` changes, so `make build` right after `git tag` reuses a binary stamped with the old version. Acceptance check uses `make clean build`; README's release section says so (Task 5).
- **Snapshot builds masquerading as a release:** `make snapshot` must not stamp a bare tag like `v1.0.0-rc.1`. Pinned in Task 3 by checking the snapshot binary's `--version` contains `SNAPSHOT`.
- **Release publishing despite red tests:** the goreleaser job must `needs:` the reused CI jobs, so a failing lint/test on the tagged commit publishes nothing. Pinned in Task 4 by the `needs: [ci]` + `workflow_call` wiring and a `goreleaser check` in PR CI.

---

### Task 1: `internal/version` package

**Files:**
- Create: `internal/version/version.go`
- Test: `internal/version/version_internal_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `var Version, Commit, Date string` (ldflags targets; `Version` defaults to `"dev"`, others `""`)
  - `type Info struct { Version, Commit, Date string }`
  - `func Get() Info`
  - `func (i Info) String() string`
  - unexported `func resolve(version, commit, date string, bi *debug.BuildInfo, ok bool) Info`

- [ ] **Step 1: Write the failing tests**

```go
package version

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{Main: debug.Module{Path: "github.com/xortim/snapback", Version: mainVersion}}
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func TestResolve_LdflagsWin(t *testing.T) {
	bi := buildInfo("v9.9.9", map[string]string{"vcs.revision": "ffffffffffffffff", "vcs.time": "2000-01-01T00:00:00Z"})
	got := resolve("v1.0.0-rc.2", "abc1234", "2026-10-31T12:00:00Z", bi, true)
	want := Info{Version: "v1.0.0-rc.2", Commit: "abc1234", Date: "2026-10-31T12:00:00Z"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoLdflags_UsesBuildInfo(t *testing.T) {
	bi := buildInfo("v1.0.0-rc.1.0.20261031120000-0123456789ab", map[string]string{
		"vcs.revision": "0123456789abcdef0123",
		"vcs.time":     "2026-10-31T12:00:00Z",
		"vcs.modified": "false",
	})
	got := resolve("dev", "", "", bi, true)
	want := Info{Version: "v1.0.0-rc.1.0.20261031120000-0123456789ab", Commit: "0123456", Date: "2026-10-31T12:00:00Z"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoLdflags_UsesBuildInfo_Dirty(t *testing.T) {
	bi := buildInfo("(devel)", map[string]string{
		"vcs.revision": "0123456789abcdef0123",
		"vcs.modified": "true",
	})
	got := resolve("dev", "", "", bi, true)
	want := Info{Version: "dev", Commit: "0123456-dirty"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_GoInstall_NoVCS(t *testing.T) {
	got := resolve("dev", "", "", buildInfo("v1.0.0", nil), true)
	want := Info{Version: "v1.0.0"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoBuildInfo(t *testing.T) {
	got := resolve("dev", "", "", nil, false)
	if got != (Info{Version: "dev"}) {
		t.Errorf("resolve() = %+v, want just Version=dev", got)
	}
}

func TestResolve_ShortRevisionNotTruncatedPastLength(t *testing.T) {
	got := resolve("dev", "", "", buildInfo("", map[string]string{"vcs.revision": "abc"}), true)
	if got.Commit != "abc" {
		t.Errorf("Commit = %q, want %q", got.Commit, "abc")
	}
}

func TestInfoString(t *testing.T) {
	tests := []struct {
		name string
		in   Info
		want string
	}{
		{"all", Info{"v1.0.0-rc.2", "abc1234", "2026-10-31T12:00:00Z"}, "v1.0.0-rc.2 (commit abc1234, built 2026-10-31T12:00:00Z)"},
		{"commit only", Info{Version: "v1.0.0", Commit: "abc1234"}, "v1.0.0 (commit abc1234)"},
		{"date only", Info{Version: "v1.0.0", Date: "2026-10-31T12:00:00Z"}, "v1.0.0 (built 2026-10-31T12:00:00Z)"},
		{"version only", Info{Version: "v1.0.0"}, "v1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/version/... -v`
Expected: FAIL — build error, `undefined: resolve` / `undefined: Info`.

- [ ] **Step 3: Implement**

```go
// Package version reports which build of snapback is running. Version,
// Commit, and Date are set at link time via -ldflags "-X ..." (see the
// Makefile and .goreleaser.yaml); when they aren't (go install ...@vX,
// plain go build), Get falls back to the module and VCS info the Go
// toolchain embeds on its own.
package version

import (
	"runtime/debug"
	"strings"
)

const devVersion = "dev"

// Set via -ldflags "-X github.com/xortim/snapback/internal/version.<Name>=...".
var (
	Version = devVersion
	Commit  = ""
	Date    = ""
)

// Info is the resolved build identity.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Get resolves the running binary's build identity.
func Get() Info {
	bi, ok := debug.ReadBuildInfo()
	return resolve(Version, Commit, Date, bi, ok)
}

// resolve fills any field ldflags left unset from the toolchain-embedded
// build info. Kept pure so tests can drive it with a synthetic BuildInfo.
func resolve(version, commit, date string, bi *debug.BuildInfo, ok bool) Info {
	info := Info{Version: version, Commit: commit, Date: date}
	if info.Version == "" {
		info.Version = devVersion
	}
	if !ok || bi == nil {
		return info
	}

	if info.Version == devVersion && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}

	var revision, vcsTime string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if info.Commit == "" && revision != "" {
		info.Commit = revision[:min(7, len(revision))]
		if modified {
			info.Commit += "-dirty"
		}
	}
	if info.Date == "" {
		info.Date = vcsTime
	}
	return info
}

// String renders e.g. "v1.0.0-rc.2 (commit abc1234, built 2026-10-31T12:00:00Z)",
// omitting whichever of commit/date is unknown.
func (i Info) String() string {
	var parts []string
	if i.Commit != "" {
		parts = append(parts, "commit "+i.Commit)
	}
	if i.Date != "" {
		parts = append(parts, "built "+i.Date)
	}
	if len(parts) == 0 {
		return i.Version
	}
	return i.Version + " (" + strings.Join(parts, ", ") + ")"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/version/... -v`
Expected: PASS (all 7 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/version
git commit -m "feat(version): add build identity package with BuildInfo fallback"
```

---

### Task 2: `snapback --version`

**Files:**
- Modify: `internal/cli/root.go` (`NewRootCmd`)
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `version.Get() version.Info`, `version.Info.String()` (Task 1).
- Produces: `snapback --version` printing `snapback <Info.String()>\n` to the command's stdout.

- [ ] **Step 1: Write the failing test** (append to `internal/cli/root_test.go`; add `"bytes"`, `"strings"` imports)

```go
func TestNewRootCmd_VersionFlag(t *testing.T) {
	root := cli.NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "snapback ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("--version output = %q, want \"snapback <version>\\n\"", got)
	}
	if strings.TrimSpace(strings.TrimPrefix(got, "snapback ")) == "" {
		t.Errorf("--version output = %q, want a non-empty version", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/cli/ -run TestNewRootCmd_VersionFlag -v`
Expected: FAIL — `unknown flag: --version`.

- [ ] **Step 3: Implement** — in `NewRootCmd`, add `Version` to the struct literal and set the template right after it; import `github.com/xortim/snapback/internal/version`.

```go
	root := &cobra.Command{
		Use:     "snapback",
		Short:   "Zero-downtime backup manager for VMware Fusion VMs",
		Version: version.Get().String(),
	}
	// Cobra's default is "snapback version <v>"; the Info string already
	// reads as a version, so drop the redundant word.
	root.SetVersionTemplate("snapback {{.Version}}\n")
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli/... -v -run 'TestNewRootCmd'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/root.go internal/cli/root_test.go
git commit -m "feat(cli): add --version flag"
```

---

### Task 3: Makefile ldflags + `.goreleaser.yaml`

**Files:**
- Modify: `Makefile` (vars at top; `tools` target; `##@ Release` section)
- Create: `.goreleaser.yaml`
- Modify: `.github/workflows/ci.yml` (lint job: add `goreleaser check`)

**Interfaces:**
- Consumes: `internal/version.{Version,Commit,Date}` symbols (Task 1).
- Produces: `make build`, `make snapshot`, `make release`, `make release-check` targets; `.goreleaser.yaml` used by Task 4's workflow.

- [ ] **Step 1: Confirm the bug** — `make clean build && dist/snapback --version`
Expected: prints something other than the tag (VCS fallback from Task 1, e.g. `snapback v1.0.0-rc.1.0.… (commit …)`), proving the `conf.*` ldflags do nothing.

- [ ] **Step 2: Fix Makefile ldflags.** Replace the `GITVERSION`/`GOLDFLAGS` lines region so the top of the Makefile reads:

```make
COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null)
GITVERSION  ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo "v0.0.0-$(COMMIT_HASH)")
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GO          ?= go
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)
PACKAGENAME := $(shell go list -m -f '{{.Path}}')
VERSION_PKG := $(PACKAGENAME)/internal/version
GOLDFLAGS   ?= -s -w -X $(VERSION_PKG).Version=$(GITVERSION) -X $(VERSION_PKG).Commit=$(COMMIT_HASH) -X $(VERSION_PKG).Date=$(BUILD_DATE)
```

(Leave `GOBUILD`, `GO_FILES`, `EXECUTABLE`, etc. as they are.)

- [ ] **Step 3: Verify** — `make clean build && dist/snapback --version`
Expected: `snapback v1.0.0-rc.1-<n>-g<sha> (commit <sha>, built <now>)` (or exactly `v1.0.0-rc.1` if HEAD is the tag).

- [ ] **Step 4: Replace the Release section and the git-cliff note.** In `tools`, delete the `@echo "NOTE: git-cliff ..."` line. Replace everything from `##@ Release` through the `release` target with:

```make
###############
##@ Release
# Requires: goreleaser (make tools). Release notes come from goreleaser's
# built-in changelog (see .goreleaser.yaml); there is no CHANGELOG.md.

.PHONY: release-check
release-check: ## Validate .goreleaser.yaml
	@$(MAKE) --no-print-directory log-$@
	goreleaser check

.PHONY: snapshot
snapshot: ## Build a snapshot release locally (no publish)
	@$(MAKE) --no-print-directory log-$@
	goreleaser release --snapshot --clean

.PHONY: release
release: ## Create a release with goreleaser (CI does this on v* tags)
	@$(MAKE) --no-print-directory log-$@
	goreleaser release --clean
```

- [ ] **Step 5: Write `.goreleaser.yaml`**

```yaml
# yaml-language-server: $schema=https://goreleaser.com/static/schema.json
version: 2

project_name: snapback

before:
  hooks:
    - go mod verify

builds:
  - id: snapback
    main: ./cmd/snapback
    binary: snapback
    env:
      - CGO_ENABLED=0
    goos: [darwin]
    goarch: [arm64, amd64]
    flags: [-trimpath]
    mod_timestamp: "{{ .CommitTimestamp }}"
    # Same symbols as the Makefile's GOLDFLAGS. "v{{ .Version }}" (not
    # .Tag) so snapshot builds get the SNAPSHOT version_template below
    # instead of masquerading as the last real tag.
    ldflags:
      - -s -w
      - -X github.com/xortim/snapback/internal/version.Version=v{{ .Version }}
      - -X github.com/xortim/snapback/internal/version.Commit={{ .ShortCommit }}
      - -X github.com/xortim/snapback/internal/version.Date={{ .CommitDate }}

archives:
  - formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - README.md
      - LICENSE

checksum:
  name_template: checksums.txt

snapshot:
  version_template: "{{ .Version }}-SNAPSHOT-{{ .ShortCommit }}"

changelog:
  use: git
  sort: asc
  filters:
    exclude:
      - "^Merge "
  groups:
    - title: Features
      regexp: '^feat(\([^)]*\))?!?:'
      order: 0
    - title: Bug fixes
      regexp: '^[a-z,]*fix(\([^)]*\))?!?:'
      order: 1
    - title: Other
      order: 999

release:
  prerelease: auto
```

- [ ] **Step 6: Validate + snapshot**

Run: `make release-check`
Expected: `config is valid` (no deprecation warnings; if goreleaser flags a key as deprecated, switch to the replacement it names).

Run: `make snapshot && ls dist/ && dist/snapback_darwin_arm64*/snapback --version`
Expected: `dist/` contains `snapback_*_darwin_arm64.tar.gz`, `snapback_*_darwin_amd64.tar.gz`, `checksums.txt`; the binary prints `snapback v1.0.0-rc.1-SNAPSHOT-<sha> (commit <sha>, built <date>)` — must contain `SNAPSHOT`. `tar -tzf dist/snapback_*_darwin_arm64.tar.gz` lists `snapback`, `README.md`, `LICENSE`. (On an Intel Mac, run the amd64 binary instead.)

- [ ] **Step 7: Gate config breakage in PR CI.** In `.github/workflows/ci.yml`'s `lint` job, after `- run: make lint`, add:

```yaml
      - run: make release-check
```

(`make tools` in that job already installs goreleaser.)

- [ ] **Step 8: Run CI targets, commit**

Run: `make clean lint test build`
Expected: all pass.

```bash
git add Makefile .goreleaser.yaml .github/workflows/ci.yml
git commit -m "build(make): stamp internal/version and add goreleaser config

Drops the dead conf.* ldflags inherited from the penny template and the
git-cliff changelog target in favor of goreleaser's built-in changelog."
```

---

### Task 4: Tag-triggered release workflow

**Files:**
- Modify: `.github/workflows/ci.yml` (`on:` block)
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `.goreleaser.yaml` (Task 3); `ci.yml`'s `lint`/`test`/`build` jobs.
- Produces: GitHub Release on `v*` tag push.

- [ ] **Step 1: Make CI reusable.** In `ci.yml`, change the `on:` block to:

```yaml
on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:
  workflow_call:
```

- [ ] **Step 2: Write `.github/workflows/release.yml`**

```yaml
name: Release

on:
  push:
    tags: ['v*']

permissions:
  contents: read

jobs:
  # Same lint/test/build gate as every PR; nothing publishes unless it passes.
  ci:
    uses: ./.github/workflows/ci.yml

  goreleaser:
    needs: [ci]
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # goreleaser needs full history + tags for the changelog
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: '~> v2'
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 3: Lint the workflows locally** (if `actionlint` is installed; `brew install actionlint` otherwise skip and note it)

Run: `actionlint .github/workflows/*.yml`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "ci: publish GitHub Releases from v* tags via goreleaser"
```

---

### Task 5: Docs

**Files:**
- Modify: `README.md` (`## Install`; new `## Releasing` section after `## Config` or before `## Roadmap`, wherever release/dev notes fit best)
- Modify: `CLAUDE.md` (`## Commands` block + the Makefile note)

- [ ] **Step 1: README Install** — replace the `## Install` block with:

````markdown
## Install

Download the `darwin_arm64` (Apple silicon) or `darwin_amd64` (Intel)
tarball from the [latest release](https://github.com/xortim/snapback/releases),
verify it against `checksums.txt`, and put `snapback` on your `$PATH`:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf snapback_*_darwin_arm64.tar.gz snapback
sudo mv snapback /usr/local/bin/
snapback --version
```

The binary isn't signed or notarized. If you downloaded the tarball with
a browser, macOS quarantines it and Gatekeeper will refuse to run it;
clear that with `xattr -d com.apple.quarantine /usr/local/bin/snapback`.
(`curl -LO` downloads aren't quarantined.)

Or build from source (Go 1.26.5+):

```sh
go install github.com/xortim/snapback/cmd/snapback@latest
```
````

- [ ] **Step 2: README Releasing section**

````markdown
## Releasing

Pushing a `v*` tag runs `.github/workflows/release.yml`: the same
lint/test/build gate as CI, then goreleaser builds darwin/arm64 +
darwin/amd64, and publishes a GitHub Release with tarballs,
`checksums.txt`, and release notes grouped from conventional-commit
prefixes. Tags with a prerelease suffix (`-rc.N`) are marked prerelease.

```sh
make snapshot                   # dry run: full goreleaser build into dist/, no publish
git tag -a v1.0.0-rc.2 -m v1.0.0-rc.2 && git push origin v1.0.0-rc.2
```

`make build` only rebuilds when `.go` files change, so after tagging use
`make clean build` to get a binary stamped with the new tag.
````

- [ ] **Step 3: CLAUDE.md** — in `## Commands`, add to the make block:

```sh
make snapshot   # goreleaser dry-run build into dist/ (no publish)
make release-check  # goreleaser check (also runs in CI's lint job)
```

and replace the `**Makefile:**` paragraph's last sentence with: "`EXECUTABLE` is now `snapback`, the leftover `start-db`/`stop-db` MariaDB targets have been removed, and the dead `conf.*` ldflags were replaced by `internal/version` (#108). Releases go through goreleaser on `v*` tags (`.github/workflows/release.yml`); goreleaser's built-in changelog replaced git-cliff."

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: document binary install, --version, and release flow"
```

- [ ] **Step 5: Final check, push, PR** (per repo workflow — push + PR by default)

Run: `make clean lint test build && dist/snapback --version`
Expected: all green; version line printed.

PR body: `Closes #108` is **not** used yet — use `Part of #108`, since acceptance requires cutting rc.2 after merge (Task 6).

---

### Task 6: Cut `v1.0.0-rc.2` end to end (after the PR merges)

Outward-facing — **confirm with Tim before pushing the tag.**

- [ ] **Step 1:** `git switch main && git pull && make clean build && dist/snapback --version` — confirms main is green locally.
- [ ] **Step 2:** `git tag -a v1.0.0-rc.2 -m v1.0.0-rc.2 && git push origin v1.0.0-rc.2`
- [ ] **Step 3:** `gh run watch $(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')` — expect `ci` then `goreleaser` to succeed.
- [ ] **Step 4:** `gh release view v1.0.0-rc.2` — expect `Pre-release`, two `darwin` tarballs + `checksums.txt`, grouped notes.
- [ ] **Step 5:** Download the arm64 tarball, `shasum -a 256 -c checksums.txt --ignore-missing`, extract, run `./snapback --version` → `snapback v1.0.0-rc.2 (commit <sha>, built <date>)`.
- [ ] **Step 6:** `gh issue close 108 --reason completed --comment "Shipped via v1.0.0-rc.2: <release URL>"`.
