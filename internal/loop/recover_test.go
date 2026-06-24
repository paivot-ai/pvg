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
		WorktreeBase: "/project/.claude/worktrees",
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/project/.claude/worktrees/agent-c3d",
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
	// Owned worktrees live under the Paivot base and use Paivot branch names so
	// they are legitimately cleaned up. The trailing "/wt/orphan-xyz" entry is a
	// FOREIGN worktree (outside the base) and MUST be preserved -- never removed,
	// its branch never deleted.
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		SnapshotStories: []SnapshotEntry{
			{
				StoryID:      "PROJ-a1b",
				NDStatus:     "in_progress",
				WorktreePath: "/project/.claude/worktrees/agent-a1b",
				BranchName:   "story/PROJ-a1b",
			},
			{
				StoryID:      "PROJ-c3d",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				WorktreePath: "/project/.claude/worktrees/agent-c3d",
				BranchName:   "story/PROJ-c3d",
			},
			{
				StoryID:      "PROJ-e5f",
				NDStatus:     "in_progress",
				WorktreePath: "/project/.claude/worktrees/agent-e5f",
				BranchName:   "story/PROJ-e5f",
			},
		},
		CurrentWorktrees: []Worktree{
			{Path: "/project/.claude/worktrees/agent-a1b", Branch: "story/PROJ-a1b"},
			{Path: "/project/.claude/worktrees/agent-c3d", Branch: "story/PROJ-c3d"},
			{Path: "/project/.claude/worktrees/agent-e5f", Branch: "story/PROJ-e5f"},
			// FOREIGN: outside the owned base -- must be preserved untouched.
			{Path: "/wt/orphan-xyz", Branch: "orphan-xyz"},
		},
		InProgressIssues: []ndIssue{
			{ID: "PROJ-a1b", Status: "in_progress"},
			{ID: "PROJ-c3d", Status: "in_progress", Labels: []string{"delivered"}},
			// PROJ-e5f was closed
		},
	}

	plan := EvaluateRecover(cfg)

	// 3 owned snapshot worktrees removed; the foreign orphan is preserved.
	if plan.Summary.WorktreesRemoved != 3 {
		t.Errorf("expected 3 worktrees removed, got %d", plan.Summary.WorktreesRemoved)
	}
	if plan.Summary.ForeignWorktreesPreserved != 1 {
		t.Errorf("expected 1 foreign worktree preserved, got %d", plan.Summary.ForeignWorktreesPreserved)
	}
	// 2 snapshot branches (a1b, e5f) deleted; PROJ-c3d's branch is preserved
	// (delivered). The foreign worktree's branch is NEVER deleted.
	if plan.Summary.BranchesDeleted != 2 {
		t.Errorf("expected 2 branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
	if plan.Summary.BranchesPreserved != 1 {
		t.Errorf("expected 1 branch preserved, got %d", plan.Summary.BranchesPreserved)
	}
	// The foreign branch must NOT appear as a delete action anywhere.
	for _, a := range plan.Actions {
		if a.Kind == ActionDeleteBranch && a.BranchName == "orphan-xyz" {
			t.Errorf("foreign branch orphan-xyz must never be deleted")
		}
	}
	// PROJ-a1b reset (in-progress, no delivered label)
	if plan.Summary.StoriesReset != 1 {
		t.Errorf("expected 1 story reset, got %d", plan.Summary.StoriesReset)
	}
	// PROJ-c3d delivered
	if plan.Summary.StoriesDelivered != 1 {
		t.Errorf("expected 1 story delivered, got %d", plan.Summary.StoriesDelivered)
	}
	// Owned orphans: none here (all owned worktrees are accounted by snapshot).
	if plan.Summary.OrphanWorktrees != 0 {
		t.Errorf("expected 0 owned orphan worktrees, got %d", plan.Summary.OrphanWorktrees)
	}
}

// --- Foreign-worktree preservation regressions (the data-loss bug) ---

// TestEvaluateRecover_ForeignCodexWorktreePreserved verifies that a Codex
// worktree under .codex-worktrees/ with an EMPTY snapshot is preserved: never
// removed, its branch never deleted. This is the exact case the user hit --
// Paivot deleting worktrees that were not his.
func TestEvaluateRecover_ForeignCodexWorktreePreserved(t *testing.T) {
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		// No snapshot stories.
		CurrentWorktrees: []Worktree{
			{Path: "/project/.codex-worktrees/feature-x", Branch: "feature/x"},
		},
	}

	plan := EvaluateRecover(cfg)

	if plan.Summary.ForeignWorktreesPreserved != 1 {
		t.Errorf("expected 1 foreign worktree preserved, got %d", plan.Summary.ForeignWorktreesPreserved)
	}
	if plan.Summary.WorktreesRemoved != 0 {
		t.Errorf("expected 0 worktrees removed, got %d", plan.Summary.WorktreesRemoved)
	}
	if plan.Summary.BranchesDeleted != 0 {
		t.Errorf("expected 0 branches deleted, got %d", plan.Summary.BranchesDeleted)
	}
	kinds := actionKinds(plan.Actions)
	assertContains(t, kinds, ActionSkipForeignWorktree, "skip_foreign_worktree")
	assertNotContains(t, kinds, ActionRemoveWorktree, "remove_worktree")
	assertNotContains(t, kinds, ActionDeleteBranch, "delete_branch")
}

