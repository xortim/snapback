package notify

import (
	"context"
	"sync"
)

// Fake is an in-memory Notifier for unit tests, mirroring
// vm.FakeVMController and launchd.FakeInstaller. Every call is recorded
// before Err (if set) is returned, so a failed send still shows up in
// Sent for a test to assert against.
type Fake struct {
	Err      error
	Sent     []Notification
	Contexts []context.Context

	mu sync.Mutex
}

// Notify implements Notifier.
func (f *Fake) Notify(ctx context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, n)
	f.Contexts = append(f.Contexts, ctx)
	return f.Err
}
