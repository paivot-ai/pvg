package guard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paivot-ai/pvg/internal/loop"
)

// writeActiveLoopState writes an active piv-loop state file under root with
// NO dispatcher state -- the posture of a loop state left by an older
// `pvg loop setup` that did not enable dispatcher mode.
func writeActiveLoopState(t *testing.T, root string) {
	t.Helper()
	if err := loop.WriteState(root, loop.NewState("epic", "PROJ-epic", 50)); err != nil {
		t.Fatal(err)
	}
}

// writeSettingsFileOnly stamps root as Paivot-managed (settings file present)
// WITHOUT any loop or dispatcher state -- the posture of a plain maintenance
// session on a Paivot-managed repo.
func writeSettingsFileOnly(t *testing.T, root string) {
	t.Helper()
	settingsPath := filepath.Join(root, ".vault", "knowledge", ".settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("stack_detection: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionActive_LoopStateOnly(t *testing.T) {
	root := t.TempDir()
	if executionActive(root) {
		t.Fatal("expected inactive with no loop state and no dispatcher state")
	}
	writeActiveLoopState(t, root)
	if !executionActive(root) {
		t.Fatal("expected active with a live loop state file and no dispatcher state")
	}
}

func TestExecutionActive_SettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)
	if executionActive(root) {
		t.Fatal("expected inactive: the settings file alone is not a live execution session")
	}
}

func TestCoordinationActive_SettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)
	// The stronger predicate (merge gate, label contract, FSM) still treats
	// the settings file as Paivot-managed even between sessions.
	if !coordinationActive(root) {
		t.Fatal("expected coordinationActive for a settings-managed project")
	}
}

// Regression suite, loop leg: every behavioral guard must fire with an ACTIVE
// loop state file and NO dispatcher state. `pvg loop setup` now enables
// dispatcher mode itself, but loop states written by older setups (or
// hand-recovered states) must still be policed.

func TestCheckBackgroundBash_BlocksWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	input := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "mix test", RunInBackground: true}}
	if r := CheckBackgroundBash(root, input); r.Allowed {
		t.Fatal("expected backgrounded Bash blocked with active loop and no dispatcher state")
	}
}

func TestCheckStoryCheckoutAtRoot_BlocksWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)
	// Main-checkout marker: a .git DIRECTORY at root.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	r := CheckStoryCheckoutAtRoot(root, "git checkout story/PROJ-a1b")
	if r.Allowed {
		t.Fatal("expected story checkout at root blocked with active loop and no dispatcher state")
	}
}

func TestCheckWorktreeCd_BlocksWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	r := CheckWorktreeCd(root, "cd .claude/worktrees/dev-PROJ-a1b && mix test")
	if r.Allowed {
		t.Fatal("expected worktree cd blocked with active loop and no dispatcher state")
	}
}

func TestCheckWorktreeAgentCheckout_BlocksWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	r := CheckWorktreeAgentCheckout(root, "git checkout worktree-agent-deadbeef")
	if r.Allowed {
		t.Fatal("expected worktree-agent checkout blocked with active loop and no dispatcher state")
	}
}

func TestCheckCWDDrift_BlocksWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	worktreeDir := filepath.Join(root, ".claude", "worktrees", "dev-PROJ-a1b")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	r := CheckCWDDrift(root)
	if r.Allowed {
		t.Fatal("expected CWD drift blocked with active loop and no dispatcher state")
	}

	// Harness worktrees (agent-*) stay exempt even in loop-only mode.
	harnessDir := filepath.Join(root, ".claude", "worktrees", "agent-deadbeef")
	if err := os.MkdirAll(harnessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(harnessDir); err != nil {
		t.Fatal(err)
	}
	if r := CheckCWDDrift(root); !r.Allowed {
		t.Fatalf("expected harness worktree CWD allowed in loop-only mode, got: %s", r.Reason)
	}
}

// Regression suite, settings leg: NONE of the behavioral guards may fire on a
// Paivot-managed repo with no live session. The settings file marks the repo
// as Paivot-managed for the acceptance-contract checks (merge gate, label
// contract), but a maintenance session there must keep full shell freedom --
// backgrounded Bash included.

func TestCheckBackgroundBash_AllowsWithSettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)

	input := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "mix test", RunInBackground: true}}
	if r := CheckBackgroundBash(root, input); !r.Allowed {
		t.Fatalf("expected backgrounded Bash allowed with only a settings file, got: %s", r.Reason)
	}
}

func TestCheckStoryCheckoutAtRoot_AllowsWithSettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	r := CheckStoryCheckoutAtRoot(root, "git checkout story/PROJ-a1b")
	if !r.Allowed {
		t.Fatalf("expected story checkout allowed with only a settings file, got: %s", r.Reason)
	}
}

func TestCheckWorktreeCd_AllowsWithSettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)

	r := CheckWorktreeCd(root, "cd .claude/worktrees/dev-PROJ-a1b && mix test")
	if !r.Allowed {
		t.Fatalf("expected worktree cd allowed with only a settings file, got: %s", r.Reason)
	}
}

func TestCheckWorktreeAgentCheckout_AllowsWithSettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)

	r := CheckWorktreeAgentCheckout(root, "git checkout worktree-agent-deadbeef")
	if !r.Allowed {
		t.Fatalf("expected worktree-agent checkout allowed with only a settings file, got: %s", r.Reason)
	}
}

func TestCheckCWDDrift_AllowsWithSettingsFileOnly(t *testing.T) {
	root := t.TempDir()
	writeSettingsFileOnly(t, root)

	worktreeDir := filepath.Join(root, ".claude", "worktrees", "dev-PROJ-a1b")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if r := CheckCWDDrift(root); !r.Allowed {
		t.Fatalf("expected worktree CWD allowed with only a settings file, got: %s", r.Reason)
	}
}

// CheckDispatcher gates on dispatcher state alone: without it there is no
// agent tracking, so enforcement would block legitimate subagent mutations
// (an Sr PM spawned by /intake could not run `pvg issues create`). Loop runs
// are covered because `pvg loop setup` enables dispatcher mode itself.

func TestCheckDispatcher_SilentWithActiveLoopOnly(t *testing.T) {
	root := t.TempDir()
	writeActiveLoopState(t, root)

	dfWrite := HookInput{ToolName: "Write", ToolInput: ToolInput{FilePath: filepath.Join(root, "BUSINESS.md")}}
	if r := CheckDispatcher(root, dfWrite); !r.Allowed {
		t.Fatalf("expected D&F write allowed without dispatcher state, got: %s", r.Reason)
	}

	ndMutation := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "nd update PROJ-a1b --status closed"}}
	if r := CheckDispatcher(root, ndMutation); !r.Allowed {
		t.Fatalf("expected nd mutation allowed without dispatcher state, got: %s", r.Reason)
	}
}
