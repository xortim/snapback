package launchd

import (
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/config"
)

func TestBuildAgent(t *testing.T) {
	v := config.VM{Name: "My VM!", VMX: "/vms/my-vm.vmx", Schedule: "weekly"}
	agent, err := buildAgent(v, "/usr/local/bin/snapback")
	if err != nil {
		t.Fatalf("buildAgent() error = %v", err)
	}
	if agent.Label != "com.tim.snapback.my-vm" {
		t.Errorf("Label = %q, want %q", agent.Label, "com.tim.snapback.my-vm")
	}
	if agent.VMName != "My VM!" {
		t.Errorf("VMName = %q, want the original, unsanitized name", agent.VMName)
	}
	if agent.BinaryPath != "/usr/local/bin/snapback" {
		t.Errorf("BinaryPath = %q, want %q", agent.BinaryPath, "/usr/local/bin/snapback")
	}
	if !strings.HasSuffix(agent.LogPath, "/Library/Logs/snapback/my-vm.log") {
		t.Errorf("LogPath = %q, want it to end in /Library/Logs/snapback/my-vm.log", agent.LogPath)
	}
	if len(agent.Interval) == 0 {
		t.Error("Interval is empty, want the weekly preset's keys")
	}
}

func TestBuildAgent_UnrecognizedSchedule_Errors(t *testing.T) {
	v := config.VM{Name: "dev", VMX: "/vms/dev.vmx", Schedule: "bogus"}
	if _, err := buildAgent(v, "/usr/local/bin/snapback"); err == nil {
		t.Error("buildAgent() error = nil, want an error for an unrecognized schedule")
	}
}

func TestBuildAgent_EmptySanitizedName_Errors(t *testing.T) {
	v := config.VM{Name: "!!!", VMX: "/vms/a.vmx", Schedule: "daily"}
	if _, err := buildAgent(v, "/usr/local/bin/snapback"); err == nil {
		t.Error("buildAgent() error = nil, want an error for a name that sanitizes to empty")
	}
}

func TestRenderPlist_Daily(t *testing.T) {
	agent := Agent{
		Label:      "com.tim.snapback.dev",
		VMName:     "dev",
		BinaryPath: "/usr/local/bin/snapback",
		LogPath:    "/Users/tim/Library/Logs/snapback/dev.log",
		Interval:   calendarInterval("daily"),
	}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.tim.snapback.dev</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/snapback</string>
		<string>run</string>
		<string>--vm</string>
		<string>dev</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>0</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>/Users/tim/Library/Logs/snapback/dev.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/tim/Library/Logs/snapback/dev.log</string>
</dict>
</plist>
`
	if string(got) != want {
		t.Errorf("renderPlist() =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderPlist_Weekly_NeverContainsDayKey(t *testing.T) {
	agent := Agent{Label: "l", VMName: "v", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("weekly")}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "<key>Day</key>") {
		t.Error("weekly plist contains a Day key -- this is the exact Day+Weekday OR-semantics bug ADR-005 exists to prevent")
	}
	if !strings.Contains(string(got), "<key>Weekday</key>") {
		t.Error("weekly plist is missing its Weekday key")
	}
}

func TestRenderPlist_Monthly_NeverContainsWeekdayKey(t *testing.T) {
	agent := Agent{Label: "l", VMName: "v", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("monthly")}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "<key>Weekday</key>") {
		t.Error("monthly plist contains a Weekday key -- this is the exact Day+Weekday OR-semantics bug ADR-005 exists to prevent")
	}
	if !strings.Contains(string(got), "<key>Day</key>") {
		t.Error("monthly plist is missing its Day key")
	}
}

func TestRenderPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	agent := Agent{
		Label:      "l",
		VMName:     `dev & <test>`,
		BinaryPath: "/bin/snapback",
		LogPath:    "/log",
		Interval:   calendarInterval("daily"),
	}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	if strings.Contains(string(got), "dev & <test>") {
		t.Error("renderPlist() did not escape XML special characters in VMName")
	}
	if !strings.Contains(string(got), "dev &amp; &lt;test&gt;") {
		t.Errorf("renderPlist() = %s, want the VMName XML-escaped", got)
	}
}

func TestRenderPlist_PATHIncludesHomebrewPrefixes(t *testing.T) {
	agent := Agent{Label: "l", VMName: "v", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}
	got, err := renderPlist(agent)
	if err != nil {
		t.Fatalf("renderPlist() error = %v", err)
	}
	// launchd's default agent PATH omits both Homebrew prefixes, and
	// internal/backup/archive.go falls back from zstd to gzip *silently*
	// when exec.LookPath misses -- so a scheduled run would quietly
	// produce a different archive format than a manual one.
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(string(got), dir) {
			t.Errorf("plist PATH is missing %s, so a Homebrew-installed zstd wouldn't resolve in a scheduled run:\n%s", dir, got)
		}
	}
	if !strings.Contains(string(got), "<key>EnvironmentVariables</key>") {
		t.Errorf("plist has no EnvironmentVariables dict:\n%s", got)
	}
}

func TestShortLabel(t *testing.T) {
	if got := ShortLabel("com.tim.snapback.my-vm"); got != "my-vm" {
		t.Errorf("ShortLabel() = %q, want %q", got, "my-vm")
	}
	if got := ShortLabel("something-else"); got != "something-else" {
		t.Errorf("ShortLabel() on an unprefixed label = %q, want it returned unchanged", got)
	}
}

func TestLogPath_UsesSanitizedName(t *testing.T) {
	path, err := LogPath("My VM!")
	if err != nil {
		t.Fatalf("LogPath() error = %v", err)
	}
	if !strings.HasSuffix(path, "/Library/Logs/snapback/my-vm.log") {
		t.Errorf("LogPath() = %q, want it to end in /Library/Logs/snapback/my-vm.log", path)
	}
}
