package launchd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchctlInstaller_WriteThenList(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(t.TempDir(), "Logs", "snapback")
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: filepath.Join(logDir, "dev.log"), Interval: calendarInterval("daily")}

	path, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !changed {
		t.Error("Write() changed = false on first write, want true")
	}
	if path != filepath.Join(dir, "com.tim.snapback.dev.plist") {
		t.Errorf("Write() path = %q, want %q", path, filepath.Join(dir, "com.tim.snapback.dev.plist"))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("plist file not written: %v", err)
	}

	// launchd won't create StandardOutPath's parent directory itself --
	// if Write doesn't, a scheduled run produces no log at all.
	info, err := os.Stat(logDir)
	if err != nil {
		t.Errorf("log directory %s not created by Write(): %v", logDir, err)
	} else if !info.IsDir() {
		t.Errorf("%s exists but is not a directory", logDir)
	}

	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 1 || labels[0] != "com.tim.snapback.dev" {
		t.Errorf("List() = %v, want [\"com.tim.snapback.dev\"]", labels)
	}
}

func TestLaunchctlInstaller_Write_UnchangedOnIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}

	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	_, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if changed {
		t.Error("second Write() with identical content changed = true, want false")
	}
}

func TestLaunchctlInstaller_Write_ChangedWhenScheduleDiffers(t *testing.T) {
	dir := t.TempDir()
	inst := &LaunchctlInstaller{Dir: dir}
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/bin/snapback", LogPath: "/log", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}

	agent.Interval = calendarInterval("weekly")
	_, changed, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if !changed {
		t.Error("second Write() with a different schedule changed = false, want true")
	}
}

func TestLaunchctlInstaller_Remove_MissingFileIsNotAnError(t *testing.T) {
	inst := &LaunchctlInstaller{Dir: t.TempDir()}
	if err := inst.Remove("com.tim.snapback.never-existed"); err != nil {
		t.Errorf("Remove() on a nonexistent plist = %v, want nil", err)
	}
}

func TestLaunchctlInstaller_List_EmptyDir(t *testing.T) {
	inst := &LaunchctlInstaller{Dir: t.TempDir()}
	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() = %v, want empty", labels)
	}
}

func TestLaunchctlInstaller_List_IgnoresNonSnapbackPlists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "com.apple.something.plist"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	inst := &LaunchctlInstaller{Dir: dir}
	labels, err := inst.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() = %v, want it to ignore a non-snapback plist", labels)
	}
}
