package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func setupStoryCheckoutFixture(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	// Real main checkout: .git is a DIRECTORY.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{"enabled": true, "since": "2026-01-01T00:00:00Z"}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// Regression: an isolated PM whose harness CWD was reset to the project root
// ran `git checkout story/<id>` there, silently moving the dispatcher's HEAD
// off main.
func TestCheckStoryCheckoutAtRoot_BlocksAtMainCheckout(t *testing.T) {
	root := setupStoryCheckoutFixture(t)
	chdir(t, root)

	for _, cmd := range []string{
		"git checkout story/PROJ-a1b2",
		"git switch story/PROJ-a1b2",
		"git checkout -b story/PROJ-new origin/epic/PROJ-epic1",
		"cd somewhere; git checkout story/PROJ-a1b2",
	} {
		if r := CheckStoryCheckoutAtRoot(root, cmd); r.Allowed {
			t.Fatalf("expected block at main checkout for: %s", cmd)
		}
	}
}

func TestCheckStoryCheckoutAtRoot_AllowsLegitimateForms(t *testing.T) {
	root := setupStoryCheckoutFixture(t)
	chdir(t, root)

	for _, cmd := range []string{
		"git branch story/PROJ-a1b2 origin/epic/PROJ-epic1", // non-switching creation
		"git checkout epic/PROJ-epic1",                      // dispatcher merge step
		"git checkout main",
		"git -C .claude/worktrees/dev-PROJ-a1b2 checkout story/PROJ-a1b2", // targets the worktree HEAD
		"git worktree add .claude/worktrees/dev-PROJ-a1b2 story/PROJ-a1b2",
		"git push origin story/PROJ-a1b2",
		"git log origin/story/PROJ-a1b2 --oneline",
	} {
		if r := CheckStoryCheckoutAtRoot(root, cmd); !r.Allowed {
			t.Fatalf("expected allow for: %s (got: %s)", cmd, r.Reason)
		}
	}
}

func TestCheckStoryCheckoutAtRoot_AllowsInsideWorktrees(t *testing.T) {
	root := setupStoryCheckoutFixture(t)

	// Linked worktree outside the repo dir: .git is a FILE.
	linked := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPtr := "gitdir: " + filepath.Join(root, ".git", "worktrees", "wt") + "\n"
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte(gitPtr), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, linked)
	if r := CheckStoryCheckoutAtRoot(root, "git checkout story/PROJ-a1b2"); !r.Allowed {
		t.Fatalf("expected allow inside linked worktree, got: %s", r.Reason)
	}

	// Dispatcher-managed worktree under .claude/worktrees/.
	nested := filepath.Join(root, ".claude", "worktrees", "dev-PROJ-a1b2")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, nested)
	if r := CheckStoryCheckoutAtRoot(root, "git checkout story/PROJ-a1b2"); !r.Allowed {
		t.Fatalf("expected allow inside dispatcher worktree, got: %s", r.Reason)
	}
}

func TestCheckStoryCheckoutAtRoot_AllowsWhenDispatcherOff(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, root)
	if r := CheckStoryCheckoutAtRoot(root, "git checkout story/PROJ-a1b2"); !r.Allowed {
		t.Fatalf("expected allow without dispatcher mode, got: %s", r.Reason)
	}
}
