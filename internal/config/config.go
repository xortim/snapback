package config

import (
	"errors"
	"fmt"
	"io/fs"

	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Destination   string        `koanf:"destination" yaml:"destination"`
	Compression   string        `koanf:"compression" yaml:"compression"`
	Retention     Retention     `koanf:"retention" yaml:"retention"`
	VMs           []VM          `koanf:"vms" yaml:"vms"`
	Notifications Notifications `koanf:"notifications" yaml:"notifications"`
}

type Retention struct {
	KeepLast   int `koanf:"keep_last" yaml:"keep_last"`
	KeepDaily  int `koanf:"keep_daily" yaml:"keep_daily"`
	KeepWeekly int `koanf:"keep_weekly" yaml:"keep_weekly"`
}

type VM struct {
	Name            string `koanf:"name" yaml:"name"`
	VMX             string `koanf:"vmx" yaml:"vmx"`
	Schedule        string `koanf:"schedule" yaml:"schedule,omitempty"`
	CommentTemplate string `koanf:"comment_template" yaml:"comment_template,omitempty"`
}

type Notifications struct {
	Enabled bool `koanf:"enabled" yaml:"enabled"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), koanfyaml.Parser()); err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return nil, err
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	expandedDest, err := expandTilde(cfg.Destination)
	if err != nil {
		return nil, fmt.Errorf("parse %s: expand destination: %w", path, err)
	}
	cfg.Destination = expandedDest

	for i, vm := range cfg.VMs {
		expandedVMX, err := expandTilde(vm.VMX)
		if err != nil {
			return nil, fmt.Errorf("parse %s: expand vms[%d].vmx: %w", path, i, err)
		}
		cfg.VMs[i].VMX = expandedVMX
	}

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
}
