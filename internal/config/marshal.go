package config

import "go.yaml.in/yaml/v3"

// Marshal renders cfg as YAML in the shape Load parses, matching the
// config.yaml documented in docs/design.md#config-reference. Used by
// `snapback init` to write a freshly-built Config to disk.
func Marshal(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
