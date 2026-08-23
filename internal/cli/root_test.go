package cli_test

import (
	"testing"

	"github.com/xortim/snapback/internal/cli"
)

func TestNewRootCmd_HasExpectedSubcommands(t *testing.T) {
	root := cli.NewRootCmd()

	want := []string{"init", "run", "list", "status"}
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
