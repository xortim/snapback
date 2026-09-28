package launchd

import (
	"fmt"
	"sync"

	"github.com/xortim/snapback/internal/config"
)

// DriftReport categorizes every way a VM's actual launchd state can
// disagree with config.yaml -- see CheckDrift. VM names throughout,
// except Stale, which holds raw labels: a stale label with no matching
// desired VM has no recoverable config.VM.Name to report instead (the
// same constraint SyncResult.Removed already has -- see Sync's
// nameByLabel doc comment).
type DriftReport struct {
	NotInstalled []string // schedule set, no plist on disk yet
	OutOfSync    []string // on-disk plist content differs from config
	NotLoaded    []string // on-disk content matches, but isn't bootstrapped
	Stale        []string // on-disk label with no corresponding desired VM
}

// IsEmpty reports whether CheckDrift found nothing to report.
func (r DriftReport) IsEmpty() bool {
	return len(r.NotInstalled) == 0 && len(r.OutOfSync) == 0 && len(r.NotLoaded) == 0 && len(r.Stale) == 0
}

// CheckDrift reports how installer's on-disk/loaded state disagrees
// with vms, without changing anything -- the read-only counterpart to
// Sync, built for `snapback status` to warn from (see ADR-006,
// docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
// Only List, Read, and IsLoaded are ever called;
// Write/Bootstrap/Bootout/Remove never are. DetectCollisions is checked
// first, the same as Sync -- otherwise two VM names that sanitize to the
// same label would each be classified independently against one shared
// on-disk plist, silently hiding the collision behind a misleading
// per-VM drift report instead of surfacing it the way Sync would.
func CheckDrift(installer Installer, vms []config.VM, binaryPath string) (DriftReport, error) {
	if err := DetectCollisions(vms); err != nil {
		return DriftReport{}, err
	}

	var scheduled []Agent
	desiredLabels := make(map[string]bool)
	for _, v := range vms {
		if v.Schedule == "" {
			continue
		}
		agent, err := buildAgent(v, binaryPath)
		if err != nil {
			return DriftReport{}, err
		}
		desiredLabels[agent.Label] = true
		scheduled = append(scheduled, agent)
	}

	// classifyAgent is subprocess-backed (IsLoaded shells out to
	// launchctl) with no per-call timeout, so classifying every scheduled
	// VM one at a time would turn a dozen-VM config into a serial chain
	// of shell-outs -- the same reasoning internal/cli's
	// warnDamagedDiskChains already applies to its own per-VM checks.
	// Each goroutine writes only to its own index of states/errs, so no
	// further synchronization is needed here.
	states := make([]agentState, len(scheduled))
	errs := make([]error, len(scheduled))
	var wg sync.WaitGroup
	for i, agent := range scheduled {
		wg.Add(1)
		go func(i int, agent Agent) {
			defer wg.Done()
			states[i], errs[i] = classifyAgent(installer, agent)
		}(i, agent)
	}
	wg.Wait()

	var report DriftReport
	for i, agent := range scheduled {
		if errs[i] != nil {
			return DriftReport{}, errs[i]
		}
		switch states[i] {
		case agentMissing:
			report.NotInstalled = append(report.NotInstalled, agent.VMName)
		case agentDiffers:
			report.OutOfSync = append(report.OutOfSync, agent.VMName)
		case agentNotLoaded:
			report.NotLoaded = append(report.NotLoaded, agent.VMName)
		}
	}

	existingLabels, err := installer.List()
	if err != nil {
		return DriftReport{}, fmt.Errorf("list installed schedules: %w", err)
	}
	for _, label := range existingLabels {
		if !desiredLabels[label] {
			report.Stale = append(report.Stale, label)
		}
	}

	return report, nil
}
