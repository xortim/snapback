// Package cli (internal test package) so this file can call
// loadConfigForCmd directly.
package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

func TestLoadConfigForCmd_WrapsLoaderError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "unused.yaml", "")

	_, _, err := loadConfigForCmd(cmd, func(string) (*config.Config, error) { return nil, errBoom })
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("loadConfigForCmd() error = %v, want it to wrap errBoom", err)
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("loadConfigForCmd() error = %v, want \"load config\" context", err)
	}
}

func TestLoadConfigForCmd_ReturnsConfigAndPath(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", "/some/path.yaml", "")

	want := &config.Config{Destination: "/dest"}
	cfg, path, err := loadConfigForCmd(cmd, func(p string) (*config.Config, error) {
		if p != "/some/path.yaml" {
			t.Errorf("loadConfig called with %q, want %q", p, "/some/path.yaml")
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("loadConfigForCmd() error = %v, want nil", err)
	}
	if cfg != want {
		t.Errorf("loadConfigForCmd() cfg = %v, want %v", cfg, want)
	}
	if path != "/some/path.yaml" {
		t.Errorf("loadConfigForCmd() path = %q, want %q", path, "/some/path.yaml")
	}
}
