package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xortim/snapback/internal/cli"
)

func TestNewRootCmd_HasExpectedSubcommands(t *testing.T) {
	root := cli.NewRootCmd()

	want := []string{"init", "run", "list", "status", "cleanup", "vm"}
	for _, name := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Errorf("root command has no %q subcommand: %v", name, err)
			continue
		}
		if cmd.Name() != name {
			t.Errorf("Find(%q) resolved to command %q", name, cmd.Name())
		}
	}
}

func TestNewRootCmd_Name(t *testing.T) {
	root := cli.NewRootCmd()
	if root.Use != "snapback" {
		t.Errorf("root.Use = %q, want %q", root.Use, "snapback")
	}
}

func TestNewRootCmd_VersionFlag(t *testing.T) {
	root := cli.NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "snapback ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("--version output = %q, want \"snapback <version>\\n\"", got)
	}
	if strings.TrimSpace(strings.TrimPrefix(got, "snapback ")) == "" {
		t.Errorf("--version output = %q, want a non-empty version", got)
	}
}
