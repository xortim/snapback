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
// discovered by this package. Dir is the basename of the search
// directory the candidate was found in -- used only by selectVMs to
// disambiguate two candidates sharing a Name (#48), never written to
// config.yaml.
type VMCandidate struct {
	Name string
	VMX  string
	Dir  string
}

// errUnexpectedEOF is what runForm returns when an accessible-mode
// form's input reader hits EOF at any point during the run -- see
// lineBufferedReader's doc comment for why huh itself can't surface
// this. Wording matches the hand-rolled prompter this wizard replaced,
// which treated a truncated interactive session as a hard failure on
// the rationale that the resulting config was never actually reviewed
// by the user.
var errUnexpectedEOF = errors.New("read input: unexpected end of input")

// runForm builds a huh.Form from groups wired to in/out and runs it,
// combining what used to be two separate helpers (newForm+runForm) so
// the EOF check below can't be skipped by a call site that builds a form
// without it.
//
// In accessible mode, in is wrapped in a *lineBufferedReader (see
// init_reader.go's doc comment for why the pointer matters) and, once
// the form finishes, checked for EOF: if the underlying reader ran out
// at any point during the run, the form is treated as failed regardless
// of what huh itself returned (nil, in that case -- huh's accessible
// mode has no way to report EOF, so anything left at a default/
// unvalidated value because the reader ran dry was never actually
// reviewed by the user). In real interactive mode in is passed through
// unwrapped, since WithInput also configures the underlying bubbletea
// program's raw terminal input, which must not be throttled, and real
// terminal input doesn't hit this failure mode.
//
// Any other failure is wrapped as "init cancelled: %w" only when the
// cause is an actual ctx cancellation (ctrl+c propagated via huh's own
// tea.WithContext, in the real interactive path, or noticed by the
// select below in the accessible path) or huh's own user-abort signal;
// anything else (a genuine huh/bubbletea error) is reported as
// "interactive prompt failed: %w" instead of being mislabeled as a
// cancellation.
//
// huh's runAccessible takes no context and blocks synchronously on the
// input reader (verified by reading huh@v1.0.0/form.go), so
// RunWithContext is run in a goroutine and raced against ctx.Done()
// below rather than called inline: without that, a canceled ctx (ctrl+c
// via cmd/snapback/main.go's signal.NotifyContext) would sit unnoticed
// until the accessible-mode read it's blocked on eventually returns --
// which, for a real interactive terminal with only stdout piped (still
// accessible, since accessible triggers on either stream), may be
// "never", leaving the process hung on a first ctrl-C and reliant on a
// second one hitting the default SIGINT disposition to force-kill it.
// The goroutine leaks past a cancellation (still blocked on the read),
// same trade-off internal/cli/init.go's discoverVMsWithContext accepts
// for the same reason: the process is exiting anyway.
func runForm(ctx context.Context, in io.Reader, out io.Writer, accessible bool, groups ...*huh.Group) error {
	form := huh.NewForm(groups...).WithOutput(out)

	var reader *lineBufferedReader
	if accessible {
		reader = &lineBufferedReader{r: in}
		form = form.WithAccessible(true).WithInput(reader)
	} else {
		form = form.WithInput(in)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- form.RunWithContext(ctx) }()

	select {
	case <-ctx.Done():
		return fmt.Errorf("init cancelled: %w", ctx.Err())
	case err := <-errCh:
		if reader != nil && reader.sawEOF {
			return errUnexpectedEOF
		}
		if err == nil {
			return nil
		}
		if isCancellation(ctx, err) {
			return fmt.Errorf("init cancelled: %w", err)
		}
		return fmt.Errorf("interactive prompt failed: %w", err)
	}
}

// isCancellation reports whether err represents an actual cancellation
// -- ctx already carrying an error (the caller's own context was
// canceled or timed out) or huh's own user-abort signal (ctrl+c inside
// a real interactive form) -- as opposed to some other huh/bubbletea
// failure that happens to surface through the same RunWithContext call.
func isCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, huh.ErrUserAborted)
}

