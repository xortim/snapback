package launchd

import "testing"

func TestFakeInstaller_IsLoaded_DefaultsToFalse(t *testing.T) {
	inst := NewFakeInstaller()
	loaded, err := inst.IsLoaded("com.tim.snapback.dev")
	if err != nil {
		t.Fatalf("IsLoaded() error = %v, want nil", err)
	}
	if loaded {
		t.Error("IsLoaded() = true for a label never bootstrapped, want false")
	}
}

func TestFakeInstaller_IsLoaded_TrueAfterBootstrap(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	loaded, err := inst.IsLoaded(agent.Label)
	if err != nil {
		t.Fatalf("IsLoaded() error = %v", err)
	}
	if !loaded {
		t.Error("IsLoaded() = false after Bootstrap, want true")
	}
}

func TestFakeInstaller_IsLoaded_FalseAfterBootout(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if err := inst.Bootout(agent.Label); err != nil {
		t.Fatalf("Bootout() error = %v", err)
	}
	loaded, err := inst.IsLoaded(agent.Label)
	if err != nil {
		t.Fatalf("IsLoaded() error = %v", err)
	}
	if loaded {
		t.Error("IsLoaded() = true after Bootout, want false")
	}
}

func TestFakeInstaller_IsLoaded_PropagatesConfiguredError(t *testing.T) {
	inst := NewFakeInstaller()
	boom := &fakeErr{"simulated launchctl print failure"}
	inst.IsLoadedErr = boom
	if _, err := inst.IsLoaded("com.tim.snapback.dev"); err != boom {
		t.Errorf("IsLoaded() error = %v, want %v", err, boom)
	}
	if len(inst.IsLoadedCalls) != 1 || inst.IsLoadedCalls[0] != "com.tim.snapback.dev" {
		t.Errorf("IsLoadedCalls = %v, want the label recorded even on error", inst.IsLoadedCalls)
	}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
