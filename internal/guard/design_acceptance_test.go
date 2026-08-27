package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
)

// planBlock is a milestone block as machinery's Gb-plan reads it: a marked,
// numbered milestone with a DoD line and, once discharged, a status line.
const planBlock = `# BUILD

## 9. Build plan

**M3 - Settlement slice.** Payment lifecycle end to end.
DoD: all 14 Payment oracle rows green by stable id (PAY-3f9c21 through PAY-b70e44).
`

// setupAcceptanceProject builds a machinery-first project with a plan
// document, a plan shard, and an existing evidence file, plus an active
// execution loop (the state in which every acceptance write actually
// happens: the seal gate runs inside the loop).
func setupAcceptanceProject(t *testing.T) (root, worktree string) {
	t.Helper()
	root, worktree = setupMachineryProject(t)
	designDir := filepath.Join(root, "design")
	for _, sub := range []string{"acceptance", "BUILD"} {
		if err := os.MkdirAll(filepath.Join(designDir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(designDir, "BUILD.md"), planBlock)
	write(filepath.Join(designDir, "BUILD", "M3-settlement.md"), planBlock)
	write(filepath.Join(designDir, "BUILD", "README.md"), "# shard index\n")
	write(filepath.Join(designDir, "acceptance", "M3.yaml"), "milestone: 3\nverdict: REJECTED\n")
	if err := loop.WriteState(root, loop.NewState("epic", "PROJ-m3", 50)); err != nil {
		t.Fatal(err)
	}
	return root, worktree
}

func toolWrite(cwd, target, content string) Result {
	return CheckDispatcher(cwd, HookInput{
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: target, Content: content},
	})
}

func toolEdit(cwd, target, oldString, newString string) Result {
	return CheckDispatcher(cwd, HookInput{
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: target, OldString: oldString, NewString: newString},
	})
}

const evidence = `milestone: 3
commit: 9f3c1a2b7d4e5f60718293a4b5c6d7e8f9012345
verdict: ACCEPTED
dod_ids:
  - PAY-3f9c21
attestations:
  - integration tests run against the real ledger; no mocks below the boundary
findings: []
reviewer: paivot-graph:anchor milestone seal review, epic PROJ-m3 (M3)
date: 2026-08-27
`

// TestAcceptanceEvidenceWriteIsSanctioned: the reviewing Anchor writes the
// milestone's acceptance evidence, whether it is tracked as an agent (a
// worktree spawn) or appears as the coordinator (the loop spawns it in the
// project root today, where no SubagentStart matcher tracks it).
func TestAcceptanceEvidenceWriteIsSanctioned(t *testing.T) {
	target := "design/acceptance/M3.yaml"

	t.Run("tracked anchor, Write", func(t *testing.T) {
		root, worktree := setupAcceptanceProject(t)
		if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:anchor"); err != nil {
			t.Fatal(err)
		}
		if r := toolWrite(worktree, filepath.Join(root, target), evidence); !r.Allowed {
			t.Fatalf("the reviewing anchor writes the evidence: %s", r.Reason)
		}
	})

	t.Run("tracked anchor, Edit", func(t *testing.T) {
		root, worktree := setupAcceptanceProject(t)
		if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:anchor"); err != nil {
			t.Fatal(err)
		}
		if r := toolEdit(worktree, filepath.Join(root, target), "verdict: REJECTED", "verdict: ACCEPTED"); !r.Allowed {
			t.Fatalf("overwriting the previous attempt is the documented flow: %s", r.Reason)
		}
	})

	t.Run("coordinator during a loop", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolWrite(root, filepath.Join(root, target), evidence); !r.Allowed {
			t.Fatalf("the seal-gate coordinator lands the evidence: %s", r.Reason)
		}
	})

	t.Run("relative path from the project root", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolWrite(root, target, evidence); !r.Allowed {
			t.Fatalf("a relative target resolves the same way: %s", r.Reason)
		}
	})
}

