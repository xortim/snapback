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
