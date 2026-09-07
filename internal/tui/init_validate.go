package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xortim/snapback/internal/config"
)

// validateWritableDestination is the huh Validate hook for the
// destination Input field. It expands a leading "~" (via
// config.ExpandTilde, the same expansion config.Load applies to an
// already-written config.yaml) and checks that the nearest existing
// ancestor directory is writable -- it deliberately does not create path
// itself (via os.MkdirAll): init is only proposing a destination here,
// not committing to it, and the review screen (reviewAndConfirm) is
// where the user actually confirms the config before anything is
// written. internal/cli/init.go's own writeConfigFile is the thing that
// actually creates directories, once the user has confirmed everything.
func validateWritableDestination(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("destination must not be empty")
	}
	expanded, err := config.ExpandTilde(trimmed)
	if err != nil {
		return err
	}

	dir := expanded
	for {
		info, statErr := os.Stat(dir)
		if statErr == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			break
		}
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("check %s: %w", dir, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no existing ancestor directory found for %s", expanded)
		}
		dir = parent
	}

	probe, err := os.CreateTemp(dir, ".snapback-writetest-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// validateNonNegativeInt is the Validate hook for the three retention
// Input fields (keep_last/keep_daily/keep_weekly). huh's Input only ever
// binds a string (Value(*string)), so parsing to int happens after the
// form completes in promptCoreSettings; this only rejects what
// strconv.Atoi or a negative value would otherwise let through silently.
func validateNonNegativeInt(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("%q is not a whole number", s)
	}
	if n < 0 {
		return fmt.Errorf("%q must not be negative", s)
	}
	return nil
}

// validateCronExpression is a lightweight sanity check -- exactly 5
// space-separated fields -- not a full cron grammar validator. No
// cron-parsing dependency exists in this module (nothing parses or
// executes config.VM.Schedule yet; see CLAUDE.md's "Other components"
// table), so this only catches the most common typo (wrong field count)
// rather than validating minute/hour/day ranges.
func validateCronExpression(s string) error {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return fmt.Errorf("cron expression must have 5 space-separated fields (minute hour day month weekday), got %d", len(fields))
	}
	return nil
}

// acceptBlankInAccessibleMode wraps validate so a blank/whitespace-only
// answer passes immediately when accessible is true, deferring to
// whatever default huh's accessible-mode PromptString substitutes
// afterward (cmp.Or(strings.TrimSpace(input), defaultValue), in
// huh@v1.0.0/internal/accessibility/accessibility.go). This exists
// because, verified against that same source, PromptString calls a
// field's Validate closure on the *raw scanned line* -- before that
// default substitution happens -- unlike Select/Confirm's own internal
// accessible-mode validators, which special-case a blank line
// themselves. Without this wrapper, every "type nothing to accept the
// default" convention this wizard relies on (in both its own tests and
// real piped/non-tty invocations) would instead fail validation and
// force a reprompt.
//
// Only applied when accessible is true: in the real interactive terminal
// path there is no equivalent default-substitution step for a genuinely
// emptied Input field (the field starts pre-filled with the default
// text, so blank only happens if a user deliberately clears it), so
// blank must still fail validation there exactly as it did before this
// wrapper existed.
func acceptBlankInAccessibleMode(accessible bool, validate func(string) error) func(string) error {
	return func(s string) error {
		if accessible && strings.TrimSpace(s) == "" {
			return nil
		}
		return validate(s)
	}
}
