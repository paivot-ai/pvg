package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

// Matches checkout/switch of a story branch, including the branch-creating
// forms (-b/-B/-c/-C): creating a story branch with checkout at the root
// moves HEAD just the same. Non-switching creation is `git branch story/...`.
var storyCheckoutRe = regexp.MustCompile(
	`(?:^|[;&|]\s*)(?:\S*/)?git\s+(?:checkout|switch)\s+(?:--detach\s+)?(?:-[bBcC]\s+)?(?:refs/heads/|origin/)?story/[\w.-]+`,
)

var gitDashCDirRe = regexp.MustCompile(`(?:^|[;&|]\s*)(?:\S*/)?git\s+-C\s+\S+`)

const storyCheckoutBlockMsg = "BLOCKED: Dispatcher mode is active and this command would check out a story/* branch on the MAIN checkout.\n\n" +
	"Story branches belong in dispatcher-managed worktrees -- checking one out at the project root silently moves the dispatcher's HEAD off main. " +
	"Isolated agents hit this when the harness resets their CWD to the project root between tool calls: ALWAYS prefix shell commands with `cd <your-worktree>`.\n\n" +
	"Correct patterns:\n" +
	"  git branch story/<STORY_ID> origin/epic/<EPIC_ID>     # create WITHOUT switching HEAD\n" +
	"  pvg worktree add .claude/worktrees/dev-<STORY_ID> story/<STORY_ID>  # stamps the ownership marker\n" +
	"  cd .claude/worktrees/dev-<STORY_ID>                    # work happens here\n\n" +
	"If you are an isolated PM/developer agent: cd into your worktree first, then check out the story branch there."

// CheckStoryCheckoutAtRoot blocks git checkout/switch of story/* branches on
// the main checkout while dispatcher mode is active. Observed failure: a PM
// spawned with worktree isolation had its harness CWD reset to the project
// root between tool calls, ran `git checkout story/<id>` there, and silently
// moved the dispatcher's HEAD off main (then joined the root's compose
// project, colliding with other agents' builds).
func CheckStoryCheckoutAtRoot(projectRoot, command string) Result {
	if projectRoot == "" || command == "" {
		return Result{Allowed: true}
	}
	if !strings.Contains(command, "story/") {
		return Result{Allowed: true}
	}

	state, _, err := dispatcher.ReadStateRoot(projectRoot)
	if err != nil || !state.Enabled {
		return Result{Allowed: true}
	}

	// `git -C <path>` operates on another checkout's HEAD; the main HEAD is
	// not at risk and worktree-internal automation may legitimately use it.
	if gitDashCDirRe.MatchString(command) {
		return Result{Allowed: true}
	}

	if !storyCheckoutRe.MatchString(command) {
		return Result{Allowed: true}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Result{Allowed: true}
	}
	if !isMainCheckoutCWD(cwd) {
		return Result{Allowed: true}
	}

	return Result{Allowed: false, Reason: storyCheckoutBlockMsg}
}

// isMainCheckoutCWD reports whether cwd is inside the MAIN checkout: the
// nearest .git up the tree is a directory. Linked worktrees (and Claude's
// .claude/worktrees/* isolation dirs) have a .git FILE pointing at the
// common dir, so story checkouts there only move the worktree's own HEAD.
func isMainCheckoutCWD(cwd string) bool {
	if strings.Contains(cwd, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
		return false
	}
	dir := filepath.Clean(cwd)
	for {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			return info.IsDir()
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
