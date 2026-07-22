package guard

import (
	"regexp"
	"strings"
)

// worktreeCdRe matches cd commands that navigate into .claude/worktrees/.
// Captures explicit cd, pushd, or chained cd after && or ;.
// Does NOT match commands that merely reference the path without cd'ing
// (e.g., git worktree add, pvg worktree remove, ls .claude/worktrees/).
var worktreeCdRe = regexp.MustCompile(
	`(?:^|[;&|]\s*)(?:cd|pushd)\s+[^\s;|&]*\.claude/worktrees/`,
)

const worktreeCdBlockMsg = "BLOCKED: Dispatcher must never cd into a worktree directory.\n" +
	"CWD inside a worktree becomes invalid when the worktree is removed,\n" +
	"permanently breaking all Bash commands for the rest of the session.\n\n" +
	"Instead:\n" +
	"  - Spawn an Agent to work inside the worktree\n" +
	"  - Use pvg/nd/git commands that accept paths as arguments\n" +
	"  - Use `git -C <worktree-path>` to run git commands from outside"

// CheckWorktreeCd blocks Bash commands that would cd into .claude/worktrees/.
// Only active when dispatcher mode is enabled (Paivot is running). In normal
// Claude Code sessions, worktrees are legitimate (e.g., Agent isolation).
func CheckWorktreeCd(projectRoot, command string) Result {
	if command == "" || projectRoot == "" {
		return Result{Allowed: true}
	}

	// Only enforce while an execution session is live (dispatcher mode OR an
	// active loop) -- never on the settings file alone. The loop dispatcher
	// must not cd into worktrees either, so the loop leg stays.
	if !executionActive(projectRoot) {
		return Result{Allowed: true}
	}

	// Quick negative: skip regex if the command doesn't even mention worktrees.
	if !strings.Contains(command, ".claude/worktrees/") {
		return Result{Allowed: true}
	}

	if worktreeCdRe.MatchString(command) {
		return Result{Allowed: false, Reason: worktreeCdBlockMsg}
	}

	return Result{Allowed: true}
}
