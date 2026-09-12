// Package launchd generates and installs/removes the per-VM launchd
// LaunchAgents that drive scheduled backups. See ADR-005
// (docs/superpowers/specs/2026-09-11-launchd-scheduling-design.md).
package launchd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xortim/snapback/internal/config"
)

// sanitizeLabel converts a VM name into a value safe to embed in a
// launchd label (com.tim.snapback.<label>) and a filesystem path
// segment: lowercase, every run of characters that isn't a-z or 0-9
// collapsed to a single "-", with no leading or trailing "-". An
// all-punctuation name sanitizes to "" -- callers must reject that (see
// DetectCollisions) rather than generate a malformed label.
func sanitizeLabel(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		default:
			pendingDash = true
		}
	}
	return b.String()
}

// DetectCollisions reports an error naming every VM name that either
// sanitizes to "" (all-punctuation, e.g. "!!!") or collides with another
// VM's sanitized name (e.g. "My VM!" and "My VM?" both -> "my-vm") --
// either case would otherwise make two VMs silently share one plist
// file/label, or produce an unusable one. Checked across every VM in
// vms regardless of Schedule, since adding a schedule to a
// currently-unscheduled VM later would surface the same collision then
// instead of now.
func DetectCollisions(vms []config.VM) error {
	bySanitized := make(map[string][]string)
	for _, v := range vms {
		s := sanitizeLabel(v.Name)
		bySanitized[s] = append(bySanitized[s], v.Name)
	}

	var errs []error
	for s, names := range bySanitized {
		switch {
		case s == "":
			errs = append(errs, fmt.Errorf("VM name(s) %v sanitize to an empty launchd label; rename them", names))
		case len(names) > 1:
			errs = append(errs, fmt.Errorf("VM names %v all sanitize to the same launchd label %q; rename one", names, s))
		}
	}
	return errors.Join(errs...)
}
