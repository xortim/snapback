# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Phase 1 (core CLI) is in progress. Real Go source exists under
`cmd/snapback`, `internal/backup`, `internal/cli`, `internal/config`,
`internal/progress`, and `internal/vm`.

Landed so far (closed issues #2–#8, #11, #14–#16, #19, #21; merged PRs
#12, #13, #18, #20, #22–#24, #33): the vmcli-vs-vmrun investigation,
cobra CLI scaffolding + koanf config loading, the `vm.Controller`
interface with `vm.FakeVMController`, the full backup choreography
(snapshot → sync → copy → merge → archive → checksum) in
`internal/backup`, `Run()` wired to `context.Context` and
`progress.Reporter`, CI, a real (non-fake) `vm.VMCLIController`
implementation backed by `vmcli` plus its integration suite, `run --vm`
and `list` wired to that choreography, tilde-expansion and field
validation in `config.Load`, and `init` (interactive config bootstrap —
discovers VMs by scanning `~/Virtual Machines`, prompts for
destination/retention, writes `config.yaml`).

`status` (terminal output only, PR #40) and `cleanup` (PR #38) have
since landed too, closing out every named sub-issue under the epic (#1
"Phase 1 — Core CLI"). Current work is #17, the optional TUI layer:
`init` is now an interactive `huh` wizard (PR #51), plus a small backlog
of follow-ups filed after its first real-world use (#47–#49, #52–#54),
including `vm add`/`vm remove <name>` for editing an existing config
without re-running the whole wizard.

Phase 3 (Restore) has since landed: `snapback restore` resolves an
archive by ID or by `--vm <name> --latest`, verifies its checksum,
extracts it, confirms the restored disk chain is consistent, and places
it as a new, non-destructively-named `.vmwarevm` bundle (never
overwriting the source) — with `--dest <dir>` to override the inferred
location.

Treat `docs/design.md` as the source of truth for architecture decisions
— it's a full ADR (context, alternatives ruled out, risks, open
questions), not just a summary.

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

Run a single test the normal Go way:
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
type Controller interface {
    CheckToolsState(vmxPath string) (ToolsState, error) // ToolsInstalled | ToolsRunning | ToolsNotInstalled | ToolsUnknown
    Snapshot(vmxPath, name string) error
    ListSnapshots(vmxPath string) ([]string, error)
    DeleteSnapshot(vmxPath, name string) error
}
```

Implemented in `internal/vm` (package `vm`, so usage is `vm.Controller` —
named to avoid the `vm.VMController` stutter). `ToolsState` is a typed
string with those four constants, not a bare string — `unknown` is a
real, confirmed guest state (see `docs/design.md`), not a hypothetical.

`vm.VMCLIController` (`internal/vm/vmcli.go`) is the real implementation,
shelling out to `vmcli`; `vm.FakeVMController` (`internal/vm`) backs the
unit tests for the choreography state machine. Write choreography logic
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
   timestamp, `tools_state`).
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
`status --xbar`, `prune`, `cleanup`, `vm add`, `vm remove <name>`.

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
- **Confirmed real incident (2026-09-08):** `run` against a powered-off
  VM merged a snapshot successfully per `vmcli`'s exit code, but the
  on-disk consolidation silently failed to apply — the VM couldn't power
  on afterward (`vmware-vdiskmanager -e` reported "needs repair" on the
  affected `.vmdk`, confirmed against both the live VM and the archived
  copy). `Run` now verifies disk-chain consistency both before taking a
  new snapshot and again right after the merge, via
  `vm.Controller.CheckDiskConsistency` (`vmware-vdiskmanager -e`) — but
  **only when the VM isn't running**: its disk files are held open by a
  live `vmware-vmx` process, so the check can't open them at all (fails
  on lock contention, not a real verdict), and doesn't need to — a
  running VM's chain is already known-good by the fact that it's
  running. Recovery, if this happens again: `vmware-vdiskmanager -R
  <disk>.vmdk` repaired both the live VM and a copy of the archived one
  in this incident, cleanly, with no data loss.