// defaultDestination lives under the user's home directory rather than
// /Volumes (an external/network mount root, not a general-purpose
// writable directory -- see #49): that guarantees it's writable on a
// completely fresh run with no drive attached, and it's picked up by
// Time Machine's default whole-disk backup policy for free.
const (
	defaultDestination = "~/Backups/snapback"
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
//
// prior, when non-nil (an existing config.yaml found under `init
// --force`, per #52), seeds every field's starting value from it
// instead of the hardcoded constants -- so re-running the wizard over
// an established config proposes what's already there rather than
// silently offering to reset destination/compression/retention/
// notifications back to factory defaults. A fresh init (prior == nil)
// is unaffected.
func promptCoreSettings(ctx context.Context, in io.Reader, out io.Writer, accessible bool, prior *config.Config) (coreSettings, error) {
	destination := defaultDestination
	compression := defaultCompression
	keepLast := defaultKeepLast
	keepDaily := defaultKeepDaily
	keepWeekly := defaultKeepWeekly
	notify := true
	if prior != nil {
		destination = prior.Destination
		compression = prior.Compression
		keepLast = prior.Retention.KeepLast
		keepDaily = prior.Retention.KeepDaily
		keepWeekly = prior.Retention.KeepWeekly
		notify = prior.Notifications.Enabled
	}
	keepLastStr := strconv.Itoa(keepLast)
	keepDailyStr := strconv.Itoa(keepDaily)
	keepWeeklyStr := strconv.Itoa(keepWeekly)

	err := runForm(ctx, in, out, accessible,
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
	if err != nil {
		return coreSettings{}, err
	}

	// Each string is guaranteed valid at this point -- not merely by
	// validateNonNegativeInt in isolation, but by that validator combined
	// with runForm's EOF guard above: huh only lets a field's value stand
	// once its own Validate closure has returned nil for it, and runForm
	// turns any input EOF (which is how an invalid value with no
	// subsequent correction would otherwise reach here unvalidated -- see
	// lineBufferedReader's doc comment) into errUnexpectedEOF before this
	// line is ever reached. So this parse cannot fail.
	keepLast, _ = strconv.Atoi(strings.TrimSpace(keepLastStr))
	keepDaily, _ = strconv.Atoi(strings.TrimSpace(keepDailyStr))
	keepWeekly, _ = strconv.Atoi(strings.TrimSpace(keepWeeklyStr))

	return coreSettings{
		destination: destination,
		compression: compression,
		keepLast:    keepLast,
		keepDaily:   keepDaily,
		keepWeekly:  keepWeekly,
		notify:      notify,
	}, nil
}

// candidateLabel returns c's display label for the MultiSelect option
// list: the plain Name, unless duplicateName is true (another candidate
// in the same list shares it), in which case it's suffixed with c.Dir
// (the search directory's basename) so the user can tell the colliding
// entries apart without seeing a full path.
func candidateLabel(c VMCandidate, duplicateName bool) string {
	if !duplicateName {
		return c.Name
	}
	return fmt.Sprintf("⚠ %s (also in %s)", c.Name, c.Dir)
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
		// The option value is VMX, not Name: two candidates can now share a
		// Name (a genuine collision across search dirs, see #48 -- discoverVMs
		// no longer drops one to hide it), and VMX is the one field
		// guaranteed unique per bundle. Using Name as the value here would
		// make a duplicate-named pair indistinguishable once selected.
		names := make(map[string]int, len(candidates))
		for _, c := range candidates {
			names[c.Name]++
		}
		options := make([]huh.Option[string], len(candidates))
		for i, c := range candidates {
			options[i] = huh.NewOption(candidateLabel(c, names[c.Name] > 1), c.VMX).Selected(true)
		}
		var selected []string
		err := runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Select VMs to back up").
					Options(options...).
					Value(&selected),
			),
		)
		if err != nil {
			return nil, err
		}

		byVMX := make(map[string]VMCandidate, len(candidates))
		for _, c := range candidates {
			byVMX[c.VMX] = c
		}
		for _, vmx := range selected {
			c := byVMX[vmx]
			vms = append(vms, config.VM{Name: c.Name, VMX: c.VMX})
		}
		// Validated here, before manual entry, so a bad selection fails fast
		// rather than after the user has also stepped through the (possibly
		// multi-VM) manual-entry prompts below -- matching the fail-fast
		// behavior of promptVMs, the plain prompter this wizard replaced.
		if err := config.ValidateVMs(vms); err != nil {
			return nil, fmt.Errorf("invalid VM selection: %w", err)
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

		err := runForm(ctx, in, out, accessible,
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
		if err != nil {
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

		err := runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewConfirm().
					Title("Add a VM manually?").
					Value(&addMore),
			),
		)
		if err != nil {
			return nil, err
		}
		if !addMore {
			return vms, nil
		}

		var name, vmx string
		err = runForm(ctx, in, out, accessible,
			huh.NewGroup(
				huh.NewInput().Title("VM name").Validate(huh.ValidateNotEmpty()).Value(&name),
				huh.NewInput().Title("Path to .vmx file").Validate(huh.ValidateNotEmpty()).Value(&vmx),
			),
		)
		if err != nil {
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
	err = runForm(ctx, in, out, accessible,
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
	if err != nil {
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
//
// prior is forwarded to promptCoreSettings -- see its doc comment. VM
// selection and schedules are unaffected by prior; only core settings
// seed from an existing config.
func RunInitWizard(ctx context.Context, in io.Reader, out io.Writer, accessible bool, candidates []VMCandidate, prior *config.Config) (*config.Config, error) {
	vms, err := selectVMs(ctx, in, out, accessible, candidates)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateVMs(vms); err != nil {
		return nil, fmt.Errorf("invalid VM selection: %w", err)
	}

	settings, err := promptCoreSettings(ctx, in, out, accessible, prior)
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
