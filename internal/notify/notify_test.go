package notify_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xortim/snapback/internal/notify"
)

type recordedCall struct {
	name     string
	args     []string
	deadline time.Time
	hasDL    bool
}

func recordingOSAScript(out []byte, err error) (*notify.OSAScript, *[]recordedCall) {
	var calls []recordedCall
	n := &notify.OSAScript{
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			dl, ok := ctx.Deadline()
			calls = append(calls, recordedCall{name: name, args: args, deadline: dl, hasDL: ok})
			return out, err
		},
	}
	return n, &calls
}

// scriptLines returns every argument that follows a "-e" flag.
func scriptLines(args []string) []string {
	var lines []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" {
			lines = append(lines, args[i+1])
		}
	}
	return lines
}

// trailingArgs returns everything after the last "-e <line>" pair.
func trailingArgs(args []string) []string {
	last := -1
	for i, a := range args {
		if a == "-e" {
			last = i
		}
	}
	return args[last+2:]
}

func TestOSAScript_NoSound_PassesStringsAsArgv(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	err := n.Notify(context.Background(), notify.Notification{Title: "snapback", Subtitle: "myvm backed up", Message: "myvm-20261026"})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("Run called %d times, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.name != notify.OSAScriptPath {
		t.Errorf("name = %q, want %q", c.name, notify.OSAScriptPath)
	}
	if got, want := trailingArgs(c.args), []string{"snapback", "myvm backed up", "myvm-20261026"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %q, want %q", got, want)
	}
	script := strings.Join(scriptLines(c.args), "\n")
	if strings.Contains(script, "sound name") {
		t.Errorf("script = %q, want no sound clause when Sound is empty", script)
	}
	if !strings.Contains(script, "on run argv") {
		t.Errorf("script = %q, want an argv-reading run handler", script)
	}
}

func TestOSAScript_WithSound_AppendsSoundArgAndClause(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	if err := n.Notify(context.Background(), notify.Notification{Title: "t", Subtitle: "s", Message: "m", Sound: "Basso"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	if got, want := trailingArgs(c.args), []string{"t", "s", "m", "Basso"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %q, want %q", got, want)
	}
	if script := strings.Join(scriptLines(c.args), "\n"); !strings.Contains(script, "sound name (item 4 of argv)") {
		t.Errorf("script = %q, want a sound clause reading item 4 of argv", script)
	}
}

func TestOSAScript_HostileStrings_NeverReachScriptSource(t *testing.T) {
	hostile := "x\" & (do shell script \"touch /tmp/pwned\") & \"\\\nline2"
	n, calls := recordingOSAScript(nil, nil)
	if err := n.Notify(context.Background(), notify.Notification{Title: "snapback", Subtitle: hostile, Message: hostile, Sound: "Basso"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	for _, line := range scriptLines(c.args) {
		if strings.Contains(line, "pwned") || strings.Contains(line, hostile) {
			t.Fatalf("script line %q contains user data; strings must only travel as argv", line)
		}
	}
	trail := trailingArgs(c.args)
	if trail[1] != hostile || trail[2] != hostile {
		t.Errorf("trailing args = %q, want the hostile string passed through verbatim as argv", trail)
	}
}

func TestOSAScript_AppliesTimeout(t *testing.T) {
	n, calls := recordingOSAScript(nil, nil)
	before := time.Now()
	if err := n.Notify(context.Background(), notify.Notification{Title: "t"}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	c := (*calls)[0]
	if !c.hasDL {
		t.Fatal("ctx passed to Run has no deadline, want NotifyTimeout applied")
	}
	if d := c.deadline.Sub(before); d > notify.NotifyTimeout+time.Second || d <= 0 {
		t.Errorf("deadline is %v after start, want about %v", d, notify.NotifyTimeout)
	}
}

func TestOSAScript_RunError_IsWrappedWithOutput(t *testing.T) {
	boom := errors.New("exit status 1")
	n, _ := recordingOSAScript([]byte("execution error: nope"), boom)
	err := n.Notify(context.Background(), notify.Notification{Title: "t"})
	if !errors.Is(err, boom) {
		t.Fatalf("Notify() error = %v, want it to wrap the Run error", err)
	}
	if !strings.Contains(err.Error(), "execution error: nope") {
		t.Errorf("Notify() error = %q, want osascript's output included", err.Error())
	}
}

func TestNewOSAScript_HasRealRunner(t *testing.T) {
	if notify.NewOSAScript().Run == nil {
		t.Fatal("NewOSAScript().Run = nil, want a real exec-backed runner")
	}
}
