# snapback

Zero-downtime backup manager for VMware Fusion VMs on macOS. Snapshot,
copy, checksum, prune — scheduled via `launchd`, controlled from the
menu bar via xbar.

Built because [Vimalin](https://www.vimalin.com/) is GUI-only shareware
and opaque about what it's actually doing. This does the same job —
snapshot-consistent backups without shutting the VM down — as a small
Go binary you can read, test, and trust.

## Status

Phase 1 (core CLI) is complete: the backup choreography (snapshot →
sync → copy → merge → archive → checksum), config loading, and the
`VMController` interface — with both a fake for unit tests and a real
`vmcli`-backed implementation — are implemented; `run`, `list`, and
`status` are wired up to that choreography, `init` is an interactive
`huh`-driven config bootstrap (discovers VMs, prompts for
destination/retention, writes config.yaml), and `vm add`/`vm remove
<name>` edit an existing config without re-running the whole wizard.
`cleanup` finds and removes any `snapback-<timestamp>` snapshot orphaned
by a `run` that died mid-choreography, and is serialized against `run`
via a per-VM lock so the two can never touch the same VM's snapshots at
once. The optional TUI layer (progress UI for `run`/`restore`, wizard
for `init`, card drill-down for `status --vm`) is also fully built out.

Phase 3 (Restore) has since landed, pulled ahead of Phase 2 on the
reasoning that an unverified restore path is a bigger risk than manual
`run` invocations in the meantime: `snapback restore` resolves an
archive by ID or by `--vm <name> --latest`, verifies its checksum,
extracts it, confirms the restored disk chain is consistent, and places
it as a new, non-destructively-named `.vmwarevm` bundle next to the
source (`--dest <dir>` to override the inferred location). Current work
is Phase 2 — `launchd` scheduling, `run --all`, and success/failure
notifications. See [`docs/design.md`](docs/design.md) for the full ADR
— architecture, choreography, config schema, risks, and the open
questions still worth verifying locally, plus its own Roadmap section
for the phase-by-phase detail this summary skips.

## Why this exists

VMware won't clone a snapshot taken while a VM is running or
suspended — clone is a dead end for zero-downtime backup. The actual
mechanism: snapshot to freeze disk state (VM keeps running), copy the
frozen files directly at the filesystem level, merge the snapshot back.
No GUI, no shareware license, no mystery about what's happening to your
disk images.

## Requirements

- macOS
- VMware Fusion (Player or Pro — this doesn't clone, so edition doesn't gate the mechanism)
- Go 1.26.5+ (build only; see `go.mod`)
- [xbar](https://xbarapp.com/) or [SwiftBar](https://swiftbar.app/), for the menu bar UI
- `zstd` (optional — `brew install zstd`; falls back to gzip if absent)

## Install

```sh
git clone <repo-url> && cd snapback
go build -o snapback ./cmd/snapback
sudo mv snapback /usr/local/bin/
```

## Quick start

```sh
snapback init                       # scans ~/Virtual Machines and ~/Virtual Machines.localized for .vmwarevm bundles (falls back to manual entry), prompts for destination + retention
snapback run --vm dev-ubuntu        # on-demand backup of one configured VM (--all isn't implemented yet, see Roadmap)
snapback list                       # backup archives, with timestamp and size
snapback status                     # one row per configured VM: last backup, size, archive count
snapback restore --vm dev-ubuntu --latest   # restore the newest archive as a new .vmwarevm, never overwriting the source
```

Full command reference — every subcommand and flag — is in
[`docs/design.md`](docs/design.md#command-reference); `snapback [command]
--help` covers the same ground from the terminal.

## Config

```yaml
# ~/.config/snapback/config.yaml
destination: ~/Backups/snapback
compression: zstd
retention:
  keep_last: 5
  keep_daily: 7
  keep_weekly: 4
vms:
  - name: dev-ubuntu
    vmx: ~/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
    schedule: "0 2 * * *"
notifications:
  enabled: true
```

## Architecture, at a glance

| Piece             | Role                                                                                                      |
| ----------------- | --------------------------------------------------------------------------------------------------------- |
| `snapback` binary | Backup/restore choreography, config, status                                                               |
| `vmcli` / `vmrun` | Snapshot, list, delete — shelled out via a `VMController` interface, not sprinkled through business logic |
| `launchd`         | Scheduling                                                                                                |
| xbar plugin       | Menu bar status + one-click backup                                                                        |

Full detail — including the `checkToolsState` pre-flight quiescing
check, the disk-headroom and orphaned-snapshot risks, and why `vmrest`
and the VIX API were both ruled out — is in the design doc, not
duplicated here.

## Development

Business logic (the snapshot → sync → copy → cleanup choreography) is
tested against a fake `VMController` — no VM, no Fusion, runs in CI.
Real CLI execution is gated behind a build tag / `SNAPBACK_INTEGRATION=1`
env var and run manually against a disposable scratch VM. See
[`docs/design.md`](docs/design.md#no-library-exists-for-snapshot-operations--plan-around-it-dont-fight-it)
for the reasoning.

```sh
go test ./...                          # unit tests, fake controller only
SNAPBACK_INTEGRATION=1 go test ./... -tags=integration  # real vmrun/vmcli, needs a scratch VM
```

## Roadmap

- [x] Phase 1 — Core CLI (`init`, `run --vm`, `list`, `status`, `cleanup`, `vm add`/`vm remove`) plus the optional TUI layer, single VM at a time
- [ ] Phase 2 — `launchd` scheduling, `run --all`, success/failure notifications
- [x] Phase 3 — Restore workflow, manifest-driven integrity check (pulled ahead of Phase 2 — see Status above)
- [ ] Phase 4 — xbar plugin
- [ ] Phase 5 — Retention/pruning policy engine (`prune` command; config already parses `keep_last`/`keep_daily`/`keep_weekly`)
- [ ] Phase 6 (stretch) — SMB/NFS destinations

## Scope

Fusion on macOS, one household, personal use. Not building for
Workstation, not building for Windows as a host, not building a
commercial multi-user tool. Full in/out-of-scope list in the design doc.

## License

MIT
