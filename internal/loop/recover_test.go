package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEvaluateRecover_EmptyConfig(t *testing.T) {
	plan := EvaluateRecover(RecoverConfig{})
	if len(plan.Actions) != 0 {
		t.Errorf("expected no actions for empty config, got %d", len(plan.Actions))
	}
	if plan.Summary.WorktreesRemoved != 0 {
		t.Errorf("expected 0 worktrees removed, got %d", plan.Summary.WorktreesRemoved)
	}
}

func TestEvaluateRecover_OrphanedStory(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-a1b",
				NDStatus:     "in_progress",
				NDLabels:     nil,
				WorktreePath: "/project/.claude/worktrees/agent-a1b",
				BranchName:   "worktree-agent-a1b",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-a1b", Status: "in_progress", Labels: nil},
		},
	}

	plan := EvaluateRecover(cfg)

	// Expect: remove worktree + delete branch + reset story
	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertContains(t, kinds, ActionDeleteBranch, "delete_branch")
	assertContains(t, kinds, ActionResetStory, "reset_story")
	assertNotContains(t, kinds, ActionNoteDelivered, "note_delivered")

	if plan.Summary.WorktreesRemoved != 1 {
		t.Errorf("expected 1 worktree removed, got %d", plan.Summary.WorktreesRemoved)
	}
	if plan.Summary.BranchesDeleted != 1 {
		t.Errorf("expected 1 branch deleted, got %d", plan.Summary.BranchesDeleted)
	}
	if plan.Summary.StoriesReset != 1 {
		t.Errorf("expected 1 story reset, got %d", plan.Summary.StoriesReset)
	}
}

