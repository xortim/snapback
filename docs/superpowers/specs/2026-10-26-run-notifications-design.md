# Run notifications (#86) — design

Status: approved in brainstorming, 2026-10-26
Issue: [#86](https://github.com/xortim/snapback/issues/86)

## Context

ADR-005 (`2026-09-11-launchd-scheduling-design.md`) shipped launchd
scheduling and split notifications out to #86. With scheduling and
drift detection (#85) both landed, the main risk scheduling introduced
is now the open one: an unattended `run` that fails does so silently,
into a log file nobody reads, until someone needs a restore.

What exists today:

- `config.Notifications{Enabled bool}` is parsed, defaulted to `true`
  by the `init` wizard's "Enable notifications" toggle, written by
  `config.Marshal` — and read by nothing.
- `progress.Notifying` is a defined `Stage` that nothing emits.
- The LaunchAgent invokes plain `snapback run --vm <name>` with stdout
  and stderr redirected to `~/Library/Logs/snapback/<vm>.log`, so
  `run` can already tell "scheduled" from "interactive" via the existing
  `runDeps.isTerminal` dependency — no new flag.

## Goals

- With `notifications.enabled: true`, a `run` nobody is watching
  produces a macOS notification on failure, and on success unless
  `notifications.on_success: false`.
- A notification failing never changes `run`'s exit code, its error
  output, or masks the backup's own error.
- The operator has a deliberate way to get the macOS notification
  permission granted while they are at the machine.

## Non-goals

- Notifications for `restore`, `cleanup`, `schedule sync`, or any
  command other than `run`.
- A native (UserNotifications / helper `.app`) notifier. Considered and
  rejected for now: UserNotifications requires a signed app bundle with
  a bundle identifier, which a bare CLI binary is not; a helper `.app`
  adds a second toolchain plus Developer ID signing/notarization. The
  `Notifier` interface below leaves room to swap one in later without
  touching callers.
- Detecting whether notification permission has actually been granted
  (no reliable API for a non-bundled process).

## Decisions

1. **Notify only when stdout is not a terminal.** An interactive run
   already renders its outcome in the TUI; notifying there is
   redundant. Non-TTY covers launchd runs and also manual runs piped or
   redirected to a file, which is acceptable — nobody is watching those
   either.
2. **Success is configurable.** New `notifications.on_success`, which
   `init` writes as `true`. Failures always notify when notifications
   are enabled.
3. **Notify from the CLI, not the choreography.** Notification happens
   in `internal/cli`'s `runVM`, after the plain dispatch path completes.
   Alternatives rejected:
   - *Pass a `Notifier` into `backup.Run` at the `Notifying` stage*
     (design.md's original step 12): `Run` would need to know about
     TTY-ness, and failures before `Run` starts (e.g. a stale plist
     naming a VM no longer in config) would never notify.
   - *A `progress.Reporter` decorator firing on the terminal event*:
     events don't carry the final `Result`/error cleanly, and it has the
     same pre-`Run` blind spot.
4. **`osascript`, behind an interface.** Same fake-vs-real split as
   `vm.Controller` and `launchd.Installer`.
5. **`snapback notify test`** exists to trigger the permission prompt
   interactively; `init` points at it.

## Design

### Package `internal/notify`

```go
// Notification is one user-visible macOS notification.
type Notification struct {
    Title    string
    Subtitle string
    Message  string
    Sound    string // macOS sound name, e.g. "Basso"; "" = no sound
}

type Notifier interface {
    Notify(ctx context.Context, n Notification) error
}
```

- **`OSAScript`** — the real implementation. Runs `/usr/bin/osascript`
  (absolute path: launchd agents don't inherit the interactive `$PATH`)
  with a fixed script that reads its strings from `argv`:

  ```applescript
  on run argv
    display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv) sound name (item 4 of argv)
  end run
  ```

  (with the `sound name` clause omitted when `Sound` is empty — two
  fixed script variants, chosen by Go code, never assembled from user
  data). Every user-controlled string (VM names, error text) travels as
  an argument and is **never** interpolated into script source, so a VM
  named `x" & do shell script "…` cannot inject AppleScript. The exec
  call is an injectable `func(ctx, name string, args ...string) error`
  field so tests can assert exact argv without running `osascript`.
  `Notify` applies its own 5-second timeout on top of the caller's
  context. A non-zero exit returns an error including osascript's
  stderr.
- **`Fake`** — records every `Notification` it receives in a slice;
  optionally returns a configured error. Lives in non-test code (like
  `vm.FakeVMController`/`launchd.FakeInstaller`) so `internal/cli` tests
  can use it.

### Message content

Built by two pure functions in `internal/notify` —
`Success(vmName, archiveID string) Notification` and
`Failure(vmName string, err error, snapshotMayRemain bool) Notification`.
`internal/notify` does not import `internal/backup`; the caller computes
`snapshotMayRemain`:

| Outcome | Title | Subtitle | Message | Sound |
|---|---|---|---|---|
| Success | `snapback` | `<vm> backed up` | archive ID | none |
| Failure | `snapback` | `<vm> backup failed` | first line of the error, truncated to 200 runes with `…` | `Basso` |
| Failure, snapshot may remain | as above | as above | as above, then ` — run snapback cleanup --vm <vm>` | `Basso` |

"Snapshot may remain" uses exactly the condition `warnIfMaybeOrphaned`
already uses (`*backup.RunError` with `Stage >= progress.Snapshotting`);
factor it into a shared predicate so the stderr warning and the
notification can't disagree. The cleanup suffix is appended after
truncation so it is never cut off.

### Config

```go
type Notifications struct {
    Enabled   bool `koanf:"enabled" yaml:"enabled"`
    OnSuccess bool `koanf:"on_success" yaml:"on_success"`
}
```

- No load-time default: snapback has no installed base yet, so there
  is no pre-`on_success` config.yaml to keep compatible. An absent key
  is Go's zero value, `false` (no success notifications) — `init` always
  writes the key explicitly, so this only affects hand-written configs.
- `config.Marshal` always writes the key (no `omitempty`).
- The `init` wizard does **not** gain a second prompt. A fresh init
  writes `on_success: true`; a re-run carries `prior.Notifications.OnSuccess`
  through unchanged (same posture as the other `prior` fields in
  `promptCoreSettings`). `vm add`/`vm remove` already round-trip the
  loaded config and need no change beyond the field existing.
- No new `Validate` rule.

### Wiring in `runVM`

`runDeps` gains `newNotifier func() notify.Notifier` (default: a real
`notify.OSAScript`). A nil `newNotifier` means "never notify", keeping
every existing `runDeps{...}` test literal valid.

Gate, evaluated once after the outcome is known:

```
notify iff cfg.Notifications.Enabled
       && !isTerminalWriter(deps.isTerminal, out)
       && (err != nil || cfg.Notifications.OnSuccess)
```

Covered failure points: everything after config has loaded
successfully — VM-not-found, controller construction failure, and any
`backup.Run` error. A config that fails to load cannot be consulted for
`enabled`, so it does not notify (it still exits non-zero into the
log, and `snapback status` drift detection surfaces broken schedules).

Implementation shape: the gate and send live in one helper (e.g.
`notifyRunOutcome(cmd, deps, cfg, vmName, result, err)`) called from
`runVM` on every return path after config load — via a small wrapper
rather than scattered calls, so a new early-return can't silently skip
it. On the success path it is called with the `*backup.Result`; the
plain path's existing `onSuccess`/`onError` callbacks stay as they are.

Notification send failure: write
`warning: could not send notification: <err>` to stderr (which is the
launchd log) and return the original outcome unchanged.

`progress.Notifying` stays unemitted: notification happens after the
choreography's Reporter is done. Its doc comment in `progress.go` is
updated to say notifications are a CLI concern (this spec) and the
stage is reserved.

### `snapback notify test`

New `notify` command group with one subcommand, `test`:

- Sends `Notification{Title: "snapback", Subtitle: "Test notification",
  Message: "Notifications are working."}` via the same notifier
  factory, **regardless** of `notifications.enabled` (and without
  requiring a config file at all).
- On success prints: `sent a test notification. If nothing appeared,
  allow notifications for Script Editor in System Settings →
  Notifications (osascript notifications are shown under Script
  Editor).`
- On notifier error, returns it (non-zero exit).
- Has its own deps struct with an injectable notifier factory for
  tests.

`init`, after writing config, prints one hint line to stderr when
`Notifications.Enabled` is true: `tip: run \`snapback notify test\` now
so macOS asks for notification permission while you're here`.

### Docs

- `docs/design.md`: choreography step 12 rewritten to say notification
  is done by the `run` command after the choreography, non-TTY only;
  Config Reference gains `on_success`; Command Reference gains
  `snapback notify test`; Phase 2 paragraph notes #86 shipped.
- `CLAUDE.md` project-status paragraph updated (it currently says
  `progress.Notifying` still fires nothing).

## Testing

All unit tests run without `osascript`:

- `internal/notify`:
  - `OSAScript` argv assertion via the injected exec func: absolute
    `/usr/bin/osascript`, fixed script, strings passed as trailing
    args in order; a hostile VM name containing `"` and `& do shell
    script` appears only as an arg, never inside the `-e` script text.
  - Sound variant selection (empty vs `Basso`).
  - Exec error is wrapped and returned; timeout is applied (ctx
    deadline set).
  - Message builders: success content; failure first-line extraction;
    200-rune truncation with `…`; cleanup suffix appended after
    truncation.
- `internal/config`: `on_success: true` parses; `Marshal` emits the
  key.
- `internal/cli` run: table test over `enabled` × TTY × `on_success` ×
  success/failure using `notify.Fake`, asserting send/no-send and
  content; VM-not-found notifies; a notifier error yields a stderr
  warning and leaves the exit error (or success) unchanged; nil
  `newNotifier` never panics.
- `internal/cli` notify test: sends exactly one notification with no
  config present; prints the hint; propagates notifier error.
- `internal/cli` init: hint printed iff notifications enabled; re-init
  preserves a prior `on_success: false`.
- Integration (`-tags=integration`, `SNAPBACK_INTEGRATION=1`): one real
  `OSAScript.Notify` call that asserts a nil error (visual confirmation
  is manual).

## Risks

- **Silent drop without permission.** If Script Editor's notification
  permission was never granted, osascript may exit 0 and show nothing —
  undetectable from our side. Mitigated by `notify test` + the `init`
  tip, not eliminated.
- **Attribution.** Notifications show Script Editor's icon, and
  clicking one opens Script Editor. Accepted; the interface allows a
  helper-app notifier later.
