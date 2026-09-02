package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrLocked is returned (wrapped) by AcquireLock when another process
// already holds the lock for this destination+VM pair -- most likely a
// concurrent `run` or `cleanup` invocation against the same VM.
var ErrLocked = errors.New("backup lock already held")

// Lock is an exclusive, advisory, per-VM file lock used to serialize
// `run` and `cleanup` against the same VM's snapshots. Run (choreography.go)
// holds it for the whole choreography, from before Snapshot through after
// DeleteSnapshot; cleanup (internal/cli/cleanup.go) takes it before
// deleting anything, so the two can never touch the same VM's snapshots
// at the same time. Backed by flock(2): if the holding process dies, the
// kernel releases the lock automatically when its file descriptor
// closes, so a crashed run can never leave a stale lock behind.
type Lock struct {
	f *os.File
}

// lockPath returns the fixed lock-file path for a given destination and
// VM name -- both run and cleanup derive the same path from the same
// config, so they contend on the same file regardless of which command
// gets there first.
func lockPath(destination, vmName string) string {
	return filepath.Join(destination, ".snapback-locks", vmName+".lock")
}

// AcquireLock takes an exclusive, non-blocking lock for vmName under
// destination. If another process already holds it, this returns
// immediately with an error wrapping ErrLocked instead of waiting.
func AcquireLock(destination, vmName string) (*Lock, error) {
	path := lockPath(destination, vmName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %q", ErrLocked, vmName)
		}
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release unlocks and closes the lock file. Both current call sites use
// a single defer, so a second call never happens today -- but Release
// guards against one anyway: a nil receiver, or a Lock whose file has
// already been released, is a safe no-op rather than reusing an fd
// number the OS may have since handed to something unrelated.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	defer func() { _ = f.Close() }()
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
