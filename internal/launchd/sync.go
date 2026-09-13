package launchd

import (
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
	// Skipped lists VM names whose LaunchAgent needed an update (its
	// plist content changed) but were left alone because a backup was
	// in progress for that VM at the time -- see RunningChecker. Neither
	// the on-disk plist nor the loaded job were touched, so the next
	// Sync call will detect the same diff and retry.
	Skipped []string
}

// IsEmpty reports whether Sync found nothing to do.
func (r SyncResult) IsEmpty() bool {
	return len(r.Installed) == 0 && len(r.Updated) == 0 && len(r.Removed) == 0 && len(r.Skipped) == 0
}

// RunningChecker reports whether vmName currently has a backup in
// progress (see backup.IsRunning). Sync consults this before tearing
// down an *existing* LaunchAgent whose content changed -- Bootout
// unloads the running job, which would otherwise kill a scheduled
// backup mid-choreography (see CLAUDE.md's "Known gotchas" for the
// orphaned-snapshot incident this guards against). This is only
// consulted on the update path (an existing plist whose content
// changed), never on a fresh install -- but "install" here just means
// "no plist on disk" (see the doc comment on the installer.Bootout
// call below): a job can still be loaded in launchd's session with its
// plist hand-deleted, in which case the install path skips this check
// and boots out unconditionally.
//
// This check has a TOCTOU gap: it probes the lock and releases it
// immediately (see backup.IsRunning), so a launchd-started run that
// begins in the narrow window between the probe and Sync's subsequent
// Bootout call can still be killed. That's strictly better than before
// this check existed (which always killed an in-flight run on the
// update path), but it is not a complete guarantee.
//
// Scope limit: this guard only covers the update path above. The
// removal loop below (VMs no longer scheduled, or dropped from config
// entirely) still boots out unconditionally with no running-check at
// all -- a known, deliberate gap tracked as
// https://github.com/xortim/snapback/issues/96. It wasn't fixed here
// because the removal loop only has the VM's *sanitized* launchd label
// for a VM that's been fully removed from config, and checking against
// the sanitized name instead of the real config.VM.Name would silently
// never detect a real running state for names that needed sanitizing.
type RunningChecker func(vmName string) (bool, error)

// Sync reconciles installer's on-disk/loaded state with vms: every VM
// with a non-empty Schedule gets its plist written and (re-)bootstrapped
// if it's new or its content changed since the last sync -- always
// booted out first, so a stale or hand-orphaned loaded job can't make
// Bootstrap fail. Every plist installer already knows about that no
// longer corresponds to a scheduled VM in vms gets booted out and
// removed. Safe to call
// repeatedly -- an already-in-sync config produces an empty SyncResult
// and no Installer calls beyond the one List().
//
// binaryPath is embedded into each plist's ProgramArguments as the
// snapback binary to invoke (os.Executable(), resolved by the caller) --
// see ADR-005's Risks for the known gap if the binary is later moved
// without a resync.
//
// If reconciliation fails partway through (an Installer call returns an
// error after the collision check and Agent-building pass have already
// succeeded), the returned SyncResult still reflects everything
// completed before the failure -- it is not zeroed out -- so callers can
// report partial progress to the user alongside the error.
func Sync(installer Installer, vms []config.VM, binaryPath string, isRunning RunningChecker) (SyncResult, error) {
	if err := DetectCollisions(vms); err != nil {
		return SyncResult{}, err
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
		if !install {
			// Checked before Write (not after): Write persists the new
			// plist content unconditionally, and that content is what
			// "changed" below is diffed against next time. Checking
			// first means a skip leaves the on-disk plist untouched, so
			// the next Sync call still sees the real diff and retries --
			// checking after Write would make the retry never fire,
			// since the second call would find changed == false.
			running, err := isRunning(agent.VMName)
			if err != nil {
				return result, fmt.Errorf("check running state for %q: %w", agent.VMName, err)
			}
			if running {
				result.Skipped = append(result.Skipped, agent.VMName)
				continue
			}
		}

		plistPath, changed, err := installer.Write(agent)
		if err != nil {
			return result, fmt.Errorf("write plist for %q: %w", agent.VMName, err)
		}
		if !install && !changed {
			continue
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
