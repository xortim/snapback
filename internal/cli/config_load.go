package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

// loadConfigForCmd reads the --config flag from cmd and calls loadConfig
// with it, wrapping any load error with enough context to identify that
// it came from config loading. Shared by every subcommand that needs the
// user's config.yaml (run, list, and eventually status/init), so a
// wording change (e.g. the friendlier missing-file message tracked in
// #32) only needs to be made once.
func loadConfigForCmd(cmd *cobra.Command, loadConfig func(path string) (*config.Config, error)) (cfg *config.Config, configPath string, err error) {
	configPath, err = cmd.Flags().GetString("config")
	if err != nil {
		return nil, "", err
	}

	cfg, err = loadConfig(configPath)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return cfg, configPath, nil
}
