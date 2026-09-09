package backup_test

import (
	"errors"
	"testing"

	"github.com/xortim/snapback/internal/backup"
	"github.com/xortim/snapback/internal/vm"
)

func TestCheckVMDiskConsistency_HealthyDisk_ReturnsNil(t *testing.T) {
	vmxPath := writeMinimalVMXWithDisk(t)
	fake := vm.NewFakeVMController()

	if err := backup.CheckVMDiskConsistency(fake, vmxPath); err != nil {
		t.Errorf("CheckVMDiskConsistency() error = %v, want nil", err)
	}
}

func TestCheckVMDiskConsistency_DamagedDisk_ReturnsError(t *testing.T) {
	vmxPath := writeMinimalVMXWithDisk(t)
	fake := vm.NewFakeVMController()
	fake.DiskConsistencyErr = errBoom

	err := backup.CheckVMDiskConsistency(fake, vmxPath)
	if !errors.Is(err, errBoom) {
		t.Errorf("CheckVMDiskConsistency() error = %v, want it to wrap %v", err, errBoom)
	}
}
