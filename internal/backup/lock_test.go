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
