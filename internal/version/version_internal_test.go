package version

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{Main: debug.Module{Path: "github.com/xortim/snapback", Version: mainVersion}}
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func TestResolve_LdflagsWin(t *testing.T) {
	bi := buildInfo("v9.9.9", map[string]string{"vcs.revision": "ffffffffffffffff", "vcs.time": "2000-01-01T00:00:00Z"})
	got := resolve("v1.0.0-rc.2", "abc1234", "2026-10-31T12:00:00Z", bi, true)
	want := Info{Version: "v1.0.0-rc.2", Commit: "abc1234", Date: "2026-10-31T12:00:00Z"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoLdflags_UsesBuildInfo(t *testing.T) {
	bi := buildInfo("v1.0.0-rc.1.0.20261031120000-0123456789ab", map[string]string{
		"vcs.revision": "0123456789abcdef0123",
		"vcs.time":     "2026-10-31T12:00:00Z",
		"vcs.modified": "false",
	})
	got := resolve("dev", "", "", bi, true)
	want := Info{Version: "v1.0.0-rc.1.0.20261031120000-0123456789ab", Commit: "0123456", Date: "2026-10-31T12:00:00Z"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoLdflags_UsesBuildInfo_Dirty(t *testing.T) {
	bi := buildInfo("(devel)", map[string]string{
		"vcs.revision": "0123456789abcdef0123",
		"vcs.modified": "true",
	})
	got := resolve("dev", "", "", bi, true)
	want := Info{Version: "dev", Commit: "0123456-dirty"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_GoInstall_NoVCS(t *testing.T) {
	got := resolve("dev", "", "", buildInfo("v1.0.0", nil), true)
	want := Info{Version: "v1.0.0"}
	if got != want {
		t.Errorf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_NoBuildInfo(t *testing.T) {
	got := resolve("dev", "", "", nil, false)
	if got != (Info{Version: "dev"}) {
		t.Errorf("resolve() = %+v, want just Version=dev", got)
	}
}

func TestResolve_ShortRevisionNotTruncatedPastLength(t *testing.T) {
	got := resolve("dev", "", "", buildInfo("", map[string]string{"vcs.revision": "abc"}), true)
	if got.Commit != "abc" {
		t.Errorf("Commit = %q, want %q", got.Commit, "abc")
	}
}

func TestInfoString(t *testing.T) {
	tests := []struct {
		name string
		in   Info
		want string
	}{
		{"all", Info{"v1.0.0-rc.2", "abc1234", "2026-10-31T12:00:00Z"}, "v1.0.0-rc.2 (commit abc1234, built 2026-10-31T12:00:00Z)"},
		{"commit only", Info{Version: "v1.0.0", Commit: "abc1234"}, "v1.0.0 (commit abc1234)"},
		{"date only", Info{Version: "v1.0.0", Date: "2026-10-31T12:00:00Z"}, "v1.0.0 (built 2026-10-31T12:00:00Z)"},
		{"version only", Info{Version: "v1.0.0"}, "v1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
