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
// isolation: "worktree") follow the naming convention
// .claude/worktrees/agent-<hash>/. The PreToolUse hook always fires inside
// the subagent's own session, never the dispatcher's, so blocking would
// strand legitimate agent Bash (git commit, mix test, npm run, etc.).
// We unconditionally allow these regardless of dispatcher tracking state,
// because SubagentStart can race-register a harness worktree into
// AgentWorktrees when the hook fires from the subagent's CWD.
func CheckCWDDrift(projectRoot string) Result {
	cwd, err := os.Getwd()
	if err != nil {
		return Result{Allowed: true}
	}

	// Quick negative: not inside any worktree directory.
	if !strings.Contains(cwd, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
		return Result{Allowed: true}
	}

	// Harness-managed worktree exemption: Claude Code's Agent tool isolates
	// subagents in .claude/worktrees/agent-<hash>/. Hooks fired from such a
	// path run in the subagent's own session -- never the dispatcher's --
	// so the drift hazard does not apply. Allow unconditionally.
	if isHarnessWorktreeCWD(cwd) {
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

// harnessWorktreePrefix is the name prefix the Claude Code Agent tool uses
// for worktrees it creates with isolation: "worktree". A subagent's CWD
// inside such a worktree is always the subagent's own session.
const harnessWorktreePrefix = "agent-"

// isHarnessWorktreeCWD reports whether cwd lies inside a worktree the Claude
// Code harness created for an isolated subagent. The harness names these
// .claude/worktrees/agent-<hash>/. Returns true for any path at or under
// such a worktree.
func isHarnessWorktreeCWD(cwd string) bool {
	sep := string(filepath.Separator)
	marker := sep + ".claude" + sep + "worktrees" + sep
	idx := strings.Index(cwd, marker)
	if idx < 0 {
		return false
	}
	rest := cwd[idx+len(marker):]
	if i := strings.Index(rest, sep); i >= 0 {
		rest = rest[:i]
	}
	return strings.HasPrefix(rest, harnessWorktreePrefix)
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
