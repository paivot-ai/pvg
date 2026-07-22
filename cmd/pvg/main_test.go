package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
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

// captureStdout redirects os.Stdout while fn runs and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(data)
}

// writeActiveLoopState writes an active loop state at root.
func writeActiveLoopState(t *testing.T, root string) {
	t.Helper()
	if err := loop.WriteState(root, loop.NewState("epic", "PROJ-epic", 50)); err != nil {
		t.Fatal(err)
	}
}

func TestLoopAgentSet_RecordsHandleAndResetsResumes(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-abc"}); err != nil {
			t.Errorf("loop agent set error: %v", err)
		}
	})

	state, err := loop.ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := state.AgentHandleFor("PROJ-s1", loop.AgentRoleDeveloper)
	if !ok || h.Handle != "agent-abc" || h.Resumes != 0 {
		t.Fatalf("expected recorded handle agent-abc with 0 resumes, got %+v (ok=%v)", h, ok)
	}

	// Overwrite must reset the resume counter.
	state.SetAgentHandle("PROJ-s1", loop.AgentRoleDeveloper, "agent-abc")
	stored, _ := state.AgentHandleFor("PROJ-s1", loop.AgentRoleDeveloper)
	stored.Resumes = 2
	state.AgentHandles["PROJ-s1"][loop.AgentRoleDeveloper] = stored
	if err := loop.WriteState(root, state); err != nil {
		t.Fatal(err)
	}
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-new"}); err != nil {
			t.Errorf("loop agent set overwrite error: %v", err)
		}
	})
	state, err = loop.ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	h, _ = state.AgentHandleFor("PROJ-s1", loop.AgentRoleDeveloper)
	if h.Handle != "agent-new" || h.Resumes != 0 {
		t.Fatalf("expected overwrite to reset resumes, got %+v", h)
	}
}

func TestLoopAgentSet_RejectsInvalidRole(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	err := loopAgent(root, []string{"set", "PROJ-s1", "pm_acceptor", "agent-abc"})
	if err == nil || !strings.Contains(err.Error(), "invalid role") {
		t.Fatalf("expected invalid role error, got %v", err)
	}
}

func TestLoopAgentSet_ErrorsWithoutActiveLoop(t *testing.T) {
	root := t.TempDir()

	err := loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-abc"})
	if err == nil || !strings.Contains(err.Error(), "no active loop") {
		t.Fatalf("expected no-active-loop error, got %v", err)
	}

	// An inactive state file is the same refusal.
	state := loop.NewState("epic", "PROJ-epic", 50)
	state.Active = false
	if err := loop.WriteState(root, state); err != nil {
		t.Fatal(err)
	}
	err = loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-abc"})
	if err == nil || !strings.Contains(err.Error(), "no active loop") {
		t.Fatalf("expected no-active-loop error for inactive state, got %v", err)
	}
}

func TestLoopAgentClear_RoleThenAllIdempotent(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-dev"}); err != nil {
			t.Fatal(err)
		}
		if err := loopAgent(root, []string{"set", "PROJ-s1", "pm", "agent-pm"}); err != nil {
			t.Fatal(err)
		}
	})

	// Clear one role: the other survives.
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"clear", "PROJ-s1", "pm"}); err != nil {
			t.Errorf("clear role error: %v", err)
		}
	})
	state, err := loop.ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.AgentHandleFor("PROJ-s1", loop.AgentRolePM); ok {
		t.Fatal("expected pm handle cleared")
	}
	if _, ok := state.AgentHandleFor("PROJ-s1", loop.AgentRoleDeveloper); !ok {
		t.Fatal("expected developer handle preserved")
	}

	// Clear the story without a role: everything for it goes.
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"clear", "PROJ-s1"}); err != nil {
			t.Errorf("clear story error: %v", err)
		}
	})
	state, err = loop.ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.AgentHandleFor("PROJ-s1", loop.AgentRoleDeveloper); ok {
		t.Fatal("expected all handles for the story cleared")
	}

	// Idempotent: clearing again (and clearing unknown stories) succeeds.
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"clear", "PROJ-s1"}); err != nil {
			t.Errorf("repeat clear must not error: %v", err)
		}
		if err := loopAgent(root, []string{"clear", "PROJ-unknown", "developer"}); err != nil {
			t.Errorf("clearing an absent story must not error: %v", err)
		}
	})
}

func TestLoopAgentClear_ToleratesMissingLoop(t *testing.T) {
	root := t.TempDir()
	out := captureStdout(t, func() {
		if err := loopAgent(root, []string{"clear", "PROJ-s1"}); err != nil {
			t.Errorf("clear without a loop must not error: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to clear") {
		t.Fatalf("expected a nothing-to-clear notice, got %q", out)
	}
}

func TestLoopAgentClear_RejectsInvalidRole(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	err := loopAgent(root, []string{"clear", "PROJ-s1", "bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid role") {
		t.Fatalf("expected invalid role error, got %v", err)
	}
}

func TestLoopAgentList_JSONAndText(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)
	_ = captureStdout(t, func() {
		if err := loopAgent(root, []string{"set", "PROJ-s1", "developer", "agent-dev"}); err != nil {
			t.Fatal(err)
		}
	})

	out := captureStdout(t, func() {
		if err := loopAgent(root, []string{"list", "--json"}); err != nil {
			t.Errorf("loop agent list --json error: %v", err)
		}
	})
	var handles map[string]map[string]loop.AgentHandle
	if err := json.Unmarshal([]byte(out), &handles); err != nil {
		t.Fatalf("expected JSON handle map, got %q: %v", out, err)
	}
	if h := handles["PROJ-s1"][loop.AgentRoleDeveloper]; h.Handle != "agent-dev" || h.Resumes != 0 {
		t.Fatalf("unexpected listed handle: %+v", h)
	}

	text := captureStdout(t, func() {
		if err := loopAgent(root, []string{"list"}); err != nil {
			t.Errorf("loop agent list error: %v", err)
		}
	})
	if !strings.Contains(text, "PROJ-s1") || !strings.Contains(text, "handle=agent-dev") || !strings.Contains(text, "resumes=0/2") {
		t.Fatalf("unexpected text listing: %q", text)
	}
}

func TestLoopAgentList_NoLoopIsNotAnError(t *testing.T) {
	root := t.TempDir()
	out := captureStdout(t, func() {
		if err := loopAgent(root, []string{"list", "--json"}); err != nil {
			t.Errorf("list without a loop must not error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("expected empty JSON object, got %q", out)
	}
}

func TestLoopAgent_UnknownSubcommand(t *testing.T) {
	root := t.TempDir()
	if err := loopAgent(root, []string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown agent subcommand")
	}
	if err := loopAgent(root, nil); err == nil {
		t.Fatal("expected error for missing agent subcommand")
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