// TestEvaluateRecover_ForeignCodexWorktreeOnPaivotLikeBranch ensures that even
// when a foreign worktree happens to be checked out on a Paivot-NAMED branch
// (story/...), it is STILL preserved -- ownership is decided by the worktree
// path, not the branch name. The branch must not be deleted either.
func TestEvaluateRecover_ForeignCodexWorktreeOnPaivotLikeBranch(t *testing.T) {
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		CurrentWorktrees: []Worktree{
			{Path: "/project/.codex-worktrees/feature-x", Branch: "story/PROJ-x"},
		},
	}

	plan := EvaluateRecover(cfg)

	if plan.Summary.ForeignWorktreesPreserved != 1 {
		t.Errorf("expected 1 foreign worktree preserved, got %d", plan.Summary.ForeignWorktreesPreserved)
	}
	if plan.Summary.WorktreesRemoved != 0 || plan.Summary.BranchesDeleted != 0 {
		t.Errorf("foreign worktree on a story/ branch must be untouched; got removed=%d deleted=%d",
			plan.Summary.WorktreesRemoved, plan.Summary.BranchesDeleted)
	}
}

// TestEvaluateRecover_ExternalAbsolutePathWorktreePreserved verifies that a
// worktree at an external absolute path (outside the project entirely) is
// preserved.
func TestEvaluateRecover_ExternalAbsolutePathWorktreePreserved(t *testing.T) {
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		CurrentWorktrees: []Worktree{
			{Path: "/home/user/other-project-wt", Branch: "main"},
		},
	}

	plan := EvaluateRecover(cfg)

	if plan.Summary.ForeignWorktreesPreserved != 1 {
		t.Errorf("expected 1 foreign worktree preserved, got %d", plan.Summary.ForeignWorktreesPreserved)
	}
	if plan.Summary.WorktreesRemoved != 0 {
		t.Errorf("expected 0 worktrees removed for external path, got %d", plan.Summary.WorktreesRemoved)
	}
}

// TestEvaluateRecover_NoSnapshot_OwnedRemovedForeignPreserved is the headline
// mixed case: no snapshot at all, one OWNED worktree (.claude/worktrees/dev-A on
// story/A, not in nd) and one FOREIGN worktree (.codex-worktrees/B). The owned
// A is removed; the foreign B is preserved.
func TestEvaluateRecover_NoSnapshot_OwnedRemovedForeignPreserved(t *testing.T) {
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		CurrentWorktrees: []Worktree{
			{Path: "/project/.claude/worktrees/dev-A", Branch: "story/A"},
			{Path: "/project/.codex-worktrees/B", Branch: "feature/B"},
		},
		// story/A is NOT delivered in nd (no entry) -> branch cleanup applies.
		InProgressIssues: nil,
	}

	plan := EvaluateRecover(cfg)

	if plan.Summary.WorktreesRemoved != 1 {
		t.Errorf("expected 1 (owned) worktree removed, got %d", plan.Summary.WorktreesRemoved)
	}
	if plan.Summary.ForeignWorktreesPreserved != 1 {
		t.Errorf("expected 1 foreign worktree preserved, got %d", plan.Summary.ForeignWorktreesPreserved)
	}
	// The owned A is on story/A -> a Paivot branch -> deleted. The foreign B is
	// on feature/B and must NOT be deleted.
	if plan.Summary.BranchesDeleted != 1 {
		t.Errorf("expected 1 branch deleted (story/A only), got %d", plan.Summary.BranchesDeleted)
	}
	for _, a := range plan.Actions {
		if a.Kind == ActionDeleteBranch && a.BranchName == "feature/B" {
			t.Errorf("foreign branch feature/B must never be deleted")
		}
		if a.Kind == ActionRemoveWorktree && a.WorktreePath == "/project/.codex-worktrees/B" {
			t.Errorf("foreign worktree .codex-worktrees/B must never be removed")
		}
	}
}

// TestEvaluateRecover_OwnedWorktreeNonPaivotBranchPreserved verifies that an
// OWNED worktree whose branch is NOT a Paivot branch has its worktree removed
// (it is under the base) but its branch PRESERVED (Paivot deletes only its own
// branch names).
func TestEvaluateRecover_OwnedWorktreeNonPaivotBranchPreserved(t *testing.T) {
	cfg := RecoverConfig{
		WorktreeBase: "/project/.claude/worktrees",
		CurrentWorktrees: []Worktree{
			{Path: "/project/.claude/worktrees/dev-Z", Branch: "feature/random"},
		},
	}

	plan := EvaluateRecover(cfg)

	if plan.Summary.WorktreesRemoved != 1 {
		t.Errorf("expected 1 owned worktree removed, got %d", plan.Summary.WorktreesRemoved)
	}
	if plan.Summary.BranchesDeleted != 0 {
		t.Errorf("expected 0 branches deleted (feature/random is not a Paivot branch), got %d", plan.Summary.BranchesDeleted)
	}
	if plan.Summary.BranchesPreserved != 1 {
		t.Errorf("expected 1 branch preserved, got %d", plan.Summary.BranchesPreserved)
	}
}

