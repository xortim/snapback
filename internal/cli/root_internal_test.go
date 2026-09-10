package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigPathFor_HomeDirResolves(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)

	var warn bytes.Buffer
	path := defaultConfigPathFor(&warn)

	want := filepath.Join(home, ".config", "snapback", "config.yaml")
	if path != want {
		t.Errorf("defaultConfigPathFor() = %q, want %q", path, want)
	}
	if warn.Len() != 0 {
		t.Errorf("defaultConfigPathFor() wrote a warning = %q, want none when HOME resolves", warn.String())
	}
}

func TestDefaultConfigPathFor_HomeDirUnresolved_WarnsAndFallsBack(t *testing.T) {
	t.Setenv("HOME", "")

	var warn bytes.Buffer
	path := defaultConfigPathFor(&warn)

	if path != "config.yaml" {
		t.Errorf("defaultConfigPathFor() = %q, want fallback %q", path, "config.yaml")
	}
	if !strings.Contains(warn.String(), "warning") {
		t.Errorf("defaultConfigPathFor() warning = %q, want it to mention the fallback", warn.String())
	}
}

// TestNewRootCmd_ConfigFlagDefaultIsEmpty guards against reintroducing
// eager resolution: NewRootCmd must not call defaultConfigPath() while
// registering the flag, since that fires the HOME-unresolved warning on
// every invocation (--help, completion, etc.) even when --config is
// always passed explicitly and the default is never consumed. See #28.
func TestNewRootCmd_ConfigFlagDefaultIsEmpty(t *testing.T) {
	root := NewRootCmd()
	f := root.PersistentFlags().Lookup("config")
	if f == nil {
		t.Fatal("root has no \"config\" persistent flag")
	}
	if f.DefValue != "" {
		t.Errorf("config flag DefValue = %q, want empty (default path resolved lazily, not at registration)", f.DefValue)
	}
}
