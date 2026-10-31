// Package version reports which build of snapback is running. Version,
// Commit, and Date are set at link time via -ldflags "-X ..." (see the
// Makefile and .goreleaser.yaml); when they aren't (go install ...@vX,
// plain go build), Get falls back to the module and VCS info the Go
// toolchain embeds on its own.
package version

import (
	"runtime/debug"
	"strings"
)

const devVersion = "dev"

// Set via -ldflags "-X github.com/xortim/snapback/internal/version.<Name>=...".
var (
	Version = devVersion
	Commit  = ""
	Date    = ""
)

// Info is the resolved build identity.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Get resolves the running binary's build identity.
func Get() Info {
	bi, ok := debug.ReadBuildInfo()
	return resolve(Version, Commit, Date, bi, ok)
}

// resolve fills any field ldflags left unset from the toolchain-embedded
// build info. Kept pure so tests can drive it with a synthetic BuildInfo.
func resolve(version, commit, date string, bi *debug.BuildInfo, ok bool) Info {
	info := Info{Version: version, Commit: commit, Date: date}
	if info.Version == "" {
		info.Version = devVersion
	}
	if !ok || bi == nil {
		return info
	}

	if info.Version == devVersion && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}

	var revision, vcsTime string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if info.Commit == "" && revision != "" {
		info.Commit = revision[:min(7, len(revision))]
		if modified {
			info.Commit += "-dirty"
		}
	}
	if info.Date == "" {
		info.Date = vcsTime
	}
	return info
}

// String renders e.g. "v1.0.0-rc.2 (commit abc1234, built 2026-10-31T12:00:00Z)",
// omitting whichever of commit/date is unknown.
func (i Info) String() string {
	var parts []string
	if i.Commit != "" {
		parts = append(parts, "commit "+i.Commit)
	}
	if i.Date != "" {
		parts = append(parts, "built "+i.Date)
	}
	if len(parts) == 0 {
		return i.Version
	}
	return i.Version + " (" + strings.Join(parts, ", ") + ")"
}
