// Package tui also renders the interactive `snapback init` wizard, per
// docs/superpowers/specs/2026-08-23-cli-ux-design.md's `init` section.
// It never touches the filesystem -- internal/cli/init.go still owns VM
// discovery, config marshaling, and writing config.yaml, calling
// RunInitWizard only to collect answers, the same boundary
// tui.RunInteractive/backup.Run already keep.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

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

// runForm runs f, wrapping the error as "init cancelled: %w" only when
// the cause is an actual ctx cancellation (ctrl+c propagated via huh's
// own tea.WithContext, in the real interactive path) or huh's own
// user-abort signal; any other failure (a genuine huh/bubbletea error)
// is reported as "interactive prompt failed: %w" instead of being
// mislabeled as a cancellation. Accessible-mode forms don't support
// mid-read cancellation the same way (huh's runAccessible takes no
// context -- verified by reading huh@v1.0.0/form.go) -- acceptable
// since accessible mode's real-world use is piped/scripted input, not an
// interactive user waiting to press ctrl+c.
func runForm(ctx context.Context, f *huh.Form) error {
	err := f.RunWithContext(ctx)
	if err == nil {
		return nil
	}
	if isCancellation(ctx, err) {
		return fmt.Errorf("init cancelled: %w", err)
	}
	return fmt.Errorf("interactive prompt failed: %w", err)
}

// isCancellation reports whether err represents an actual cancellation
// -- ctx already carrying an error (the caller's own context was
// canceled or timed out) or huh's own user-abort signal (ctrl+c inside
// a real interactive form) -- as opposed to some other huh/bubbletea
// failure that happens to surface through the same RunWithContext call.
func isCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, huh.ErrUserAborted)
}

// Defaults match internal/cli/init.go's old plain prompter, kept
// identical so a fresh `snapback init` proposes the same values as
// before this rewrite.
const (
	defaultDestination = "/Volumes/Backups/snapback"
	defaultCompression = "zstd"
	defaultKeepLast    = 5
	defaultKeepDaily   = 7
	defaultKeepWeekly  = 4
)

type coreSettings struct {
	destination string
	compression string
	keepLast    int
	keepDaily   int
	keepWeekly  int
	notify      bool
}

// promptCoreSettings asks destination, compression, the three retention
// counts, and whether to enable notifications -- one huh.Form of four
// groups, matching docs/superpowers/specs/2026-08-23-cli-ux-design.md's
// "destination path → compression choice → retention numbers" (plus the
// pre-existing notifications toggle, part of config.Config before this
// rewrite and kept here rather than dropped).
func promptCoreSettings(ctx context.Context, in io.Reader, out io.Writer, accessible bool) (coreSettings, error) {
	destination := defaultDestination
	compression := defaultCompression
	keepLastStr := strconv.Itoa(defaultKeepLast)
	keepDailyStr := strconv.Itoa(defaultKeepDaily)
	keepWeeklyStr := strconv.Itoa(defaultKeepWeekly)
	notify := true

	form := newForm(in, out, accessible,
		huh.NewGroup(
			huh.NewInput().
				Title("Backup destination").
				Validate(acceptBlankInAccessibleMode(accessible, validateWritableDestination)).
				Value(&destination),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Compression").
				Options(huh.NewOption("zstd", "zstd"), huh.NewOption("gzip", "gzip")).
				Value(&compression),
		),
		huh.NewGroup(
			huh.NewInput().Title("Keep last N backups").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepLastStr),
			huh.NewInput().Title("Keep daily backups for N days").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepDailyStr),
			huh.NewInput().Title("Keep weekly backups for N weeks").Validate(acceptBlankInAccessibleMode(accessible, validateNonNegativeInt)).Value(&keepWeeklyStr),
		),
		huh.NewGroup(
			huh.NewConfirm().Title("Enable notifications").Value(&notify),
		),
	)
	if err := runForm(ctx, form); err != nil {
		return coreSettings{}, err
	}

	// Each string is already validated by validateNonNegativeInt above,
	// so the parse here cannot fail.
	keepLast, _ := strconv.Atoi(strings.TrimSpace(keepLastStr))
	keepDaily, _ := strconv.Atoi(strings.TrimSpace(keepDailyStr))
	keepWeekly, _ := strconv.Atoi(strings.TrimSpace(keepWeeklyStr))

	return coreSettings{
		destination: destination,
		compression: compression,
		keepLast:    keepLast,
		keepDaily:   keepDaily,
		keepWeekly:  keepWeekly,
		notify:      notify,
	}, nil
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

