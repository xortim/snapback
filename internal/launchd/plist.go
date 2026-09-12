package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/xortim/snapback/internal/config"
)

// labelPrefix is prepended to every sanitized VM name to form a launchd
// label, following reverse-DNS convention (matches the existing
// com.tim.snapback.plist name docs/design.md already documents for the
// pre-this-ADR single global plist).
const labelPrefix = "com.tim.snapback."

// Agent describes one VM's scheduled backup job -- everything
// renderPlist needs to produce that VM's LaunchAgent plist.
type Agent struct {
	Label      string
	VMName     string // original, unsanitized config.VM.Name
	BinaryPath string
	LogPath    string
	Interval   []calendarKey
}

// buildAgent resolves v's plist inputs. binaryPath is the running
// snapback binary's path (os.Executable(), resolved by the caller) --
// see ADR-005's Risks for the known gap if the binary later moves
// without a resync.
func buildAgent(v config.VM, binaryPath string) (Agent, error) {
	sanitized := sanitizeLabel(v.Name)
	if sanitized == "" {
		return Agent{}, fmt.Errorf("VM %q sanitizes to an empty launchd label; rename it", v.Name)
	}
	interval := calendarInterval(v.Schedule)
	if interval == nil {
		return Agent{}, fmt.Errorf("VM %q has no recognized schedule (got %q)", v.Name, v.Schedule)
	}
	logPath, err := LogPath(v.Name)
	if err != nil {
		return Agent{}, err
	}
	return Agent{
		Label:      labelPrefix + sanitized,
		VMName:     v.Name,
		BinaryPath: binaryPath,
		LogPath:    logPath,
		Interval:   interval,
	}, nil
}

// LogPath returns the deterministic path a scheduled run's stdout/stderr
// gets redirected to via the generated plist's StandardOutPath/
// StandardErrorPath. internal/cli/run.go's own log rotation (Task 11)
// computes this same path independently rather than threading it
// through config, so both sides always agree on one location.
func LogPath(vmName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", "snapback", sanitizeLabel(vmName)+".log"), nil
}

func xmlEscapeString(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{
	"esc": xmlEscapeString,
}).Parse(plistTmplSrc))

const plistTmplSrc = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{esc .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{esc .BinaryPath}}</string>
		<string>run</string>
		<string>--vm</string>
		<string>{{esc .VMName}}</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
{{- range .Interval}}
		<key>{{.Name}}</key>
		<integer>{{.Value}}</integer>
{{- end}}
	</dict>
	<key>StandardOutPath</key>
	<string>{{esc .LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{esc .LogPath}}</string>
</dict>
</plist>
`

// renderPlist renders agent as a complete LaunchAgent plist document.
func renderPlist(agent Agent) ([]byte, error) {
	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, agent); err != nil {
		return nil, fmt.Errorf("render plist for %q: %w", agent.VMName, err)
	}
	return buf.Bytes(), nil
}