// TestClosureMarkerEditIsSanctioned: the seal-gate coordinator may add or
// flip a milestone's Status line, in the root plan or in a shard, in every
// decoration machinery's own parser tolerates.
func TestClosureMarkerEditIsSanctioned(t *testing.T) {
	dod := "DoD: all 14 Payment oracle rows green by stable id (PAY-3f9c21 through PAY-b70e44).\n"
	cases := []struct {
		name          string
		target        string
		before, after string
	}{
		{"add the marker beside DoD", "design/BUILD.md", dod, dod + "Status: closed\n"},
		{"flip open to closed", "design/BUILD.md", "Status: open", "Status: closed"},
		{"list decoration", "design/BUILD.md", dod, dod + "- Status: closed\n"},
		{"bold decoration", "design/BUILD.md", dod, dod + "**Status:** closed\n"},
		{"in a plan shard", "design/BUILD/M3-settlement.md", dod, dod + "Status: closed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := setupAcceptanceProject(t)
			if r := toolEdit(root, filepath.Join(root, tc.target), tc.before, tc.after); !r.Allowed {
				t.Fatalf("the closure act is one Status line: %s", r.Reason)
			}
		})
	}

	t.Run("Write whose only difference is the status line", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolWrite(root, filepath.Join(root, "design", "BUILD.md"), planBlock+"Status: closed\n"); !r.Allowed {
			t.Fatalf("a whole-file write that changes only the marker is the same act: %s", r.Reason)
		}
	})
}

// TestAcceptanceCarveOutDoesNotLeak walks every adjacent write the carve-out
// must still refuse. Each one is a way the exception could have been widened
// by accident.
func TestAcceptanceCarveOutDoesNotLeak(t *testing.T) {
	dod := "DoD: all 14 Payment oracle rows green by stable id (PAY-3f9c21 through PAY-b70e44).\n"

	t.Run("other files under acceptance/", func(t *testing.T) {
		for _, target := range []string{
			"design/acceptance/README.md",
			"design/acceptance/M3.yml",
			"design/acceptance/M3-round2.yaml",
			"design/acceptance/M3.yaml.bak",
			"design/acceptance/2026/M3.yaml",
			"design/acceptance/notes.txt",
		} {
			t.Run(target, func(t *testing.T) {
				root, worktree := setupAcceptanceProject(t)
				if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:anchor"); err != nil {
					t.Fatal(err)
				}
				if r := toolWrite(worktree, filepath.Join(root, target), "x"); r.Allowed {
					t.Fatalf("only acceptance/M<n>.yaml is writable, not %s", target)
				}
				// The coordinator has no wider permit than the anchor here.
				if r := toolWrite(root, filepath.Join(root, target), "x"); r.Allowed {
					t.Fatalf("the coordinator must not write %s either", target)
				}
			})
		}
	})

	t.Run("a sibling of the acceptance directory", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolWrite(root, filepath.Join(root, "design", "acceptance-notes.md"), "x"); r.Allowed {
			t.Fatal("the rule is path-anchored: acceptance-notes.md is not under acceptance/")
		}
	})

	t.Run("delivery roles never write evidence", func(t *testing.T) {
		for _, agent := range []string{"paivot-graph:developer", "paivot-graph:pm", "paivot-graph:sr-pm"} {
			t.Run(agent, func(t *testing.T) {
				root, worktree := setupAcceptanceProject(t)
				if err := dispatcher.TrackAgent(worktree, "agent-1", agent); err != nil {
					t.Fatal(err)
				}
				r := toolWrite(worktree, filepath.Join(root, "design", "acceptance", "M3.yaml"), evidence)
				if r.Allowed {
					t.Fatalf("%s must not write acceptance evidence", agent)
				}
				if !strings.Contains(r.Reason, "report card") {
					t.Errorf("the block must say why: %s", r.Reason)
				}
			})
		}
	})

	t.Run("the anchor does not close the milestone", func(t *testing.T) {
		root, worktree := setupAcceptanceProject(t)
		if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:anchor"); err != nil {
			t.Fatal(err)
		}
		r := toolEdit(worktree, filepath.Join(root, "design", "BUILD.md"), dod, dod+"Status: closed\n")
		if r.Allowed {
			t.Fatal("the Status line is the coordinator's closure act, not the reviewer's")
		}
		if !strings.Contains(r.Reason, "closure act") {
			t.Errorf("the block must say why: %s", r.Reason)
		}
	})

	t.Run("plan edits beyond the status line", func(t *testing.T) {
		cases := []struct {
			name          string
			before, after string
		}{
			{
				"a DoD change riding along with the marker",
				dod,
				"DoD: all 14 Payment oracle rows green by stable id (PAY-3f9c21).\nStatus: closed\n",
			},
			{
				"no status line at all",
				dod,
				"DoD: whatever the delivery managed to pass.\n",
			},
			{
				"a milestone title rewritten beside the marker",
				"**M3 - Settlement slice.** Payment lifecycle end to end.\n" + dod,
				"**M3 - Settlement slice (descoped).** Payment lifecycle.\n" + dod + "Status: closed\n",
			},
			{
				"a status-looking line that also says something else",
				dod,
				dod + "Status: closed and the DoD is waived\n",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				root, _ := setupAcceptanceProject(t)
				if r := toolEdit(root, filepath.Join(root, "design", "BUILD.md"), tc.before, tc.after); r.Allowed {
					t.Fatal("only a milestone Status line may change in a plan document")
				}
			})
		}
	})

	t.Run("Write that rewrites the plan", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolWrite(root, filepath.Join(root, "design", "BUILD.md"), "# BUILD\n\nStatus: closed\n"); r.Allowed {
			t.Fatal("a whole-file rewrite is not a closure act")
		}
	})

	t.Run("Write creating a new plan document", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		target := filepath.Join(root, "design", "BUILD", "M9-invented.md")
		if r := toolWrite(root, target, planBlock+"Status: closed\n"); r.Allowed {
			t.Fatal("the exception edits an existing plan; it does not author new ones")
		}
	})

	t.Run("shard index files are not plan documents", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		if r := toolEdit(root, filepath.Join(root, "design", "BUILD", "README.md"), "# shard index\n", "# shard index\nStatus: closed\n"); r.Allowed {
			t.Fatal("BUILD/README.md is navigation, not a plan shard")
		}
	})

	t.Run("shell writes stay blocked", func(t *testing.T) {
		for _, cmd := range []string{
			"cat > design/acceptance/M3.yaml <<'EOF'\nverdict: ACCEPTED\nEOF",
			"rm design/acceptance/M3.yaml",
			"cp /tmp/evidence.yaml design/acceptance/M3.yaml",
			`sed -i '' 's/Status: open/Status: closed/' design/BUILD.md`,
		} {
			t.Run(cmd, func(t *testing.T) {
				root, _ := setupAcceptanceProject(t)
				r := CheckDispatcher(root, HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: cmd}})
				if r.Allowed {
					t.Fatalf("a shell write carries no reviewable before and after: %q", cmd)
				}
			})
		}
	})

	t.Run("the rest of the design tree is untouched by the carve-out", func(t *testing.T) {
		root, _ := setupAcceptanceProject(t)
		for _, target := range []string{
			"design/ARCHITECTURE.md",
			"design/domain.modelith.yaml",
			"design/machines/Deal.oracle.md",
			"design/formal/Policy.oracle.md",
		} {
			if r := toolWrite(root, filepath.Join(root, target), "x"); r.Allowed {
				t.Errorf("the coordinator must not write %s while a loop runs", target)
			}
		}
	})
}

