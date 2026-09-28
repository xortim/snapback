package launchd

import (
	"errors"
	"testing"
)

func TestClassifyAgent_NoPlistOnDisk_ReturnsAgentMissing(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentMissing {
		t.Errorf("classifyAgent() = %v, want agentMissing", state)
	}
}

func TestClassifyAgent_ContentDiffers_ReturnsAgentDiffers(t *testing.T) {
	inst := NewFakeInstaller()
	installed := Agent{Label: "com.tim.snapback.dev", VMName: "dev", BinaryPath: "/old/bin/snapback", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(installed); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	desired := installed
	desired.BinaryPath = "/new/bin/snapback"
	state, err := classifyAgent(inst, desired)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentDiffers {
		t.Errorf("classifyAgent() = %v, want agentDiffers", state)
	}
	// A content mismatch is drift regardless of load state -- classifyAgent
	// must not even need to ask.
	if len(inst.IsLoadedCalls) != 0 {
		t.Errorf("IsLoadedCalls = %v, want none consulted when content already differs", inst.IsLoadedCalls)
	}
}

func TestClassifyAgent_ContentMatchesButNotLoaded_ReturnsAgentNotLoaded(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	// Deliberately never Bootstrap'd -- FakeInstaller defaults to "not
	// loaded" for any label it hasn't seen a Bootstrap call for.

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentNotLoaded {
		t.Errorf("classifyAgent() = %v, want agentNotLoaded", state)
	}
}

func TestClassifyAgent_ContentMatchesAndLoaded_ReturnsAgentInSync(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	path, _, err := inst.Write(agent)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := inst.Bootstrap(path); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	state, err := classifyAgent(inst, agent)
	if err != nil {
		t.Fatalf("classifyAgent() error = %v", err)
	}
	if state != agentInSync {
		t.Errorf("classifyAgent() = %v, want agentInSync", state)
	}
}

func TestClassifyAgent_ReadError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	boom := errors.New("simulated read failure")
	inst.ReadErr = boom
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev"}

	_, err := classifyAgent(inst, agent)
	if !errors.Is(err, boom) {
		t.Errorf("classifyAgent() error = %v, want it to wrap %v", err, boom)
	}
}

func TestClassifyAgent_IsLoadedError_IsPropagated(t *testing.T) {
	inst := NewFakeInstaller()
	agent := Agent{Label: "com.tim.snapback.dev", VMName: "dev", Interval: calendarInterval("daily")}
	if _, _, err := inst.Write(agent); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	boom := errors.New("simulated launchctl print failure")
	inst.IsLoadedErr = boom

	_, err := classifyAgent(inst, agent)
	if !errors.Is(err, boom) {
		t.Errorf("classifyAgent() error = %v, want it to wrap %v", err, boom)
	}
}
