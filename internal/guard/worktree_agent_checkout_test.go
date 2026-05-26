package guard

import (
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

func TestCheckWorktreeAgentCheckout_BlocksWhenDispatcherActive(t *testing.T) {
	root := t.TempDir()
	if err := dispatcher.On(root); err != nil {
		t.Fatalf("dispatcher on: %v", err)
	}

	commands := []string{
		"git checkout worktree-agent-a455031",
		"git switch worktree-agent-adbcf90",
		"git checkout origin/worktree-agent-acaf778",
		"git checkout refs/heads/worktree-agent-abc123",
		"git status && git checkout worktree-agent-cross-session",
	}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			result := CheckWorktreeAgentCheckout(root, command)
			if result.Allowed {
				t.Fatalf("expected command to be blocked: %s", command)
			}
			if !strings.Contains(result.Reason, "shared HEAD") {
				t.Fatalf("expected observable shared HEAD warning, got: %s", result.Reason)
			}
		})
	}
}

func TestCheckBlocksWorktreeAgentCheckoutThroughBashGuard(t *testing.T) {
	root := t.TempDir()
	if err := dispatcher.On(root); err != nil {
		t.Fatalf("dispatcher on: %v", err)
	}

	result := Check("", root, HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: "git checkout worktree-agent-a455031"},
	})

	if result.Allowed {
		t.Fatal("expected Bash guard to block worktree-agent checkout")
	}
	if !strings.Contains(result.Reason, "shared HEAD") {
		t.Fatalf("expected observable shared HEAD warning, got: %s", result.Reason)
	}
}

func TestCheckWorktreeAgentCheckout_AllowsCleanupAndWorktreeAdd(t *testing.T) {
	root := t.TempDir()
	if err := dispatcher.On(root); err != nil {
		t.Fatalf("dispatcher on: %v", err)
	}

	commands := []string{
		"git branch -D worktree-agent-a455031",
		"git push origin --delete worktree-agent-a455031",
		"git worktree add .claude/worktrees/dev-HXT-mstp story/HXT-mstp",
		"git checkout story/HXT-mstp",
	}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			result := CheckWorktreeAgentCheckout(root, command)
			if !result.Allowed {
				t.Fatalf("expected command to be allowed, blocked: %s", result.Reason)
			}
		})
	}
}

func TestCheckWorktreeAgentCheckout_InactiveDispatcherAllows(t *testing.T) {
	result := CheckWorktreeAgentCheckout(
		t.TempDir(),
		"git checkout worktree-agent-a455031",
	)
	if !result.Allowed {
		t.Fatalf("expected inactive dispatcher to allow command, blocked: %s", result.Reason)
	}
}
