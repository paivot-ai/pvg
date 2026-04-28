package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckCWDDrift_NormalCWD(t *testing.T) {
	// CWD not inside a worktree -- always allowed.
	result := CheckCWDDrift("/some/project")
	if !result.Allowed {
		t.Fatalf("expected allowed for normal CWD, got blocked: %s", result.Reason)
	}
}

func TestCheckCWDDrift_WorktreeCWDWithActiveAgent(t *testing.T) {
	// Simulate: CWD inside worktree, dispatcher active, developer agent active.
	// This should be BLOCKED -- the dispatcher never needs CWD inside a worktree.
	// Developer subagent Bash commands run in the DEVELOPER'S own session.
	// Previously this was incorrectly allowed, which neutralized the guard.

	root := t.TempDir()
	worktreeDir := filepath.Join(root, ".claude", "worktrees", "dev-TEST-001")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Resolve symlinks so the worktree path matches what os.Getwd() returns
	// (macOS /var/folders -> /private/var/folders).
	resolvedWorktree := worktreeDir
	if resolved, err := filepath.EvalSymlinks(worktreeDir); err == nil {
		resolvedWorktree = resolved
	}

	// Create dispatcher state with an active developer at the worktree path.
	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{
		"enabled":         true,
		"since":           "2026-01-01T00:00:00Z",
		"active_agents":   map[string]string{"agent-1": "paivot-graph:developer"},
		"agent_worktrees": map[string]string{"agent-1": resolvedWorktree},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// cd into the worktree to simulate drift.
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if result.Allowed {
		t.Fatal("expected BLOCKED (dispatcher CWD inside worktree), got allowed")
	}
	if result.Reason == "" {
		t.Fatal("expected a reason message")
	}
}

func TestCheckCWDDrift_WorktreeCWDNoActiveAgent(t *testing.T) {
	// Simulate: CWD inside worktree, dispatcher active, NO active agents
	// AND no tracked worktrees. This is the harness-managed-worktree case:
	// the Claude Code Agent tool spawned a worktree outside Paivot's
	// knowledge, then invoked Bash from inside it. The PreToolUse hook
	// fires in the SUBAGENT's session (not the dispatcher's), so blocking
	// would prevent the harness's parallel execution model from working.
	// Expected: ALLOWED.

	root := t.TempDir()
	worktreeDir := filepath.Join(root, ".claude", "worktrees", "agent-deadbeefcafebabe")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create dispatcher state with NO active agents and NO tracked worktrees.
	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{
		"enabled":         true,
		"since":           "2026-01-01T00:00:00Z",
		"active_agents":   map[string]string{},
		"agent_worktrees": map[string]string{},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// cd into the worktree to simulate the subagent's CWD.
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if !result.Allowed {
		t.Fatalf("expected ALLOWED (harness-managed worktree), got blocked: %s", result.Reason)
	}
}

func TestCheckCWDDrift_DispatcherModeOff(t *testing.T) {
	// CWD inside worktree but dispatcher mode is OFF.
	// Should be ALLOWED (not running Paivot).

	root := t.TempDir()
	worktreeDir := filepath.Join(root, ".claude", "worktrees", "dev-TEST-001")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No dispatcher state file -- mode is off.

	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if !result.Allowed {
		t.Fatalf("expected allowed (dispatcher mode off), got blocked: %s", result.Reason)
	}
}

// TestCheckCWDDrift_HarnessWorktreeExclusion is a regression test for the
// bug where pvg guard blocked every Bash invocation made by a subagent whose
// CWD was a Claude Code harness-managed worktree (.claude/worktrees/agent-*/).
// The dispatcher state belongs to a Paivot agent at the project root; the
// harness independently spawned an isolated worktree for an unrelated
// subagent. The drift check must not punish the subagent for the harness's
// isolation directory.
func TestCheckCWDDrift_HarnessWorktreeExclusion(t *testing.T) {
	root := t.TempDir()

	// The harness creates its own worktree under .claude/worktrees/agent-<id>/.
	harnessWorktree := filepath.Join(root, ".claude", "worktrees", "agent-a513315f422bca98e")
	if err := os.MkdirAll(harnessWorktree, 0o755); err != nil {
		t.Fatal(err)
	}

	// Dispatcher tracks an UNRELATED Paivot PM agent at the project root
	// (this is exactly what dispatcher.TrackAgent does -- it stores the
	// orchestrator's CWD, not the harness worktree).
	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedRoot := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = resolved
	}
	state := map[string]interface{}{
		"enabled":         true,
		"since":           "2026-04-28T17:10:57Z",
		"active_agents":   map[string]string{"pm-001": "paivot-graph:pm"},
		"agent_worktrees": map[string]string{"pm-001": resolvedRoot},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// cd into the harness worktree -- this is what os.Getwd() returns when
	// pvg guard runs as a PreToolUse hook in the harness-spawned subagent.
	origDir, _ := os.Getwd()
	if err := os.Chdir(harnessWorktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if !result.Allowed {
		t.Fatalf("expected ALLOWED (harness-managed worktree, not tracked by Paivot), got blocked: %s", result.Reason)
	}
}

// TestCheckCWDDrift_TrackedWorktreeStillBlocks confirms the original drift
// detection still works for worktrees Paivot itself tracks (e.g., a developer
// agent worktree that Paivot's own dispatcher created and registered).
func TestCheckCWDDrift_TrackedWorktreeStillBlocks(t *testing.T) {
	root := t.TempDir()
	trackedWorktree := filepath.Join(root, ".claude", "worktrees", "dev-PROJ-a1b")
	if err := os.MkdirAll(trackedWorktree, 0o755); err != nil {
		t.Fatal(err)
	}

	resolvedWT := trackedWorktree
	if resolved, err := filepath.EvalSymlinks(trackedWorktree); err == nil {
		resolvedWT = resolved
	}

	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{
		"enabled":         true,
		"since":           "2026-01-01T00:00:00Z",
		"active_agents":   map[string]string{"agent-1": "paivot-graph:developer"},
		"agent_worktrees": map[string]string{"agent-1": resolvedWT},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(trackedWorktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if result.Allowed {
		t.Fatal("expected BLOCKED for Paivot-tracked worktree CWD, got allowed")
	}
	if result.Reason == "" {
		t.Fatal("expected a reason message")
	}
}

// TestCheckCWDDrift_HarnessWorktreeAllowsVaultBash exercises the user's
// reported scenario end-to-end: a Bash command is invoked from the harness
// worktree and references the project's .vault/ path (e.g., reading a story
// file). The drift check must allow the Bash so subsequent checks (which are
// path-aware) can decide on their own whether the specific operation touches
// protected vault content.
func TestCheckCWDDrift_HarnessWorktreeAllowsVaultBash(t *testing.T) {
	root := t.TempDir()
	harnessWorktree := filepath.Join(root, ".claude", "worktrees", "agent-feedfacecafe1234")
	if err := os.MkdirAll(harnessWorktree, 0o755); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{
		"enabled":         true,
		"since":           "2026-04-28T17:10:57Z",
		"active_agents":   map[string]string{},
		"agent_worktrees": map[string]string{},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(harnessWorktree); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result := CheckCWDDrift(root)
	if !result.Allowed {
		t.Fatalf("expected ALLOWED for any Bash from harness worktree CWD, got blocked: %s", result.Reason)
	}
}
