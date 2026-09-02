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

	errs = append(errs, validateVMs(cfg.VMs)...)

	return errors.Join(errs...)
}

// ValidateVMs checks vms for the same per-VM problems Validate checks as
// part of a full config: a non-empty name, a non-empty vmx path, and no
// two VMs sharing a name once whitespace is trimmed. Exported separately
// from Validate so a caller that builds a VM list incrementally --
// `init`'s VM-selection prompt, in particular -- can fail fast on a bad
// selection before collecting the rest of the config.
func ValidateVMs(vms []VM) error {
	return errors.Join(validateVMs(vms)...)
}

func validateVMs(vms []VM) []error {
	var errs []error
	seen := make(map[string]bool, len(vms))
	for i, vm := range vms {
		name := strings.TrimSpace(vm.Name)
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("vms[%d]: name must not be empty", i))
		case seen[name]:
			errs = append(errs, fmt.Errorf("vms[%d]: duplicate VM name %q", i, vm.Name))
		default:
			seen[name] = true
		}
		if strings.TrimSpace(vm.VMX) == "" {
			errs = append(errs, fmt.Errorf("vms[%d]: vmx must not be empty", i))
		}
	}
	return errs
}
