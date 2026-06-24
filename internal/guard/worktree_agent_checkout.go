package guard

import (
	"regexp"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

var worktreeAgentCheckoutRe = regexp.MustCompile(
	`(?:^|[;&|]\s*)(?:\S*/)?git\s+(?:checkout|switch)\s+(?:--detach\s+)?(?:refs/heads/|origin/)?worktree-agent-[\w.-]+`,
)

const worktreeAgentCheckoutBlockMsg = "BLOCKED: Dispatcher mode is active and this command would check out a worktree-agent-* branch on the shared HEAD.\n\n" +
	"worktree-agent-* branches are Claude Code isolation branches. Checking one out in the parent repository can reset another Paivot session's HEAD and hide in-flight edits.\n\n" +
	"Use dispatcher-managed worktrees instead:\n" +
	"  pvg worktree add .claude/worktrees/dev-<STORY_ID> story/<STORY_ID>  # stamps the ownership marker\n\n" +
	"If you are cleaning stale isolation branches, delete them with git branch -D or git push --delete; do not check them out."

// CheckWorktreeAgentCheckout blocks shared-HEAD checkout/switch operations to
// Claude Code's auto-generated worktree-agent-* branches while dispatcher mode
// is active. This turns cross-session interference into an observable warning
// instead of silently moving the parent repo's HEAD.
func CheckWorktreeAgentCheckout(projectRoot, command string) Result {
	if projectRoot == "" || command == "" {
		return Result{Allowed: true}
	}

	state, _, err := dispatcher.ReadStateRoot(projectRoot)
	if err != nil || !state.Enabled {
		return Result{Allowed: true}
	}

	if !strings.Contains(command, "worktree-agent-") {
		return Result{Allowed: true}
	}

	if worktreeAgentCheckoutRe.MatchString(command) {
		return Result{Allowed: false, Reason: worktreeAgentCheckoutBlockMsg}
	}

	return Result{Allowed: true}
}
