package config

import (
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Destination   string        `koanf:"destination"`
	Compression   string        `koanf:"compression"`
	Retention     Retention     `koanf:"retention"`
	VMs           []VM          `koanf:"vms"`
	Notifications Notifications `koanf:"notifications"`
}

type Retention struct {
	KeepLast   int `koanf:"keep_last"`
	KeepDaily  int `koanf:"keep_daily"`
	KeepWeekly int `koanf:"keep_weekly"`
}

type VM struct {
	Name            string `koanf:"name"`
	VMX             string `koanf:"vmx"`
	Schedule        string `koanf:"schedule"`
	CommentTemplate string `koanf:"comment_template"`
}

type Notifications struct {
	Enabled bool `koanf:"enabled"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, err
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
