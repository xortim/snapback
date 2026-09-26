package launchd

import (
	"bytes"
	"fmt"
)

// agentState is classifyAgent's verdict for one desired VM's plist,
// comparing installer's actual on-disk/loaded state against what
// buildAgent would currently render for it. See ADR-006
// (docs/superpowers/specs/2026-09-26-schedule-drift-detection-design.md).
type agentState int

const (
	// agentInSync means the on-disk plist content matches and the label
	// is loaded -- nothing to do.
	agentInSync agentState = iota
	// agentMissing means no plist file exists for this label at all.
	agentMissing
	// agentDiffers means a plist file exists but its content doesn't
	// match what buildAgent would currently render (e.g. config.yaml's
	// schedule was hand-edited since the last sync).
	agentDiffers
	// agentNotLoaded means the on-disk content matches, but the label
	// isn't currently bootstrapped into launchd (e.g. after a manual
	// `launchctl bootout`, or an interrupted prior sync).
	agentNotLoaded
)

// classifyAgent reports how agent's actual state (on disk, and loaded)
// compares to what buildAgent would currently render for it. It's the
// one place both Sync and CheckDrift decide "does this VM's launchd
// state already match config.yaml" -- shared so the two can't disagree.
// IsLoaded is only consulted when content already matches: a plist that
// differs is drift regardless of its load state, so there's no need to
// also probe IsLoaded for it.
func classifyAgent(installer Installer, agent Agent) (agentState, error) {
	existingData, ok, err := installer.Read(agent.Label)
	if err != nil {
		return agentInSync, fmt.Errorf("read existing plist for %q: %w", agent.VMName, err)
	}
	if !ok {
		return agentMissing, nil
	}

	candidate, err := renderPlist(agent)
	if err != nil {
		return agentInSync, fmt.Errorf("render plist for %q: %w", agent.VMName, err)
	}
	if !bytes.Equal(existingData, candidate) {
		return agentDiffers, nil
	}

	loaded, err := installer.IsLoaded(agent.Label)
	if err != nil {
		return agentInSync, fmt.Errorf("check loaded state for %q: %w", agent.VMName, err)
	}
	if !loaded {
		return agentNotLoaded, nil
	}
	return agentInSync, nil
}
