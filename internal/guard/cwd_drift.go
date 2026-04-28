package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/worktree"
)

// CheckCWDDrift detects when the dispatcher's shell CWD has silently drifted
// into a worktree after a developer or PM subagent completed. This catches the
// session-fatal failure mode where the dispatcher removes a worktree while CWD
// is inside it, permanently breaking all subsequent Bash commands.
//
// The check fires when ALL of the following are met:
//   - CWD is inside .claude/worktrees/
//   - Dispatcher mode is active (Paivot is running)
//   - The CWD worktree is one Paivot itself spawned (tracked in
//     dispatcher state's AgentWorktrees map)
//
// Developer subagent Bash commands run in the DEVELOPER'S own session, not the
// dispatcher's. The dispatcher never needs CWD inside a worktree. Any CWD drift
// into a tracked worktree while dispatcher mode is on indicates a completed
// subagent whose CWD persisted in the dispatcher shell.
//
// Harness-managed worktrees (created by Claude Code's Agent tool with
// isolation: "worktree") are NOT tracked in dispatcher state -- the harness
// spawns them outside Paivot's knowledge. When a subagent runs Bash from
// inside its harness-assigned worktree, the PreToolUse hook fires in the
// agent's own session with CWD inside .claude/worktrees/agent-<id>/. Blocking
// these would prevent the harness's parallel execution model from working at
// all. We detect them by their absence from AgentWorktrees and allow them.
func CheckCWDDrift(projectRoot string) Result {
	cwd, err := os.Getwd()
	if err != nil {
		return Result{Allowed: true}
	}

	// Quick negative: not inside any worktree directory.
	if !strings.Contains(cwd, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
		return Result{Allowed: true}
	}

	// Resolve the main repo root from the worktree path.
	mainRoot, resolveErr := worktree.ResolveProjectRoot(cwd)
	if resolveErr != nil {
		// Can't resolve main repo -- fall back to projectRoot from guard input.
		mainRoot = projectRoot
	}

	// Check if dispatcher mode is active.
	state, _, stateErr := dispatcher.ReadStateRoot(mainRoot)
	if stateErr != nil || !state.Enabled {
		return Result{Allowed: true}
	}

	// Harness-managed worktree exemption: if CWD is inside a worktree that
	// Paivot did NOT spawn (i.e., not registered in AgentWorktrees), it was
	// created by the Claude Code harness for an isolated subagent. The hook
	// is firing in that subagent's own session, not the dispatcher's. Allow.
	if !isTrackedWorktreeCWD(state, cwd) {
		return Result{Allowed: true}
	}

	// CWD is inside a worktree Paivot tracks and dispatcher mode is on.
	// This means CWD drifted from a completed Paivot-spawned subagent.
	return Result{
		Allowed: false,
		Reason: fmt.Sprintf(
			"BLOCKED: Shell CWD has drifted into a worktree directory.\n"+
				"Current CWD: %s\n\n"+
				"This happens when a developer/PM agent completes -- their CWD\n"+
				"persists in the dispatcher's shell. Reset IMMEDIATELY:\n"+
				"  cd %s\n\n"+
				"Then verify with: pwd",
			cwd, mainRoot),
	}
}

// isTrackedWorktreeCWD reports whether cwd is inside any worktree path that
// Paivot's dispatcher itself tracks in AgentWorktrees. Worktrees that match
// belong to Paivot-spawned subagents; worktrees outside this set were created
// by the Claude Code harness (Agent isolation) and must not be policed here.
func isTrackedWorktreeCWD(state *dispatcher.State, cwd string) bool {
	if state == nil || len(state.AgentWorktrees) == 0 {
		return false
	}
	cleanCWD := filepath.Clean(cwd)
	if resolved, err := filepath.EvalSymlinks(cleanCWD); err == nil {
		cleanCWD = resolved
	}
	for _, wt := range state.AgentWorktrees {
		cleanWT := filepath.Clean(wt)
		if cleanWT == "." || cleanWT == "" {
			continue
		}
		// Skip entries that aren't worktree paths (e.g., the project root
		// itself stored as the orchestrator's CWD by SubagentStart).
		if !strings.Contains(cleanWT, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(cleanWT); err == nil {
			cleanWT = resolved
		}
		if cleanCWD == cleanWT || strings.HasPrefix(cleanCWD, cleanWT+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