// promptSchedules asks a schedule preset for each VM in vms, in order,
// mutating vms[i].Schedule in place. The custom-cron question is always
// asked (see resolveSchedule's doc comment for why), gated only by its
// own Validate closure checking the choice already made in the same
// VM's prior group.
func promptSchedules(ctx context.Context, in io.Reader, out io.Writer, accessible bool, vms []config.VM) error {
	for i := range vms {
		choice := scheduleChoiceNone
		var custom string

		form := newForm(in, out, accessible,
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Schedule for %s", vms[i].Name)).
					Options(huh.NewOptions(scheduleChoices...)...).
					Value(&choice),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Custom cron expression (only used if 'custom' was chosen above)").
					Validate(func(s string) error {
						if choice != scheduleChoiceCustom {
							return nil
						}
						return validateCronExpression(s)
					}).
					Value(&custom),
			),
		)
		if err := runForm(ctx, form); err != nil {
			return err
		}
		vms[i].Schedule = resolveSchedule(choice, custom)
	}
	return nil
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

// reviewAndConfirm shows cfg rendered exactly as config.Marshal would
// write it (so the review is guaranteed accurate, not a hand-maintained
// summary that can drift from what actually gets written) and asks for
// confirmation before RunInitWizard returns it to the caller.
func reviewAndConfirm(ctx context.Context, in io.Reader, out io.Writer, accessible bool, cfg *config.Config) (bool, error) {
	rendered, err := config.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("render config for review: %w", err)
	}

	confirmed := true
	form := newForm(in, out, accessible,
		huh.NewGroup(
			huh.NewNote().
				Title("Review").
				Description(string(rendered)),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Write this config?").
				Value(&confirmed),
		),
	)
	if err := runForm(ctx, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

// RunInitWizard drives the interactive `snapback init` flow: select
// which discovered VMs to include (plus manual entry), core settings
// (destination/compression/retention/notifications), a schedule preset
// per included VM, then a review screen showing the exact YAML that
// will be written before writing anything. It returns the built,
// already config.Validate'd Config; internal/cli/init.go still owns
// marshaling and writing it to disk.
//
// accessible forces huh's plain sequential-prompt mode instead of a real
// bubbletea render -- internal/cli/init.go sets this from the same TTY
// check run.go already uses, since a real bubbletea program can't read a
// non-terminal stdin (a pipe, a test's strings.Reader) correctly. It's
// also how this package's own tests drive the wizard deterministically.
func RunInitWizard(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate) (*config.Config, error) {
	vms, err := selectVMs(ctx, in, out, accessible, candidates)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateVMs(vms); err != nil {
		return nil, fmt.Errorf("invalid VM selection: %w", err)
	}

	settings, err := promptCoreSettings(ctx, in, out, accessible)
	if err != nil {
		return nil, err
	}

	if err := promptSchedules(ctx, in, out, accessible, vms); err != nil {
		return nil, err
	}

	cfg := &config.Config{
		Destination: settings.destination,
		Compression: settings.compression,
		Retention: config.Retention{
			KeepLast:   settings.keepLast,
			KeepDaily:  settings.keepDaily,
			KeepWeekly: settings.keepWeekly,
		},
		VMs:           vms,
		Notifications: config.Notifications{Enabled: settings.notify},
	}
	if err := config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("built an invalid config: %w", err)
	}

	ok, err := reviewAndConfirm(ctx, in, out, accessible, cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("init aborted: config not written")
	}
	return cfg, nil
}
