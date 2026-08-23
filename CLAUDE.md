# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Pre-implementation. No Go source exists yet (no `cmd/` or package directories) —
only `go.mod`, `README.md`, and the design doc. Design is settled; phase 1
(core CLI) is next. Treat `docs/design.md` as the source of truth for
architecture decisions — it's a full ADR (context, alternatives ruled out,
risks, open questions), not just a summary.

Module path: `github.com/xortim/snapback`, Go 1.26.5.

## Commands

Build/lint/test are driven by the `Makefile`:

```sh
make build      # go build with version ldflags -> dist/<goos-goarch>/<bin>
make test       # go test -coverprofile=coverage.out -covermode=atomic -v ./...
make lint       # golangci-lint fmt --diff, then golangci-lint run
make fmt-fix    # apply golangci-lint formatting fixes
make verify     # go mod verify
make tools      # installs golangci-lint and goreleaser (git-cliff install separately)
make all        # clean verify lint test build
```

Run a single test the normal Go way once packages exist:
`go test ./path/to/pkg/... -run TestName -v`

Per the README, the test split is intentional: business logic (the
snapshot → sync → copy → cleanup choreography) is unit-tested against a
fake `VMController` and runs in CI with no VM needed. Real CLI execution
(`vmrun`/`vmcli`) is a separate integration suite gated behind a build tag
and an env var, run manually against a disposable scratch VM:

```sh
go test ./...                                            # unit tests, fake controller only
SNAPBACK_INTEGRATION=1 go test ./... -tags=integration    # real vmrun/vmcli, needs a scratch VM
```

(`docs/design.md` and `README.md` both use `SNAPBACK_INTEGRATION=1` — an
earlier draft of the design doc had a stale `VMBACKUP_INTEGRATION` name
from before the project was renamed; already fixed.)

**Makefile:** originally copied from another project template
(`xortim/penny`) — `EXECUTABLE` is now `snapback` and the leftover
`start-db`/`stop-db` MariaDB targets (no database involved in this
project) have been removed.

## Architecture

The core design problem: there is no structured Go-friendly API for
VMware Fusion snapshot operations. `vmrest` doesn't expose a snapshots
endpoint, and the VIX API has no Go bindings. Everything goes through
shelling out to `vmcli` (Fusion 13+, preferred if its `Snapshot` module
supports structured `-f` output like `Power query` does) or `vmrun`
(fallback, plaintext parsing). Because of that, all snapshot/VM control
is isolated behind one interface so the choreography logic never touches
a shell command directly:

```go
type VMController interface {
    CheckToolsState(vmxPath string) (string, error) // "installed" | "running" | "notInstalled"
    Snapshot(vmxPath, name string) error
    ListSnapshots(vmxPath string) ([]string, error)
    DeleteSnapshot(vmxPath, name string) error
}
```

A real implementation shells out; a fake implementation backs the unit
tests for the choreography state machine. Write choreography logic
against the fake first — this is the intended TDD path for phase 1, not
an afterthought.

**Backup choreography** (the zero-downtime mechanism, full detail in
`docs/design.md#backup-choreography`): `vmrun clone` cannot be used at
all for a running VM — VMware refuses to clone from a snapshot taken
while powered on or suspended. The actual mechanism is snapshot-freeze,
copy the frozen files at the filesystem level, then merge the snapshot
back — never clone:

1. `checkToolsState` pre-flight — only `running` means the guest
   filesystem gets quiesced; anything else is a crash-consistent
   snapshot, recorded as such (`tools_state`) in the manifest rather than
   assumed.
2. Snapshot (`snapback-<timestamp>`) — VM keeps running, new writes
   redirect to a fresh delta file, prior disk state is now frozen.
3. Confirm the snapshot exists via `listSnapshots` before trusting it.
4. `sync`, then copy the `.vmwarevm` bundle to a staging dir — source is
   untouched by ongoing VM writes because of step 2.
5. `deleteSnapshot` — merges the delta back into the source; VM is never
   paused.
6. tar + compress (zstd, gzip fallback if zstd isn't installed) the
   staged copy to the destination.
7. SHA-256 the archive, write `manifest.json` (VM name, guest OS, size,
   comment, timestamp, `tools_state`).
8. Remove the staging copy, prune archives beyond retention policy, fire
   an `osascript` notification.

If the process dies between snapshot and cleanup, the source VM is left
with an orphaned `snapback-<timestamp>` snapshot — `snapback cleanup`
(phase 1) finds and removes anything matching that naming pattern.

**Other components:**

| Piece         | Role                                                                     |
| ------------- | ------------------------------------------------------------------------- |
| `launchd`     | Scheduling, via `~/Library/LaunchAgents/com.tim.snapback.plist`, generated from config `schedule` (cron syntax) fields |
| xbar plugin   | Shell script wrapping `snapback status --xbar`; text above `---` is the menu bar line, everything below is the dropdown |
| Config        | YAML at `~/.config/snapback/config.yaml` — destination, compression, retention (keep_last/daily/weekly), per-VM name/vmx/schedule |

Command surface (`docs/design.md#command-reference`): `init`, `run
--vm <name>` / `run --all`, `list`, `restore <archive-id>` (never
overwrites source, suffixes `- backup yyyy-mm-dd`), `status` /
`status --xbar`, `prune`, `cleanup`.

## Known gotchas worth carrying into implementation

- `vmrun` is not on `$PATH` by default and launchd agents don't inherit
  the interactive shell's `$PATH` — hardcode or config the full path
  (`/Applications/VMware Fusion.app/Contents/Library/vmrun`).
- The copy step temporarily doubles the VM's disk footprint on the
  source volume before compression starts — a real constraint when
  sizing where the copy stages.
- Copying disk files while Fusion still holds open handles on them
  (post-snapshot, pre-merge) needs to be verified locally against a real
  VM with a checksum comparison — don't assume it's safe on APFS without
  checking.
