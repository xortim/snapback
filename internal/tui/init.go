// Package tui also renders the interactive `snapback init` wizard, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md's `init` section.
// It never touches the filesystem -- internal/cli/init.go still owns VM
// discovery, config marshaling, and writing config.yaml, calling
// RunInitWizard only to collect answers, the same boundary
// tui.RunInteractive/backup.Run already keep.
package tui

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/huh"

	"github.com/xortim/snapback/internal/config"
)

// VMCandidate is a VM internal/cli's discovery already found (see
// internal/cli/vmdiscovery.go's discoverVMs), passed in rather than
// discovered by this package.
type VMCandidate struct {
	Name string
	VMX  string
}

// newForm builds a huh.Form wired to in/out. In accessible mode, in is
// wrapped in lineBufferedReader (see init_reader.go's doc comment for
// why); in real interactive mode it's passed through unwrapped, since
// WithInput also configures the underlying bubbletea program's raw
// terminal input, which must not be throttled.
func newForm(in io.Reader, out io.Writer, accessible bool, groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithOutput(out)
	if accessible {
		return form.WithAccessible(true).WithInput(lineBufferedReader{r: in})
	}
	return form.WithInput(in)
}

// runForm runs f, translating both a real ctx cancellation (ctrl+c
// propagated via huh's own tea.WithContext, in the real interactive
// path) and huh's own abort signal into the same "init cancelled: %w"
// wording internal/cli's other commands use. Accessible-mode forms don't
// support mid-read cancellation the same way (huh's runAccessible takes
// no context -- verified by reading huh@v1.0.0/form.go) -- acceptable
// since accessible mode's real-world use is piped/scripted input, not an
// interactive user waiting to press ctrl+c.
func runForm(ctx context.Context, f *huh.Form) error {
	if err := f.RunWithContext(ctx); err != nil {
		return fmt.Errorf("init cancelled: %w", err)
	}
	return nil
}

// selectVMs asks which discovered candidates to include (if any were
// found), then always offers manual entry afterward -- discoverVMs
// requires a bundle's .vmx to match the bundle's own name exactly, so a
// VM renamed in Finder after creation is invisible to discovery even
// though it's a valid VM; manual entry is the escape hatch for that,
// available whether or not discovery found anything.
func selectVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) ([]config.VM, error) {
	var vms []config.VM

	if len(candidates) > 0 {
		options := make([]huh.Option[string], len(candidates))
		for i, c := range candidates {
			options[i] = huh.NewOption(c.Name, c.Name).Selected(true)
		}
		var selected []string
		form := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Select VMs to back up").
					Options(options...).
					Value(&selected),
			),
		)
		if err := runForm(ctx, form); err != nil {
			return nil, err
		}

		byName := make(map[string]VMCandidate, len(candidates))
		for _, c := range candidates {
			byName[c.Name] = c
		}
		for _, name := range selected {
			c := byName[name]
			vms = append(vms, config.VM{Name: c.Name, VMX: c.VMX})
		}
	} else if _, err := fmt.Fprintln(out, "no VMs found automatically; add them manually below"); err != nil {
		return nil, err
	}

	manual, err := addManualVMs(ctx, in, out, accessible, len(candidates) == 0)
	if err != nil {
		return nil, err
	}
	return append(vms, manual...), nil
}

// addManualVMs loops "add a VM manually?" (Confirm) followed, if yes, by
// a name+.vmx-path pair, until the user declines. firstDefaultYes
// defaults the very first iteration's Confirm to true -- used when
// discovery found nothing, so the wizard walks straight into naming a
// VM instead of the user having to explicitly say "y" to a question
// whose answer is already implied by there being nothing to select from.
func addManualVMs(ctx context.Context, in io.Reader, out io.Writer, accessible bool, firstDefaultYes bool) ([]config.VM, error) {
	var vms []config.VM
	first := true
	for {
		addMore := firstDefaultYes && first
		first = false

		confirmForm := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewConfirm().
					Title("Add a VM manually?").
					Value(&addMore),
			),
		)
		if err := runForm(ctx, confirmForm); err != nil {
			return nil, err
		}
		if !addMore {
			return vms, nil
		}

		var name, vmx string
		entryForm := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewInput().Title("VM name").Validate(huh.ValidateNotEmpty()).Value(&name),
				huh.NewInput().Title("Path to .vmx file").Validate(huh.ValidateNotEmpty()).Value(&vmx),
			),
		)
		if err := runForm(ctx, entryForm); err != nil {
			return nil, err
		}
		vms = append(vms, config.VM{Name: name, VMX: vmx})
	}
}
