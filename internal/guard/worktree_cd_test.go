package guard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

// setupDispatcherForWorktree creates a temp dir with dispatcher mode enabled.
func setupDispatcherForWorktree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	knowledgeDir := filepath.Join(root, ".vault", "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.On(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCheckWorktreeCd_BlocksDirectCd(t *testing.T) {
	root := setupDispatcherForWorktree(t)

	cases := []struct {
		name    string
		command string
	}{
		{"bare cd", "cd .claude/worktrees/dev-PRA-36tu"},
		{"cd with absolute path", "cd /Users/ramirosalas/workspace/praktical/.claude/worktrees/dev-PRA-36tu"},
		{"pushd", "pushd .claude/worktrees/dev-PRA-36tu"},
		{"chained after &&", "echo hello && cd .claude/worktrees/dev-PRA-36tu"},
		{"chained after ;", "echo hello; cd .claude/worktrees/dev-PRA-36tu"},
		{"cd with trailing command", "cd .claude/worktrees/dev-X && ls"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := CheckWorktreeCd(root, tc.command)
			if r.Allowed {
				t.Errorf("expected BLOCKED for %q", tc.command)
			}
		})
	}
}

func TestCheckWorktreeCd_AllowsLegitimateCommands(t *testing.T) {
	root := setupDispatcherForWorktree(t)

	cases := []struct {
		name    string
		command string
	}{
		{"empty", ""},
		{"no worktree ref", "cd /Users/ramirosalas/workspace/praktical"},
		{"git worktree add", "git worktree add .claude/worktrees/dev-PRA-36tu story/PRA-36tu"},
		{"pvg worktree add", "pvg worktree add .claude/worktrees/dev-PRA-36tu story/PRA-36tu"},
		{"pvg worktree remove", "pvg worktree remove .claude/worktrees/dev-PRA-36tu"},
		{"ls worktree dir", "ls .claude/worktrees/dev-PRA-36tu"},
		{"git -C worktree", "git -C .claude/worktrees/dev-PRA-36tu log --oneline"},
		{"cat file in worktree", "cat .claude/worktrees/dev-PRA-36tu/mix.exs"},
		{"grep in worktree", "grep -r 'pattern' .claude/worktrees/dev-PRA-36tu/"},
		{"cd to project root with worktree remove", "cd /project/root && pvg worktree remove .claude/worktrees/dev-X"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := CheckWorktreeCd(root, tc.command)
			if !r.Allowed {
				t.Errorf("expected ALLOWED for %q, got blocked: %s", tc.command, r.Reason)
			}
		})
	}
}

func TestCheckWorktreeCd_AllowsWhenDispatcherOff(t *testing.T) {
	// No dispatcher state -- simulates normal Claude Code session.
	root := t.TempDir()

	r := CheckWorktreeCd(root, "cd .claude/worktrees/dev-PRA-36tu")
	if !r.Allowed {
		t.Errorf("expected ALLOWED when dispatcher is off, got blocked: %s", r.Reason)
	}
}

func TestCheckWorktreeCd_AllowsWithEmptyProjectRoot(t *testing.T) {
	r := CheckWorktreeCd("", "cd .claude/worktrees/dev-PRA-36tu")
	if !r.Allowed {
		t.Error("expected ALLOWED with empty project root")
	}
}
