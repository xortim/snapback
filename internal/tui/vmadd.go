package tui

import (
	"context"
	"fmt"
	"io"

	"github.com/xortim/snapback/internal/config"
)

// AddVMs drives the "pick which VMs to add" + "schedule per VM" portion
// of the init wizard standalone, for `snapback vm add`
// (internal/cli/vm.go): reuses selectVMs (candidate selection + manual
// entry) and promptSchedules (per-VM schedule preset) exactly as
// RunInitWizard does, rather than duplicating either, since those two
// steps are exactly what's needed to add VMs to an *existing* config
// without re-asking about destination, compression, retention, or
// notifications. candidates should already have any VM present in the
// existing config filtered out by the caller -- this only handles
// selection among what's passed in.
func AddVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) ([]config.VM, error) {
	vms, err := selectVMs(ctx, in, out, accessible, candidates)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateVMs(vms); err != nil {
		return nil, fmt.Errorf("invalid VM selection: %w", err)
	}

	if err := promptSchedules(ctx, in, out, accessible, vms, nil); err != nil {
		return nil, err
	}
	return vms, nil
}
