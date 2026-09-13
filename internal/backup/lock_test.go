package backup_test

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/backup"
)

func TestAcquireLock_SecondAcquireForSameVMFailsWithErrLocked(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	_, err = backup.AcquireLock(dest, "myvm")
	if !errors.Is(err, backup.ErrLocked) {
		t.Errorf("second AcquireLock() error = %v, want it to wrap ErrLocked", err)
	}
}

func TestAcquireLock_DistinctVMNamesDoNotContend(t *testing.T) {
	dest := t.TempDir()
	lockA, err := backup.AcquireLock(dest, "vm-a")
	if err != nil {
		t.Fatalf("AcquireLock(vm-a) error = %v, want nil", err)
	}
	defer func() { _ = lockA.Release() }()

	lockB, err := backup.AcquireLock(dest, "vm-b")
	if err != nil {
		t.Fatalf("AcquireLock(vm-b) error = %v, want nil (different VM, must not contend)", err)
	}
	defer func() { _ = lockB.Release() }()
}

func TestAcquireLock_ReleaseFreesTheLockForReacquisition(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}

	lock2, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after Release() error = %v, want nil", err)
	}
	_ = lock2.Release()
}

func TestIsRunning_FalseWhenNoLockHeld(t *testing.T) {
	dest := t.TempDir()
	running, err := backup.IsRunning(dest, "myvm")
	if err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}
	if running {
		t.Error("IsRunning() = true, want false -- nothing holds the lock")
	}
}

func TestIsRunning_TrueWhileAnotherProcessHoldsTheLock(t *testing.T) {
	dest := t.TempDir()
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() error = %v, want nil", err)
	}
	defer func() { _ = lock.Release() }()

	running, err := backup.IsRunning(dest, "myvm")
	if err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}
	if !running {
		t.Error("IsRunning() = false, want true -- the lock is held")
	}
}

func TestIsRunning_DoesNotItselfHoldTheLockAfterReturning(t *testing.T) {
	dest := t.TempDir()
	if _, err := backup.IsRunning(dest, "myvm"); err != nil {
		t.Fatalf("IsRunning() error = %v, want nil", err)
	}

	// If IsRunning leaked its own probe lock, this second AcquireLock
	// would fail with ErrLocked.
	lock, err := backup.AcquireLock(dest, "myvm")
	if err != nil {
		t.Fatalf("AcquireLock() after IsRunning() error = %v, want nil -- IsRunning must release its probe lock", err)
	}
	_ = lock.Release()
}
