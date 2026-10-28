package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xortim/snapback/internal/notify"
)

// notifyDeps groups `notify test`'s one external dependency so tests can
// substitute notify.Fake for the real osascript-backed notifier.
type notifyDeps struct {
	newNotifier func() notify.Notifier
}

func defaultNotifyDeps() notifyDeps {
	return notifyDeps{newNotifier: func() notify.Notifier { return notify.NewOSAScript() }}
}

// notifyTestHint is printed after a test notification is sent. osascript
// notifications are attributed to Script Editor, and macOS may drop them
// silently until that permission is granted -- which only reliably gets
// prompted for while a person is at the machine, not during a midnight
// launchd run.
const notifyTestHint = "sent a test notification. If nothing appeared, allow notifications for Script Editor in System Settings → Notifications (osascript notifications are shown under Script Editor)."

// notifyTip is what `init` prints when notifications are enabled.
const notifyTip = "tip: run `snapback notify test` now so macOS asks for notification permission while you're here"

func newNotifyCmd() *cobra.Command {
	return newNotifyCmdWithDeps(defaultNotifyDeps())
}

func newNotifyCmdWithDeps(deps notifyDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage macOS notifications",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test notification",
		Long:  "Sends a sample notification regardless of config, so macOS prompts for notification permission while you're at the machine. Scheduled runs can't trigger that prompt reliably on their own.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			n := notify.Notification{Title: "snapback", Subtitle: "Test notification", Message: "Notifications are working."}
			if err := deps.newNotifier().Notify(cmd.Context(), n); err != nil {
				return fmt.Errorf("send test notification: %w", err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), notifyTestHint)
			return err
		},
	})
	return cmd
}