func TestEvaluateRecover_DeliveredStory_PreservesBranch(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/project/.claude/worktrees/agent-c3d",
				BranchName:   "story/PROJ-c3d",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-c3d", Status: "in_progress", Labels: []string{"delivered"}},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	// Worktree is disposable; the branch is the record of the delivered work.
	assertContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertContains(t, kinds, ActionPreserveBranch, "preserve_branch")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")
	assertContains(t, kinds, ActionNoteDelivered, "note_delivered")
	assertNotContains(t, kinds, ActionResetStory, "reset_story")

	if plan.Summary.StoriesDelivered != 1 {
		t.Errorf("expected 1 story delivered, got %d", plan.Summary.StoriesDelivered)
	}
	if plan.Summary.StoriesReset != 0 {
		t.Errorf("expected 0 stories reset, got %d", plan.Summary.StoriesReset)
	}
	if plan.Summary.BranchesPreserved != 1 {
		t.Errorf("expected 1 branch preserved, got %d", plan.Summary.BranchesPreserved)
	}
	if plan.Summary.BranchesDeleted != 0 {
		t.Errorf("expected 0 branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
}

func TestEvaluateRecover_AcceptedStory_PreservesBranch(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/project/.claude/worktrees/agent-c3d",
				BranchName:   "story/PROJ-c3d",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-c3d", Status: "in_progress", Labels: []string{"accepted"}},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionPreserveBranch, "preserve_branch")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")
}

func TestEvaluateRecover_OrphanWorktree_DeliveredStoryBranchPreserved(t *testing.T) {
	cfg := RecoverConfig{
		// Orphan worktree (not in snapshot) checked out on a story branch
		// whose story is delivered in nd: remove the worktree, keep the branch.
		CurrentWorktrees: []Worktree{
			{
				Path:   "/project/.claude/worktrees/orphan-d4e",
				Branch: "story/PROJ-d4e",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-d4e", Status: "in_progress", Labels: []string{"delivered"}},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertContains(t, kinds, ActionPreserveBranch, "preserve_branch")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")

	if plan.Summary.BranchesPreserved != 1 {
		t.Errorf("expected 1 branch preserved, got %d", plan.Summary.BranchesPreserved)
	}
	if plan.Summary.OrphanWorktrees != 1 {
		t.Errorf("expected 1 orphan worktree, got %d", plan.Summary.OrphanWorktrees)
	}
}

func TestEvaluateRecover_OrphanWorktree_NonDeliveredStoryBranchDeleted(t *testing.T) {
	cfg := RecoverConfig{
		CurrentWorktrees: []Worktree{
			{
				Path:   "/project/.claude/worktrees/orphan-d4e",
				Branch: "story/PROJ-d4e",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-d4e", Status: "in_progress", Labels: nil},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionDeleteBranch, "delete_branch")
	assertNotContains(t, kinds, ActionPreserveBranch, "preserve_branch")
}

func TestEvaluateRecover_PreservedBranchNotRedeletedByStaleOrPMLoops(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/wt/agent-c3d",
				BranchName:   "worktree-agent-PROJ-c3d",
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-c3d", Status: "in_progress", Labels: []string{"delivered"}},
		},
		// The same branch shows up in the unconditional PM isolation list.
		PMIsolationBranches: []string{"worktree-agent-PROJ-c3d"},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionPreserveBranch, "preserve_branch")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")
	if plan.Summary.BranchesDeleted != 0 {
		t.Errorf("expected 0 branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
}

func TestEvaluateRecover_StoryClosedSinceSnapshot(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-e5f",
				NDStatus:     "in_progress",
				WorktreePath: "/project/.claude/worktrees/agent-e5f",
				BranchName:   "worktree-agent-e5f",
			},
		},
		// Story no longer in-progress in nd (was closed)
		InProgressIssues: nil,
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertContains(t, kinds, ActionDeleteBranch, "delete_branch")
	// No nd mutation since story is no longer in-progress
	assertNotContains(t, kinds, ActionResetStory, "reset_story")
	assertNotContains(t, kinds, ActionNoteDelivered, "note_delivered")
}

func TestEvaluateRecover_OrphanWorktree(t *testing.T) {
	cfg := RecoverConfig{
		// No snapshot stories
		CurrentWorktrees: []Worktree{
			{
				Path:   "/project/.claude/worktrees/orphan-123",
				Branch: "worktree-orphan-123",
			},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertContains(t, kinds, ActionDeleteBranch, "delete_branch")

	if plan.Summary.OrphanWorktrees != 1 {
		t.Errorf("expected 1 orphan worktree, got %d", plan.Summary.OrphanWorktrees)
	}
}

func TestEvaluateRecover_StoryWithNoWorktree(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:  "PROJ-g7h",
				NDStatus: "in_progress",
				// No worktree path or branch
			},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-g7h", Status: "in_progress"},
		},
	}

	plan := EvaluateRecover(cfg)

	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionResetStory, "reset_story")
	assertNotContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")

	if plan.Summary.WorktreesRemoved != 0 {
		t.Errorf("expected 0 worktrees removed, got %d", plan.Summary.WorktreesRemoved)
	}
}

func TestEvaluateRecover_MixedStories(t *testing.T) {
	cfg := RecoverConfig{
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-a1b",
				NDStatus:     "in_progress",
				WorktreePath: "/wt/agent-a1b",
				BranchName:   "wt-a1b",
			},
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/wt/agent-c3d",
				BranchName:   "wt-c3d",
			},
			{
				StoryID:      "PROJ-e5f",
				NDStatus:     "in_progress",
				WorktreePath: "/wt/agent-e5f",
				BranchName:   "wt-e5f",
			},
		},
		CurrentWorktrees: []Worktree{
			{Path: "/wt/agent-a1b", Branch: "wt-a1b"},
			{Path: "/wt/agent-c3d", Branch: "wt-c3d"},
			{Path: "/wt/agent-e5f", Branch: "wt-e5f"},
			{Path: "/wt/orphan-xyz", Branch: "orphan-xyz"},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-a1b", Status: "in_progress"},
			{ID: "PROJ-c3d", Status: "in_progress", Labels: []string{"delivered"}},
			// PROJ-e5f was closed
		},
	}

	plan := EvaluateRecover(cfg)

	// 3 snapshot worktrees + 1 orphan = 4 worktrees removed
	if plan.Summary.WorktreesRemoved != 4 {
		t.Errorf("expected 4 worktrees removed, got %d", plan.Summary.WorktreesRemoved)
	}
	// 2 snapshot branches (a1b, e5f) + 1 orphan = 3 branches deleted;
	// PROJ-c3d's branch is preserved (delivered but not merged).
	if plan.Summary.BranchesDeleted != 3 {
		t.Errorf("expected 3 branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
	if plan.Summary.BranchesPreserved != 1 {
		t.Errorf("expected 1 branch preserved, got %d", plan.Summary.BranchesPreserved)
	}
	// PROJ-a1b reset (in-progress, no delivered label)
	if plan.Summary.StoriesReset != 1 {
		t.Errorf("expected 1 story reset, got %d", plan.Summary.StoriesReset)
	}
	// PROJ-c3d delivered
	if plan.Summary.StoriesDelivered != 1 {
		t.Errorf("expected 1 story delivered, got %d", plan.Summary.StoriesDelivered)
	}
	// 1 orphan
	if plan.Summary.OrphanWorktrees != 1 {
		t.Errorf("expected 1 orphan worktree, got %d", plan.Summary.OrphanWorktrees)
	}
}

func TestIsInProgressInND_Found(t *testing.T) {
	issues := []ndIssue{
		{ID: "PROJ-a1b", Status: "in_progress"},
		{ID: "PROJ-c3d", Status: "ready"},
	}
	if !isInProgressInND("PROJ-a1b", issues) {
		t.Error("expected true for in-progress story")
	}
}

func TestIsInProgressInND_NotFound(t *testing.T) {
	issues := []ndIssue{
		{ID: "PROJ-a1b", Status: "in_progress"},
	}
	if isInProgressInND("PROJ-xxx", issues) {
		t.Error("expected false for missing story")
	}
}

func TestIsInProgressInND_WrongStatus(t *testing.T) {
	issues := []ndIssue{
		{ID: "PROJ-a1b", Status: "closed"},
	}
	if isInProgressInND("PROJ-a1b", issues) {
		t.Error("expected false for closed story")
	}
}

func TestIsInProgressInND_Empty(t *testing.T) {
	if isInProgressInND("PROJ-a1b", nil) {
		t.Error("expected false for nil issues")
	}
}

func TestIsInProgressInND_CaseInsensitive(t *testing.T) {
	issues := []ndIssue{
		{ID: "PROJ-a1b", Status: "In_Progress"},
	}
	if !isInProgressInND("PROJ-a1b", issues) {
		t.Error("expected case-insensitive match")
	}
}

func TestEvaluateRecover_StaleMergedBranches(t *testing.T) {
	cfg := RecoverConfig{
		StaleBranches: []string{
			"epic/PROJ-abc",
			"epic/PROJ-def",
			"story/PROJ-g1h",
			"worktree-agent-xyz",
		},
	}

	plan := EvaluateRecover(cfg)

	// All 4 stale branches should be scheduled for deletion
	deleteCount := 0
	for _, a := range plan.Actions {
		if a.Kind == ActionDeleteBranch {
			deleteCount++
		}
	}
	if deleteCount != 4 {
		t.Errorf("expected 4 branch deletions, got %d", deleteCount)
	}
	if plan.Summary.StaleBranchesDeleted != 4 {
		t.Errorf("expected 4 stale branches deleted, got %d", plan.Summary.StaleBranchesDeleted)
	}
	if plan.Summary.BranchesDeleted != 4 {
		t.Errorf("expected 4 total branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
}

func TestEvaluateRecover_StaleBranchDeduplication(t *testing.T) {
	// Stale branch that is also an orphan worktree branch should not be deleted twice
	cfg := RecoverConfig{
		CurrentWorktrees: []Worktree{
			{Path: "/wt/agent-abc", Branch: "worktree-agent-abc"},
		},
		StaleBranches: []string{
			"worktree-agent-abc", // same as orphan worktree branch
			"epic/PROJ-old",      // genuinely stale
		},
	}

	plan := EvaluateRecover(cfg)

	// worktree-agent-abc: 1 from orphan cleanup
	// epic/PROJ-old: 1 from stale branch cleanup
	// worktree-agent-abc should NOT be duplicated
	if plan.Summary.BranchesDeleted != 2 {
		t.Errorf("expected 2 total branches deleted (no duplicates), got %d", plan.Summary.BranchesDeleted)
	}
	if plan.Summary.StaleBranchesDeleted != 1 {
		t.Errorf("expected 1 stale branch (epic/PROJ-old only), got %d", plan.Summary.StaleBranchesDeleted)
	}
}

func TestExecuteRecover_ResetStoryResolvesVaultAndAnchorsToProjectRoot(t *testing.T) {
	var calls [][]string
	var cmds []*exec.Cmd
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		cmd := exec.Command("true")
		cmds = append(cmds, cmd)
		return cmd
	}
	defer func() { execCommand = oldExec }()

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	projectRoot := t.TempDir()
	plan := RecoverPlan{
		Actions: []RecoverAction{
			{Kind: ActionResetStory, StoryID: "PROJ-a1b", Reason: "test"},
		},
	}

	errs := ExecuteRecover(projectRoot, plan)
	if len(errs) != 0 {
		t.Fatalf("ExecuteRecover() errors: %v", errs)
	}

	want := []string{"nd", "--vault", override, "update", "PROJ-a1b", "--status", "open"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("unexpected nd call: got %#v want %#v", calls, want)
	}
	if cmds[0].Dir != projectRoot {
		t.Fatalf("expected nd command Dir %q, got %q", projectRoot, cmds[0].Dir)
	}
}

func TestExecuteRecover_PreserveBranchIsNoOp(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	plan := RecoverPlan{
		Actions: []RecoverAction{
			{Kind: ActionPreserveBranch, StoryID: "PROJ-a1b", BranchName: "story/PROJ-a1b", Reason: "test"},
		},
	}

	errs := ExecuteRecover(t.TempDir(), plan)
	if len(errs) != 0 {
		t.Fatalf("ExecuteRecover() errors: %v", errs)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no commands for preserve_branch, got %#v", calls)
	}
}

// --- helpers ---

func actionKinds(actions []RecoverAction) []ActionKind {
	var kinds []ActionKind
	for _, a := range actions {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}

func assertContains(t *testing.T, kinds []ActionKind, want ActionKind, label string) {
	t.Helper()
	for _, k := range kinds {
		if k == want {
			return
		}
	}
	t.Errorf("expected actions to contain %s", label)
}

func assertNotContains(t *testing.T, kinds []ActionKind, unwanted ActionKind, label string) {
	t.Helper()
	for _, k := range kinds {
		if k == unwanted {
			t.Errorf("expected actions NOT to contain %s", label)
			return
		}
	}
}
