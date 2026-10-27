// Package notify sends macOS user notifications. Its only real
// implementation shells out to osascript -- see
// docs/superpowers/specs/2026-10-26-run-notifications-design.md for why
// (UserNotifications needs a signed app bundle; a bare CLI binary isn't
// one) and why every caller goes through the Notifier interface so a
// native helper-app implementation can replace it later.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// OSAScriptPath is osascript's absolute path. launchd agents don't
// inherit the interactive shell's $PATH, so never rely on a lookup.
const OSAScriptPath = "/usr/bin/osascript"

// NotifyTimeout bounds a single osascript invocation, on top of
// whatever deadline the caller's context already carries.
const NotifyTimeout = 5 * time.Second

// Notification is one user-visible macOS notification.
type Notification struct {
	Title    string
	Subtitle string
	Message  string
	Sound    string // macOS sound name, e.g. "Basso"; "" = no sound
}

// Notifier sends a Notification.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// The two fixed scripts OSAScript chooses between. Every user-supplied
// string is read from argv -- never spliced into script source -- so a
// VM name like `x" & do shell script "..."` can't inject AppleScript.
var (
	scriptNoSound = []string{
		"on run argv",
		"display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)",
		"end run",
	}
	scriptWithSound = []string{
		"on run argv",
		"display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv) sound name (item 4 of argv)",
		"end run",
	}
)

// OSAScript is the real Notifier, backed by osascript's
// `display notification`. Run is the process runner; NewOSAScript sets
// it to an exec.CommandContext-backed one, and tests replace it to
// assert exact argv without running osascript.
type OSAScript struct {
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewOSAScript returns an OSAScript that really runs osascript.
func NewOSAScript() *OSAScript {
	return &OSAScript{Run: runCombined}
}

func runCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Notify implements Notifier.
func (o *OSAScript) Notify(ctx context.Context, n Notification) error {
	ctx, cancel := context.WithTimeout(ctx, NotifyTimeout)
	defer cancel()

	script := scriptNoSound
	argv := []string{n.Title, n.Subtitle, n.Message}
	if n.Sound != "" {
		script = scriptWithSound
		argv = append(argv, n.Sound)
	}

	args := make([]string, 0, 2*len(script)+len(argv))
	for _, line := range script {
		args = append(args, "-e", line)
	}
	args = append(args, argv...)

	out, err := o.Run(ctx, OSAScriptPath, args...)
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