// TestAcceptanceCarveOutNeedsTheSubstrate: with design.machinery off there is
// no design-tree rule at all, so the carve-out neither adds nor removes
// anything on other projects.
func TestAcceptanceCarveOutNeedsTheSubstrate(t *testing.T) {
	root, worktree := setupAcceptanceProject(t)
	if err := os.WriteFile(filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), []byte("design.machinery: off\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}
	if r := toolWrite(worktree, filepath.Join(root, "design", "acceptance", "M3.yaml"), evidence); !r.Allowed {
		t.Fatalf("substrate off: design/ is an ordinary directory: %s", r.Reason)
	}
}

// TestAcceptanceEvidenceStillReadable: the guard is a WRITE guard. Every role
// reads the committed evidence (the next reviewer reads the last verdict).
func TestAcceptanceEvidenceStillReadable(t *testing.T) {
	root, worktree := setupAcceptanceProject(t)
	if err := dispatcher.TrackAgent(worktree, "agent-1", "paivot-graph:developer"); err != nil {
		t.Fatal(err)
	}
	for _, input := range []HookInput{
		{ToolName: "Read", ToolInput: ToolInput{FilePath: filepath.Join(root, "design", "acceptance", "M3.yaml")}},
		{ToolName: "Bash", ToolInput: ToolInput{Command: "cat design/acceptance/M3.yaml"}},
		{ToolName: "Bash", ToolInput: ToolInput{Command: "git add design/acceptance/M3.yaml design/BUILD.md"}},
		{ToolName: "Bash", ToolInput: ToolInput{Command: `git commit -m "accept(M3): milestone closed"`}},
	} {
		if r := CheckDispatcher(worktree, input); !r.Allowed {
			t.Errorf("reads and git plumbing stay open (%s): %s", input.ToolName, r.Reason)
		}
	}
}
