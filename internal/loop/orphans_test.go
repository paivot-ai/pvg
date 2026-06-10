package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: in_progress stories from a dead session (no worktree, not
// delivered) shadowed real state forever -- the loop counted them as active
// work and never re-dispatched them.
func TestReconcileOrphans_ResetsDeadClaimsOnly(t *testing.T) {
	projectRoot := t.TempDir()

	override := filepath.Join(t.TempDir(), "shared-vault")
	t.Setenv("ND_VAULT_DIR", override)

	// PROJ-live has a developer worktree on disk; PROJ-pm is delivered
	// (awaiting PM, legitimately no worktree); PROJ-dead has neither.
	liveWorktree := filepath.Join(projectRoot, ".claude", "worktrees", "dev-PROJ-live")
	if err := os.MkdirAll(liveWorktree, 0o755); err != nil {
		t.Fatal(err)
	}

	var mutations [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "list --status in_progress") {
			return exec.Command("echo", `[
				{"ID":"PROJ-live","Status":"in_progress","Labels":[]},
				{"ID":"PROJ-pm","Status":"in_progress","Labels":["delivered"]},
				{"ID":"PROJ-dead","Status":"in_progress","Labels":[]}
			]`)
		}
		mutations = append(mutations, append([]string{name}, args...))
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = oldExec })

	resets, err := ReconcileOrphans(projectRoot)
	if err != nil {
		t.Fatalf("ReconcileOrphans() error: %v", err)
	}
	if len(resets) != 1 || resets[0].StoryID != "PROJ-dead" {
		t.Fatalf("expected only PROJ-dead reset, got %#v", resets)
	}

	var resetCmds []string
	for _, m := range mutations {
		joined := strings.Join(m, " ")
		if strings.Contains(joined, "--status open") {
			resetCmds = append(resetCmds, joined)
		}
	}
	if len(resetCmds) != 1 || !strings.Contains(resetCmds[0], "PROJ-dead") {
		t.Fatalf("expected exactly one reset for PROJ-dead, got %v", resetCmds)
	}
	for _, m := range mutations {
		joined := strings.Join(m, " ")
		if strings.Contains(joined, "PROJ-live") || strings.Contains(joined, "PROJ-pm") {
			t.Fatalf("must not mutate live or delivered stories: %v", joined)
		}
	}
}
