package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks that cfg's fields are internally consistent -- valid
// enum values, no negative retention counts, every VM has the fields
// run/list/status need to identify and back it up. Load calls this on
// every parsed config so a malformed config.yaml fails fast at load
// time with every problem listed at once, instead of surfacing as a
// confusing failure deep in the backup choreography.
func Validate(cfg *Config) error {
	var errs []error

	if strings.TrimSpace(cfg.Destination) == "" {
		errs = append(errs, errors.New("destination must not be empty"))
	}

	switch cfg.Compression {
	case "zstd", "gzip":
	default:
		errs = append(errs, fmt.Errorf("compression must be \"zstd\" or \"gzip\", got %q", cfg.Compression))
	}

	if cfg.Retention.KeepLast < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_last must not be negative, got %d", cfg.Retention.KeepLast))
	}
	if cfg.Retention.KeepDaily < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_daily must not be negative, got %d", cfg.Retention.KeepDaily))
	}
	if cfg.Retention.KeepWeekly < 0 {
		errs = append(errs, fmt.Errorf("retention.keep_weekly must not be negative, got %d", cfg.Retention.KeepWeekly))
	}

	seen := make(map[string]bool, len(cfg.VMs))
	for i, vm := range cfg.VMs {
		if strings.TrimSpace(vm.Name) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: name must not be empty", i))
		} else if seen[vm.Name] {
			errs = append(errs, fmt.Errorf("vms[%d]: duplicate VM name %q", i, vm.Name))
		} else {
			seen[vm.Name] = true
		}
		if strings.TrimSpace(vm.VMX) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: vmx must not be empty", i))
		}
	}

	return errors.Join(errs...)
}
