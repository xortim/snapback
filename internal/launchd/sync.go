package launchd

import (
	"bytes"
	"fmt"

	"github.com/xortim/snapback/internal/config"
)

// SyncResult records what Sync changed, for callers to report to the
// user (internal/cli's `schedule sync`, `vm add`, `vm remove`, `init`
// all print this the same way).
type SyncResult struct {
	Installed []string // VM names newly given a LaunchAgent
	Updated   []string // VM names whose LaunchAgent was rewritten and re-bootstrapped
	Removed   []string // labels booted out and deleted
	// Skipped lists VM names whose LaunchAgent needed a change (new
	// install or updated content) but were left alone because a backup
	// was in progress for that VM at the time -- see RunningChecker.
	// Neither the on-disk plist nor the loaded job were touched, so a
	// later Sync call (there's no automatic retry -- something has to
	// invoke Sync again, e.g. a re-run of `snapback schedule sync`) will
	// detect the same diff and apply it then.
	Skipped []string
}

// IsEmpty reports whether Sync found nothing to do.
func (r SyncResult) IsEmpty() bool {
	return len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Removed) == 0 && len(r.Skipped) == 0
}

// RunningChecker reports whether vmName currently has a backup in
// progress (see backup.IsRunning). Sync consults this before tearing
// down a LaunchAgent whose content needs to change -- Bootout unloads
// the running job, which would otherwise kill a scheduled backup
// mid-choreography (see CLAUDE.md's "Known gotchas" for the
// orphaned-snapshot incident this guards against). It's checked
// whenever Sync has determined an agent's plist content actually
// differs from what's on disk (see Sync's use of Installer.Read) --
// including a fresh install, not just an update: "install" just means
// "no plist on disk" (see the doc comment on the installer.Bootout call
// below), and a job can still be loaded in launchd's session with its
// plist hand-deleted, in which case a naive install-only check would
// miss it and boot out unconditionally. An already-in-sync VM (content
// unchanged) never consults this at all, so a flaky or unmounted backup
// destination can't break a `schedule sync` that has nothing to do.
//
// This check has a TOCTOU gap: it probes the lock and releases it
// immediately (see backup.IsRunning), so a launchd-started run that
// begins in the narrow window between the probe and Sync's subsequent
// Bootout call can still be killed. That's strictly better than before
// this check existed (which always killed an in-flight run whenever
// content changed), but it is not a complete guarantee.
//
// Scope limit: the loop below covers every VM still present in vms,
// whether it's newly scheduled, updated, or had its Schedule cleared to
// "" (which routes it into the removal loop further down, since it's no
// longer in desiredLabels) -- the real config.VM.Name is available for
// all of those. A VM removed from vms entirely (`vm remove`) is also
// covered, but only because `vm remove` passes its own name through
// Sync's removedNames parameter explicitly -- the removal loop has no
// way to recover a real Name from a bare sanitized label on its own.
// Two paths still fall back to the unconditional-bootout case below:
// a VM entry deleted by hand directly in config.yaml, followed by
// `schedule sync` (nothing calling Sync in that path ever knew the
// deleted VM's real Name, so there's no name to pass as removedNames);
// and `init --force` dropping a previously-scheduled VM that discovery
// didn't carry forward -- its real Name is known and confirmed with the
// operator (internal/tui/init.go's confirmUnmatchedPriorSchedules), but
// runInit does not yet thread it through as a removedNames argument.
// Both remain tracked under issue #96.
type RunningChecker func(vmName string) (bool, error)

