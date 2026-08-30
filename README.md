# snapback

Zero-downtime backup manager for VMware Fusion VMs on macOS. Snapshot,
copy, checksum, prune — scheduled via `launchd`, controlled from the
menu bar via xbar.

Built because [Vimalin](https://www.vimalin.com/) is GUI-only shareware
and opaque about what it's actually doing. This does the same job —
snapshot-consistent backups without shutting the VM down — as a small
Go binary you can read, test, and trust.

## Status

Phase 1 (core CLI) is in progress. The backup choreography (snapshot →
sync → copy → merge → archive → checksum), config loading, and the
`VMController` interface — with both a fake for unit tests and a real
`vmcli`-backed implementation — are implemented; `init`, `run`, and
`list` are wired up to that choreography, while `status` is still
scaffolded, returning "not yet implemented", and `cleanup` doesn't exist
yet. See
[`docs/design.md`](docs/design.md) for the full ADR — architecture,
choreography, config schema, risks, and the open questions still worth
verifying locally.

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
snapback init        # scans ~/Virtual Machines for .vmwarevm bundles (falls back to manual entry), prompts for destination + retention
snapback run --all   # on-demand backup of every configured VM
snapback status       # last run, next scheduled run, disk usage
```

Full command reference is in [`docs/design.md`](docs/design.md#command-reference).

## Config

```yaml
# ~/.config/snapback/config.yaml
destination: /Volumes/Backups/snapback
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

- [ ] Phase 1 — Core CLI (`init`, `run`, `list`, `status`), single VM at a time
- [ ] Phase 2 — `launchd` scheduling, orphaned-snapshot cleanup
- [ ] Phase 3 — Restore workflow, manifest-driven integrity check
- [ ] Phase 4 — xbar plugin
- [ ] Phase 5 — Retention/pruning policy engine
- [ ] Phase 6 (stretch) — SMB/NFS destinations

## Scope

Fusion on macOS, one household, personal use. Not building for
Workstation, not building for Windows as a host, not building a
commercial multi-user tool. Full in/out-of-scope list in the design doc.

## License

MIT
