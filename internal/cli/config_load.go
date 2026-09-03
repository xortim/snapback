package cli

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/config"
)

// configPathForCmd reads the --config persistent flag. Shared by every
// subcommand that needs the config path, whether or not the config file
// exists yet (init doesn't require it to; run/list/status do).
func configPathForCmd(cmd *cobra.Command) (string, error) {
	return cmd.Flags().GetString("config")
}

// loadConfigForCmd reads the --config flag from cmd and calls loadConfig
// with it, wrapping any load error with enough context to identify that
// it came from config loading. Shared by every subcommand that needs the
// user's config.yaml (run, list, status, cleanup), so a wording change
// only needs to be made once. A missing config file gets a dedicated
// message pointing at `snapback init` instead of the raw os.PathError --
// on a fresh machine with no config.yaml yet, that raw error is the very
// first thing a new user sees and gives no hint of how to fix it.
func loadConfigForCmd(cmd *cobra.Command, loadConfig func(path string) (*config.Config, error)) (cfg *config.Config, configPath string, err error) {
	configPath, err = configPathForCmd(cmd)
	if err != nil {
		return nil, "", err
	}

	cfg, err = loadConfig(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("no config file at %s -- run `snapback init` to create one", configPath)
		}
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return cfg, configPath, nil
}