// Sync reconciles installer's on-disk/loaded state with vms: every VM
// with a non-empty Schedule gets its plist written and (re-)bootstrapped
// if it's new or its content changed since the last sync -- always
// booted out first, so a stale or hand-orphaned loaded job can't make
// Bootstrap fail. Every plist installer already knows about that no
// longer corresponds to a scheduled VM in vms gets booted out and
// removed. Safe to call repeatedly -- an already-in-sync config
// produces an empty SyncResult and, for each already-scheduled VM, only
// the one Read() beyond the initial List() (no Write, no isRunning
// probe, no Bootout/Bootstrap).
//
// binaryPath is embedded into each plist's ProgramArguments as the
// snapback binary to invoke (os.Executable(), resolved by the caller) --
// see ADR-005's Risks for the known gap if the binary is later moved
// without a resync.
//
// removedNames names any VM(s) no longer present in vms at all (e.g.
// `vm remove <name>`) whose in-progress-backup state should still be
// checked before their LaunchAgent is torn down, the same as any VM
// still in vms -- see RunningChecker's Scope limit above for why this
// can't be recovered from vms itself. Most callers (schedule sync,
// init, vm add) have nothing to pass here and omit it entirely.
//
// If reconciliation fails partway through (an Installer call returns an
// error after the collision check and Agent-building pass have already
// succeeded), the returned SyncResult still reflects everything
// completed before the failure -- it is not zeroed out -- so callers can
// report partial progress to the user alongside the error.
func Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker, removedNames ...string) (SyncResult, error) {
	if err := DetectCollisions(vms); err != nil {
		return SyncResult{}, err
	}

	// nameByLabel covers every VM still in vms, scheduled or not, plus
	// any name supplied via removedNames -- used by the removal loop
	// below to recover the real config.VM.Name for a VM whose schedule
	// was just cleared to "" (still configured, just no longer desired)
	// or whose caller (e.g. vm remove) already knows its name despite it
	// being gone from vms entirely, so both cases can be running-checked
	// like any other VM instead of falling into the unconditional-
	// bootout gap that applies only when no name is available at all.
	nameByLabel := make(map[string]string, len(vms)+len(removedNames))
	for _, v := range vms {
		nameByLabel[labelPrefix+sanitizeLabel(v.Name)] = v.Name
	}
	for _, name := range removedNames {
		label := labelPrefix + sanitizeLabel(name)
		if _, ok := nameByLabel[label]; !ok {
			nameByLabel[label] = name
		}
	}

	var scheduled []Agent
	desiredLabels := make(map[string]bool)
	for _, v := range vms {
		if v.Schedule == "" {
			continue
		}
		agent, err := buildAgent(v, binaryPath)
		if err != nil {
			return SyncResult{}, err
		}
		scheduled = append(scheduled, agent)
		desiredLabels[agent.Label] = true
	}

	existingLabels, err := installer.List()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list installed schedules: %w", err)
	}
	existing := make(map[string]bool, len(existingLabels))
	for _, l := range existingLabels {
		existing[l] = true
	}

	var result SyncResult
	for _, agent := range scheduled {
		install := !existing[agent.Label]

		// Determine whether this agent's content actually needs to
		// change *before* calling Write or consulting isRunning --
		// Write both diffs and persists in one call, so deciding
		// "changed" from its return value would mean either probing
		// isRunning (and, for a real destination, touching the
		// filesystem) for every already-in-sync VM on every Sync, or
		// leaving a skip's plist half-written. Read-then-compare keeps
		// both Write and isRunning off the hot, common path where
		// nothing needs to happen.
		var changed bool
		if install {
			changed = true
		} else {
			existingData, ok, err := installer.Read(agent.Label)
			if err != nil {
				return result, fmt.Errorf("read existing plist for %q: %w", agent.VMName, err)
			}
			candidate, err := renderPlist(agent)
			if err != nil {
				return result, fmt.Errorf("render plist for %q: %w", agent.VMName, err)
			}
			changed = !ok || !bytes.Equal(existingData, candidate)
		}
		if !changed {
			continue
		}

		running, err := isRunning(agent.VMName)
		if err != nil {
			return result, fmt.Errorf("check running state for %q: %w", agent.VMName, err)
		}
		if running {
			result.Skipped = append(result.Skipped, agent.VMName)
			continue
		}

		plistPath, _, err := installer.Write(agent)
		if err != nil {
			return result, fmt.Errorf("write plist for %q: %w", agent.VMName, err)
		}
		// Bootout before Bootstrap on *both* paths, not just the update
		// path. Bootstrap fails against an already-loaded label, and
		// "install" here only means "no plist on disk" (that's all
		// Installer.List can see) -- a job can still be loaded in
		// launchd's session with its plist deleted by hand, which would
		// otherwise make an unrelated command like `vm add` fail after it
		// had already written config.yaml. Bootout is idempotent for a
		// label that isn't loaded (see isNotLoadedError), so the extra
		// call is free on the normal install path.
		if err := installer.Bootout(agent.Label); err != nil {
			return result, fmt.Errorf("bootout stale %q: %w", agent.VMName, err)
		}
		if err := installer.Bootstrap(plistPath); err != nil {
			return result, fmt.Errorf("bootstrap %q: %w", agent.VMName, err)
		}
		if install {
			result.Installed = append(result.Installed, agent.VMName)
		} else {
			result.Updated = append(result.Updated, agent.VMName)
		}
	}

	for _, label := range existingLabels {
		if desiredLabels[label] {
			continue
		}

		// vmName is set for a VM still present in vms (schedule cleared
		// to "") or one whose real name the caller supplied via
		// removedNames (e.g. vm remove) -- see nameByLabel's doc comment
		// and RunningChecker's Scope limit above. It's absent only when
		// nothing in the call path ever knew the real Name at all (a
		// hand-edited config.yaml followed by `schedule sync`).
		if vmName, ok := nameByLabel[label]; ok {
			running, err := isRunning(vmName)
			if err != nil {
				return result, fmt.Errorf("check running state for %q: %w", vmName, err)
			}
			if running {
				result.Skipped = append(result.Skipped, vmName)
				continue
			}
		}

		if err := installer.Bootout(label); err != nil {
			return result, fmt.Errorf("bootout %q: %w", label, err)
		}
		if err := installer.Remove(label); err != nil {
			return result, fmt.Errorf("remove plist %q: %w", label, err)
		}
		result.Removed = append(result.Removed, label)
	}

	return result, nil
}
