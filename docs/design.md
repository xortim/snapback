# ADR-001: snapback — VM Backup Manager for VMware Fusion

**Status:** Accepted
**Date:** 2026-08-12
**Deciders:** Tim

## Scope

**In scope:**

- Snapshot-consistent backups of VMware Fusion VMs without shutting them down
- Scheduled backups via `launchd`, on-demand backups via CLI or menu bar
- Checksummed, compressed backup archives with retention pruning
- Restore workflow that never overwrites the source VM
- xbar/SwiftBar menu bar plugin for status and one-click backup
- macOS native notifications on success/failure

**Out of scope (v1):**

- Encrypted VM support (Vimalin doesn't support this either — not a regression)
- Network share destinations (local/external disk only for v1; add SMB/NFS in a later phase)
- Email notifications (native notification center covers the "don't forget" problem without the SMTP config overhead)
- Windows as a host platform, or VMware Workstation as a target (Fusion on macOS only — this is a personal tool for one household, not a cross-platform product). Guest OS is unaffected by this: a VM running Windows as its guest, backed up from a macOS host running Fusion, is fully in scope.
- Linked clones (Fusion Pro-only feature per VMware docs — confirm your Fusion edition before considering this for a later phase)

## Context

Vimalin does this well but it's shareware, GUI-only, and opaque about what it's actually doing under the hood. You already run a Go + LaunchAgent pattern for the 1Password SSH agent socket workaround — this follows the same shape: a small Go binary, launchd for scheduling, no GUI app to maintain. xbar gives you the menu bar visibility without writing Swift.

## Architecture

| Component         | Responsibility                                                        | Tech                                                            |
| ----------------- | --------------------------------------------------------------------- | --------------------------------------------------------------- |
| `snapback` binary | Backup/restore choreography, config parsing, status reporting         | Go, [cobra](https://github.com/spf13/cobra) (commands), [koanf](https://github.com/knadh/koanf) (config) |
| VM control        | Snapshot, list, delete — no structured API exists for this; see below | `vmcli` (Fusion 13+, confirmed as primary) or `vmrun` (fallback) |
| launchd           | Scheduled execution                                                   | `~/Library/LaunchAgents/com.tim.snapback.plist`                 |
| xbar plugin       | Menu bar status + manual trigger                                      | Shell script wrapping `snapback status --xbar`                  |
| Config            | VM list, destination, retention, schedule                             | YAML at `~/.config/snapback/config.yaml`                        |
| Backup archive    | Compressed, checksummed VM snapshots                                  | tar + zstd (or gzip if zstd isn't installed) + SHA-256 manifest |

### No library exists for snapshot operations — plan around it, don't fight it

Checked the alternatives before committing to shelling out:

- **`vmrest` (Fusion Pro REST API):** ruled out. Covers power state, network adapters, and shared folders — [Broadcom's docs](https://techdocs.broadcom.com/us/en/vmware-cis/desktop-hypervisors/fusion-pro/13-0/using-vmware-fusion/guide-and-help-using-the-vmware-fusion-rest-api.html) don't expose a snapshots endpoint.
- **VIX API** (the legacy C API `vmrun` itself is built on): ruled out. No Go bindings, unclear support status on current Fusion/Apple Silicon, and it's less officially maintained than the CLI wrapping it. Not worth building on something shakier than the fallback.
- **`vmcli`** (Fusion 13+, newer than `vmrun`): has a dedicated `Snapshot` module. **Confirmed (2026-08-23, Fusion with vmrun 1.17.0.25388279, tested against a real Ubuntu ARM VM):** `vmcli Snapshot query -f json` and `vmcli Tools Query -f json` both return clean, well-formed JSON. Note the `--help` text is wrong about the format flag's values — it claims `-f/--format <2, 1, 0>`, but the accepted values are actually `toml`, `yaml`, `json`; the numeric forms are rejected outright. `vmcli` is the primary backing tool — `Snapshot Take`/`Delete`/`query -f json` for snapshot lifecycle, `Tools Query -f json` for the pre-flight quiesce check.
- **`vmrun`:** fallback/reference. `checkToolsState` is present on this install (addresses the [GNS3#1499](https://github.com/GNS3/gns3-gui/issues/1499) "missing on some builds" risk — not an issue here), but its state model is incomplete: against a running VM with no VMware Tools installed in the guest, it returned `unknown` — a fourth state beyond the documented `installed`/`running`/`notInstalled`. After installing `open-vm-tools` in the same guest, it correctly returned `running`, and `vmcli Tools Query -f json` showed `installType: "ovt"` (distinguishing open-vm-tools from VMware's own tools), `running: true`, `runningStatus: "running"`. `vmcli`'s richer fields are still the preferred quiesce-check source over `vmrun checkToolsState`'s flat string — both are now empirically confirmed for the `running` and `unknown`/no-tools cases.

Either way, this is a CLI dependency — there's no way around shelling out. The fix for testability isn't finding a library, it's isolating the shell-out behind an interface so the choreography logic doesn't know or care how snapshots actually happen:

```go
type Controller interface {
    CheckToolsState(vmxPath string) (ToolsState, error) // ToolsInstalled | ToolsRunning | ToolsNotInstalled | ToolsUnknown
    Snapshot(vmxPath, name string) error
    ListSnapshots(vmxPath string) ([]string, error)
    DeleteSnapshot(vmxPath, name string) error
}
```

Implemented as `internal/vm.Controller` (named to avoid the `vm.VMController` stutter), with `ToolsState` a typed string rather than a bare string. `ToolsUnknown` is a real, confirmed return value (not hypothetical) — it's what a Tools-less guest reports. Treat it the same as `ToolsNotInstalled`: gate the quiesce step off, proceed crash-consistent, record it in the manifest's `tools_state` as-is rather than normalizing it away.

The real implementation shells out and parses output. `internal/vm.FakeVMController` backs unit tests for the choreography state machine — no Fusion, no VM, no flakiness, runs in CI on every commit. Reserve actual CLI execution for a small integration suite behind a build tag or `SNAPBACK_INTEGRATION=1` env var, run manually against a disposable scratch VM.

### Backup choreography

`vmrun clone` is off the table for the running-VM case — VMware will not clone from a snapshot that was taken while the VM was powered on or suspended (confirmed in [VMware's snapshot docs](https://techdocs.broadcom.com/us/en/vmware-cis/desktop-hypervisors/workstation-pro/17-0/using-vmware-workstation-pro/using-virtual-machines-in-workstation-pro-user-guide/taking-snapshots-of-virtual-machines/take-a-snapshot-of-a-virtual-machine.html); the clone-from-snapshot picker only ever lists powered-off snapshots). Clone is a dead end for zero-downtime — drop it entirely.

The mechanism that actually works, and the one Vimalin uses under the hood: snapshot to freeze disk state, copy the frozen files directly at the filesystem level, then merge the snapshot back. No clone command involved.

```
1. snapback run --vm <name>
2. vmrun checkToolsState <vmx>                            # pre-flight: confirms "running" before assuming quiesce; log and proceed crash-consistent if not
3. vmrun snapshot <vmx> snapback-<timestamp>              # VM keeps running; new writes redirect to a fresh delta file, prior disk state is now frozen
4. vmrun listSnapshots <vmx>                               # sanity check: confirm the snapshot actually exists before trusting step 3
5. sync                                                     # host-level: flush page cache to disk before reading — cheap insurance, not strictly required but removes a class of doubt
6. cp -R (or rsync) the .vmwarevm bundle -> staging dir    # copies the frozen base disk + snapshot chain; source untouched by the VM's ongoing writes
7. vmrun deleteSnapshot <vmx> snapback-<timestamp>         # merges the live delta back into the source, VM never paused
8. tar + compress the staged copy -> destination
9. sha256 the archive, write manifest.json (VM name, guest OS, size, comment, timestamp, tools_state)
10. rm -rf the staging copy
11. prune archives beyond retention policy
12. osascript notification: success/failure
```

Step 3 is what makes this safe: once the snapshot exists, the disk files being copied in step 6 are frozen — the VM's ongoing writes land in the new delta, not in the files `cp` is reading. Step 7 folds that delta back into the source after the copy completes, so the running VM never sees a pause and never carries a lingering snapshot.

**Guest-level consistency is a separate question from host-level sync, and it's now checkable rather than assumed.** `vmrun checkToolsState <vmx>` returns `installed`, `running`, `notInstalled`, or `unknown`. Only `running` means Fusion can quiesce the guest filesystem before the snapshot commits — anything else is a crash-consistent snapshot, equivalent to a power-cord pull from the guest's perspective. Gate step 3 on this check and record the result in the manifest (`tools_state`), so every backup's consistency guarantee is knowable after the fact rather than assumed at write time. Note: `checkToolsState` has been reported missing in some VIX/Workstation version combinations ([GNS3 #1499](https://github.com/GNS3/gns3-gui/issues/1499)) — confirm it's present on your installed Fusion version before building around it.

## Command Reference

| Command                         | Description                                                                                      |
| ------------------------------- | ------------------------------------------------------------------------------------------------ |
| `snapback init`                 | Interactive config bootstrap — discovers VMs by scanning `~/Virtual Machines` for `.vmwarevm` bundles, prompts for destination/retention (falls back to manual entry if none are found) |
| `snapback run --vm <name>`      | On-demand backup of one VM                                                                       |
| `snapback run --all`            | Backup every VM in config (used by launchd)                                                      |
| `snapback list`                 | List backup archives with timestamp, size, comment                                               |
| `snapback restore <archive-id>` | Restore to a new `.vmwarevm`, suffixed `- backup yyyy-mm-dd`, never overwrites source            |
| `snapback status`               | Human-readable status: last run, next scheduled run, disk usage                                  |
| `snapback status --vm <name>`   | Drill into a single VM: full consistency detail, disk usage, retention policy                    |
| `snapback status --xbar`        | Same data, formatted for xbar plugin consumption                                                 |
| `snapback prune`                | Manually trigger retention cleanup                                                               |

## Config Reference

```yaml
# ~/.config/snapback/config.yaml
destination: /Volumes/Backups/snapback
compression: zstd # zstd | gzip
retention:
  keep_last: 5
  keep_daily: 7
  keep_weekly: 4
vms:
  - name: dev-ubuntu
    vmx: ~/Virtual Machines/dev-ubuntu.vmwarevm/dev-ubuntu.vmx
    schedule: "0 2 * * *" # daily 2am, cron syntax, translated to launchd StartCalendarInterval
    comment_template: "nightly auto-backup"
  - name: win-testbed
    vmx: ~/Virtual Machines/win-testbed.vmwarevm/win-testbed.vmx
    schedule: "0 2 * * 0" # weekly Sunday 2am
notifications:
  enabled: true
```

## xbar Plugin Output Format

xbar plugins are just executables that print to stdout in a specific format — text above the `---` line renders in the menu bar itself, everything below is the dropdown. `snapback status --xbar` should emit:

```
✓ 2 VMs backed up
---
dev-ubuntu: last backup 2h ago (2.1 GB) | color=green
win-testbed: last backup 6d ago (4.8 GB) | color=orange
---
Run All Backups Now | bash='/usr/local/bin/snapback' param1=run param2=--all terminal=false refresh=true
Open Backup Folder | bash='/usr/bin/open' param1=/Volumes/Backups/snapback terminal=false
```

Save the wrapper as `~/Library/Application Support/xbar/plugins/snapback.5m.sh` — the `5m` in the filename sets the 5-minute refresh interval, which is xbar convention, not something you configure separately.

## Risks & Gotchas

- **No Fusion Pro requirement:** since the copy-based approach never calls `clone`, edition doesn't matter — this works the same on Fusion Player and Pro. One less thing to gate on.
- **Disk headroom during the copy:** step 3 temporarily doubles the VM's disk footprint on whatever volume holds the source `.vmwarevm`, before compression starts. Size the source volume's free space accordingly, or stage the copy directly on the destination volume if it has more headroom.
- **Concurrent read on files VMware still has open:** step 3 reads the frozen disk files while Fusion's process still holds handles on them (just not writing to them post-snapshot). This should be safe on APFS for a straight read, but verify it locally with a checksum comparison before trusting it in production — don't take my word for it, confirm against a real VM.
- **Crash between snapshot and cleanup:** if `snapback` dies between steps 3 and 7, you're left with an orphaned `snapback-<timestamp>` snapshot on the source VM. Add a `snapback cleanup --vm <name>` command that finds and removes any snapshot matching that naming pattern — cheap insurance, build it in phase 1.
- **xbar full disk access:** xbar itself needs Full Disk Access in System Settings to reliably read `~/Virtual Machines` and your backup destination if it's outside the sandboxed default locations. Grant this once at setup, note it in the README so future-you doesn't rediscover it the hard way.
- **`vmrun` path isn't on `$PATH` by default:** hardcode or config the full path (`/Applications/VMware Fusion.app/Contents/Library/vmrun`) rather than assuming shell resolution — launchd agents don't inherit your interactive shell's `$PATH` anyway.

## Q&A

**Why not use `vmrun clone` at all?**
Because it can't do what we need — VMware refuses to clone from a snapshot taken while the VM was running or suspended, full stop. The clone command only works against powered-off snapshots. Snapshot-then-copy is the only path to zero-downtime backups; it's also what Vimalin does under the hood.

**Why copy the whole bundle instead of just archiving delta files?**
More portable and easier to verify. Copying the full `.vmwarevm` bundle at snapshot time gives you a normal, openable virtual machine as the pre-compression artifact — you can drag it into Fusion directly if `snapback` itself is ever unavailable. Archiving raw delta files is more space-efficient but couples the backup format to VMware's internal snapshot layout across Fusion versions, which is a worse trade for a personal backup tool than it is for Vimalin's cross-version commercial support burden.

**Why zstd over gzip?**
Faster compression at comparable ratios for VM disk images, which tend to be large and only partially compressible. Fall back to gzip if `zstd` isn't installed (`brew install zstd`) rather than hard-requiring it.

**Why xbar instead of a real menu bar app?**
xbar/SwiftBar gets you 90% of the value — a menu bar icon, a dropdown, click-to-run — for a shell script and zero Xcode. A real Swift menu bar app is a reasonable phase 3+ upgrade if xbar's refresh-interval polling model ever feels too laggy for your taste.

## Roadmap

- [ ] **Phase 1 — Core CLI (in progress):** ~~Confirm `vmcli Snapshot --help` output and whether it supports structured (`-f`) formatting before choosing `vmcli` vs `vmrun` as the backing tool. Confirm `checkToolsState` is present on your installed Fusion version. Define the `VMController` interface (including `CheckToolsState`) and a fake implementation before writing any real shell-out code — write the choreography logic against the fake first.~~ Done: `vmcli` chosen and confirmed, the `VMController` interface with both a fake and a real `vmcli`-backed implementation, and the full backup choreography engine. Remaining: wire `snapback init`, `run`, `list`, `status` (terminal output only) to that choreography — tools check → snapshot → sync → copy → compress → checksum → cleanup, single VM at a time.
- [ ] **Phase 2 — Scheduling:** launchd plist generation from config schedules, `snapback run --all`, orphaned-snapshot cleanup command.
- [ ] **Phase 3 — Restore:** `snapback restore`, non-destructive naming, manifest-driven integrity check before restore.
- [ ] **Phase 4 — xbar plugin:** `status --xbar` output, plugin script, click-to-run wiring.
- [ ] **Phase 5 — Retention:** `prune` command, keep-last/daily/weekly policy engine.
- [ ] **Phase 6 (stretch):** SMB/NFS destinations, encrypted VM support if VMware's automation API ever exposes the password flow cleanly.
