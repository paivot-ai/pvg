package main

import (
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
)

func TestValidateGitRef(t *testing.T) {
	valid := []string{
		"main",
		"epic/PRA-ru13",
		"v1.2.3",
		"HEAD~1",
		"main^",
		"abc123def",
		"origin/main",
	}
	for _, ref := range valid {
		if err := validateGitRef(ref); err != nil {
			t.Errorf("validateGitRef(%q) = %v, want nil", ref, err)
		}
	}

	invalid := []string{
		"--upload-pack=x",
		"-x",
		"a b",
		"a;b",
		"a$(x)",
		"",
	}
	for _, ref := range invalid {
		if err := validateGitRef(ref); err == nil {
			t.Errorf("validateGitRef(%q) = nil, want error", ref)
		}
	}
}

// dispatcherEnabled reads dispatcher state at root and reports Enabled.
func dispatcherEnabled(t *testing.T, root string) bool {
	t.Helper()
	state, err := dispatcher.ReadState(root)
	return err == nil && state.Enabled
}

func TestEnsureLoopDispatcher_EnablesWhenOff(t *testing.T) {
	root := t.TempDir()

	enabled, err := ensureLoopDispatcher(root)
	if err != nil {
		t.Fatalf("ensureLoopDispatcher() error: %v", err)
	}
	if !enabled {
		t.Fatal("expected setup to report it enabled dispatcher mode")
	}
	if !dispatcherEnabled(t, root) {
		t.Fatal("expected dispatcher mode enabled")
	}
}

func TestEnsureLoopDispatcher_LeavesEnabledDispatcherUntouched(t *testing.T) {
	root := t.TempDir()
	if err := dispatcher.On(root); err != nil {
		t.Fatal(err)
	}
	// Simulate an active tracked agent: re-enabling would reset this map.
	if err := dispatcher.TrackAgent(root, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}

	enabled, err := ensureLoopDispatcher(root)
	if err != nil {
		t.Fatalf("ensureLoopDispatcher() error: %v", err)
	}
	if enabled {
		t.Fatal("expected setup NOT to claim ownership of a pre-enabled dispatcher")
	}

	state, err := dispatcher.ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveAgents) != 1 {
		t.Fatalf("expected agent tracking preserved, got %v", state.ActiveAgents)
	}
}

func TestLoopCancel_DisablesDispatcherEnabledBySetup(t *testing.T) {
	root := t.TempDir()

	// Posture written by loop setup: active loop that enabled dispatcher mode.
	enabled, err := ensureLoopDispatcher(root)
	if err != nil || !enabled {
		t.Fatalf("ensureLoopDispatcher() = %v, %v", enabled, err)
	}
	state := loop.NewState("epic", "PROJ-epic", 50)
	state.DispatcherEnabledBySetup = true
	if err := loop.WriteState(root, state); err != nil {
		t.Fatal(err)
	}

	if err := loopCancel(root); err != nil {
		t.Fatalf("loopCancel() error: %v", err)
	}
	if loop.IsActive(root) {
		t.Fatal("expected loop state removed")
	}
	if dispatcherEnabled(t, root) {
		t.Fatal("expected cancel to disable the dispatcher the loop enabled")
	}
}

func TestLoopCancel_LeavesUserEnabledDispatcherOn(t *testing.T) {
	root := t.TempDir()

	// Posture: user ran `pvg dispatcher on` before the loop; setup recorded
	// that it did NOT enable dispatcher mode.
	if err := dispatcher.On(root); err != nil {
		t.Fatal(err)
	}
	state := loop.NewState("epic", "PROJ-epic", 50)
	state.DispatcherEnabledBySetup = false
	if err := loop.WriteState(root, state); err != nil {
		t.Fatal(err)
	}

	if err := loopCancel(root); err != nil {
		t.Fatalf("loopCancel() error: %v", err)
	}
	if loop.IsActive(root) {
		t.Fatal("expected loop state removed")
	}
	if !dispatcherEnabled(t, root) {
		t.Fatal("expected cancel to leave a user-enabled dispatcher on")
	}
}