// TestIsOwnedWorktreePath_FallbackSegment exercises the empty-base fallback:
// detection of the .claude/worktrees path segment.
func TestIsOwnedWorktreePath_FallbackSegment(t *testing.T) {
	cases := []struct {
		path string
		base string
		want bool
	}{
		{"/p/.claude/worktrees/dev-A", "", true},
		{"/p/.codex-worktrees/B", "", false},
		{"/home/user/other", "", false},
		{"/p/.claude/worktrees/dev-A", "/p/.claude/worktrees", true},
		{"/p/.claude/worktrees", "/p/.claude/worktrees", true},
		{"/p/.codex-worktrees/B", "/p/.claude/worktrees", false},
		{"/p/.claude/worktrees-evil/x", "/p/.claude/worktrees", false}, // prefix-but-not-subdir
		{"", "/p/.claude/worktrees", false},
	}
	for _, c := range cases {
		if got := isOwnedWorktreePath(c.path, c.base); got != c.want {
			t.Errorf("isOwnedWorktreePath(%q, %q) = %v, want %v", c.path, c.base, got, c.want)
		}
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
		WorktreeBase: "/project/.claude/worktrees",
		CurrentWorktrees: []Worktree{
			{Path: "/project/.claude/worktrees/agent-abc", Branch: "worktree-agent-abc"},
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

func TestExecuteRecover_SkipForeignWorktreeIsNoOp(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	plan := RecoverPlan{
		Actions: []RecoverAction{
			{
				Kind:         ActionSkipForeignWorktree,
				WorktreePath: "/project/.codex-worktrees/feature-x",
				BranchName:   "feature/x",
				Reason:       "test",
			},
		},
	}

	errs := ExecuteRecover(t.TempDir(), plan)
	if len(errs) != 0 {
		t.Fatalf("ExecuteRecover() errors: %v", errs)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no commands for skip_foreign_worktree, got %#v", calls)
	}
}

// TestRecover_Integration_PreservesForeignWorktree is the highest-value
// regression: it builds a REAL git repo with both a Paivot-owned worktree
// (.claude/worktrees/dev-X on story/X) and a foreign worktree
// (.codex-worktrees/foreign on feature/foreign), runs the full
// BuildRecoverConfig -> EvaluateRecover -> ExecuteRecover pipeline, and asserts
// the foreign worktree directory and its branch SURVIVE while the owned one is
// removed.
func TestRecover_Integration_PreservesForeignWorktree(t *testing.T) {
	root := t.TempDir()

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s", args, out)
		}
	}

	// init repo on main with an initial commit
	if out, err := exec.Command("git", "init", "-b", "main", root).CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}
	run("-C", root, "commit", "--allow-empty", "-m", "init")

	// Owned worktree: .claude/worktrees/dev-X on story/X
	run("-C", root, "branch", "story/X")
	ownedWT := filepath.Join(root, ".claude", "worktrees", "dev-X")
	if err := os.MkdirAll(filepath.Dir(ownedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	run("-C", root, "worktree", "add", ownedWT, "story/X")

	// Foreign worktree: .codex-worktrees/foreign on feature/foreign
	run("-C", root, "branch", "feature/foreign")
	foreignWT := filepath.Join(root, ".codex-worktrees", "foreign")
	if err := os.MkdirAll(filepath.Dir(foreignWT), 0o755); err != nil {
		t.Fatal(err)
	}
	run("-C", root, "worktree", "add", foreignWT, "feature/foreign")

	// Sanity: both worktrees exist before recovery.
	if _, err := os.Stat(ownedWT); err != nil {
		t.Fatalf("owned worktree missing before recover: %v", err)
	}
	if _, err := os.Stat(foreignWT); err != nil {
		t.Fatalf("foreign worktree missing before recover: %v", err)
	}

	// Run the full recover pipeline (no snapshot exists).
	cfg, err := BuildRecoverConfig(root)
	if err != nil {
		t.Fatalf("BuildRecoverConfig: %v", err)
	}
	// The owned base must resolve under the project root.
	wantBase := filepath.Join(root, ".claude", "worktrees")
	if cfg.WorktreeBase != wantBase {
		t.Fatalf("WorktreeBase = %q, want %q", cfg.WorktreeBase, wantBase)
	}
	plan := EvaluateRecover(cfg)
	_ = ExecuteRecover(root, plan)

	// The foreign worktree directory MUST still exist.
	if _, err := os.Stat(foreignWT); err != nil {
		t.Errorf("foreign worktree was removed (data loss!): %v", err)
	}
	// The foreign branch MUST still exist.
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "feature/foreign").Run(); err != nil {
		t.Errorf("foreign branch feature/foreign was deleted (data loss!)")
	}

	// The owned worktree directory should be gone (legitimate cleanup).
	if _, err := os.Stat(ownedWT); err == nil {
		t.Errorf("owned worktree .claude/worktrees/dev-X was NOT removed")
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
