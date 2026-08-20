package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
)

// setupMachineryProject builds a dispatcher-mode project whose design tree
// is real and whose design.machinery setting is on.
func setupMachineryProject(t *testing.T) (root, worktree string) {
	t.Helper()
	root, worktree = setupDispatcher(t)
	designDir := filepath.Join(root, "design")
	for _, sub := range []string{"machines", "formal"} {
		if err := os.MkdirAll(filepath.Join(designDir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(designDir, "domain.modelith.yaml"), "model: {}\n")
	write(filepath.Join(designDir, "BUILD.md"), "# BUILD\n")
	write(filepath.Join(designDir, "ARCHITECTURE.md"), "# Architecture Contract\n")
	write(filepath.Join(designDir, "machines", "Deal.oracle.md"), "# Generated transition oracle: Deal\n")
	write(filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), "design.machinery: on\n")
	return root, worktree
}

func designWrite(cwd, target string) Result {
	return CheckDispatcher(cwd, HookInput{ToolName: "Write", ToolInput: ToolInput{FilePath: target}})
}

// TestDesignTreeIsReadOnlyForDeliveryAgents is the G10 contract: on a
// machinery-first project the design SOURCES (not just the generated
// oracles the machinery hook already protects) are closed to the Sr PM, the
// developer, the PM, and the Anchor.
func TestDesignTreeIsReadOnlyForDeliveryAgents(t *testing.T) {
	targets := []string{
		"design/BUILD.md",
		"design/ARCHITECTURE.md",
		"design/domain.modelith.yaml",
		"design/machines/Deal.machine.json",
		"design/machines/Deal.oracle.md",
		"design/formal/Policy.oracle.md",
		"design/workspace.dsl",
	}
	for _, agent := range []string{"paivot-graph:sr-pm", "paivot-graph:developer", "paivot-graph:pm", "paivot-graph:anchor"} {
		for _, target := range targets {
			t.Run(agent+" "+target, func(t *testing.T) {
				root, worktree := setupMachineryProject(t)
				if err := dispatcher.TrackAgent(worktree, "agent-1", agent); err != nil {
					t.Fatal(err)
				}
				result := designWrite(worktree, filepath.Join(root, target))
				if result.Allowed {
					t.Fatalf("%s must not write %s", agent, target)
				}
				if !strings.Contains(result.Reason, "read-only") || !strings.Contains(result.Reason, "DESIGN DEFECT") {
					t.Errorf("block message must explain the rule: %s", result.Reason)
				}
			})
		}
	}
}

// TestDesignTreeArchitectIsTheOnlyWriter: the Architect path stays open, so
// projects that DO use the Paivot Architect keep working (backward
// compatibility). On a machinery-first project the Architect is simply
// never spawned.
func TestDesignTreeArchitectIsTheOnlyWriter(t *testing.T) {
	root, worktree := setupMachineryProject(t)
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:architect"); err != nil {
		t.Fatal(err)
	}
	if result := designWrite(worktree, filepath.Join(root, "design", "ARCHITECTURE.md")); !result.Allowed {
		t.Fatalf("the architect owns the design tree: %s", result.Reason)
	}
}

// TestDesignTreeCoordinatorReadsAlwaysAllowed: the guard is a WRITE guard.
// Reading the design is how the coordinator and every agent get their
// context, and must never be blocked.
func TestDesignTreeCoordinatorReadsAlwaysAllowed(t *testing.T) {
	root, worktree := setupMachineryProject(t)
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{root, worktree} {
		for _, input := range []HookInput{
			{ToolName: "Read", ToolInput: ToolInput{FilePath: filepath.Join(root, "design", "BUILD.md")}},
			{ToolName: "Grep", ToolInput: ToolInput{FilePath: filepath.Join(root, "design", "machines", "Deal.oracle.md")}},
			{ToolName: "Bash", ToolInput: ToolInput{Command: "cat design/formal/Policy.oracle.md"}},
			{ToolName: "Bash", ToolInput: ToolInput{Command: "grep -rn DEAL- design/machines/"}},
		} {
			if result := CheckDispatcher(cwd, input); !result.Allowed {
				t.Errorf("reads must stay open (%s from %s): %s", input.ToolName, cwd, result.Reason)
			}
		}
	}
}

// TestDesignTreeCoordinatorBlockedOnlyDuringALoop: outside a loop the
// coordinator is the user's own session (a design revision is legitimate);
// while an execution loop runs, the design is frozen.
func TestDesignTreeCoordinatorBlockedOnlyDuringALoop(t *testing.T) {
	root, _ := setupMachineryProject(t)
	target := filepath.Join(root, "design", "BUILD.md")

	if result := designWrite(root, target); !result.Allowed {
		t.Fatalf("no loop active: the coordinator may run a design revision: %s", result.Reason)
	}

	if err := loop.WriteState(root, loop.NewState("epic", "PROJ-m1", 50)); err != nil {
		t.Fatal(err)
	}
	result := designWrite(root, target)
	if result.Allowed {
		t.Fatal("with a loop active the design tree is frozen for the coordinator too")
	}
	if !strings.Contains(result.Reason, "coordinator while an execution loop is active") {
		t.Errorf("reason: %s", result.Reason)
	}
}

// TestDesignTreeGuardOffWhenSubstrateOff: other projects must not acquire a
// new blocked path. With design.machinery off (the default), design/ is an
// ordinary directory.
func TestDesignTreeGuardOffWhenSubstrateOff(t *testing.T) {
	root, worktree := setupMachineryProject(t)
	if err := os.WriteFile(filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), []byte("design.machinery: off\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}
	if result := designWrite(worktree, filepath.Join(root, "design", "BUILD.md")); !result.Allowed {
		t.Fatalf("substrate off: no design-tree rule: %s", result.Reason)
	}
}

// TestDesignTreeGuardBashWrites: the rule follows write intent through bash
// too, not only the Write/Edit tools.
func TestDesignTreeGuardBashWrites(t *testing.T) {
	_, worktree := setupMachineryProject(t)
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}
	blocked := []string{
		`echo "row" >> design/machines/Deal.oracle.md`,
		`sed -i '' 's/a/b/' design/BUILD.md`,
		`rm design/formal/Policy.oracle.md`,
		`cat > design/ARCHITECTURE.md <<EOF
x
EOF`,
	}
	for _, cmd := range blocked {
		t.Run(cmd, func(t *testing.T) {
			result := CheckDispatcher(worktree, HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: cmd}})
			if result.Allowed {
				t.Fatalf("bash write into the design tree must be blocked: %q", cmd)
			}
		})
	}
	// A write OUTSIDE the design tree is untouched by this rule.
	result := CheckDispatcher(worktree, HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: `echo x > lib/hextropian/policy.ex`}})
	if !result.Allowed {
		t.Fatalf("implementation writes stay open: %s", result.Reason)
	}
}
