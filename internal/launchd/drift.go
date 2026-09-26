package launchd

import (
	"fmt"

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
// Write/Bootstrap/Bootout/Remove never are. DetectCollisions is
// deliberately not run here -- a colliding config is Sync's (and
// persistConfigAndSync's) problem to reject before it's ever written,
// not status's to re-diagnose.
func CheckDrift(installer Installer, vms []config.VM, binaryPath string) (DriftReport, error) {
	var report DriftReport

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

		state, err := classifyAgent(installer, agent)
		if err != nil {
			return DriftReport{}, err
		}
		switch state {
		case agentMissing:
			report.NotInstalled = append(report.NotInstalled, v.Name)
		case agentDiffers:
			report.OutOfSync = append(report.OutOfSync, v.Name)
		case agentNotLoaded:
			report.NotLoaded = append(report.NotLoaded, v.Name)
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
