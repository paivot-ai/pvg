package loop

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/paivot-ai/pvg/internal/ndvault"
	"github.com/paivot-ai/pvg/internal/worktree"
)

// ActionKind describes a single recovery action type.
type ActionKind string

const (
	ActionRemoveWorktree ActionKind = "remove_worktree"
	ActionDeleteBranch   ActionKind = "delete_branch"
	ActionResetStory     ActionKind = "reset_story"
	ActionNoteDelivered  ActionKind = "note_delivered"
	// ActionPreserveBranch is informational (no-op in ExecuteRecover): the
	// branch holds delivered-but-unmerged work and must NOT be deleted.
	ActionPreserveBranch ActionKind = "preserve_branch"
)

// RecoverAction is one step in a recovery plan.
type RecoverAction struct {
	Kind         ActionKind `json:"kind"`
	StoryID      string     `json:"story_id,omitempty"`
	WorktreePath string     `json:"worktree_path,omitempty"`
	BranchName   string     `json:"branch_name,omitempty"`
	Reason       string     `json:"reason"`
}

// RecoverConfig holds all inputs needed for recovery evaluation.
// Constructed by BuildRecoverConfig -- main.go never builds this directly.
type RecoverConfig struct {
	SnapshotStories     []SnapshotEntry
	CurrentWorktrees    []Worktree
	InProgressIssues    []ndIssue
	StaleBranches       []string // local branches merged into main matching epic/*, story/*, worktree-*
	PMIsolationBranches []string // local worktree-agent-* branches (never merged to main)
	Warnings            []string
}

// RecoverSummary counts what recovery did.
type RecoverSummary struct {
	WorktreesRemoved     int `json:"worktrees_removed"`
	BranchesDeleted      int `json:"branches_deleted"`
	BranchesPreserved    int `json:"branches_preserved"`
	StoriesReset         int `json:"stories_reset"`
	StoriesDelivered     int `json:"stories_delivered"`
	OrphanWorktrees      int `json:"orphan_worktrees"`
	StaleBranchesDeleted int `json:"stale_branches_deleted"`
}

// RecoverPlan is the full set of recovery actions with a summary.
type RecoverPlan struct {
	Actions []RecoverAction `json:"actions"`
	Summary RecoverSummary  `json:"summary"`
}

// EvaluateRecover is a pure function -- no I/O. It examines the snapshot,
// current worktrees, and nd state to produce a recovery plan.
//
// Branches that hold delivered-but-unmerged work are PRESERVED: the worktree
// checkout is disposable, but the branch is the record. Deleting it would
// destroy work that the PM has not yet reviewed or merged.
func EvaluateRecover(cfg RecoverConfig) RecoverPlan {
	var plan RecoverPlan

	// Track which worktree paths are accounted for by snapshot entries
	accountedWorktrees := make(map[string]bool)

	// Process snapshot stories
	for _, entry := range cfg.SnapshotStories {
		// Worktree cleanup
		if entry.WorktreePath != "" {
			plan.Actions = append(plan.Actions, RecoverAction{
				Kind:         ActionRemoveWorktree,
				StoryID:      entry.StoryID,
				WorktreePath: entry.WorktreePath,
				Reason:       "snapshot worktree cleanup",
			})
			plan.Summary.WorktreesRemoved++
			accountedWorktrees[entry.WorktreePath] = true
		}

		if entry.BranchName != "" {
			if isDeliveredInND(entry.StoryID, cfg.InProgressIssues) {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionPreserveBranch,
					StoryID:    entry.StoryID,
					BranchName: entry.BranchName,
					Reason:     "story is delivered/accepted but not merged -- branch preserved as the record of the work",
				})
				plan.Summary.BranchesPreserved++
			} else {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionDeleteBranch,
					StoryID:    entry.StoryID,
					BranchName: entry.BranchName,
					Reason:     "snapshot branch cleanup",
				})
				plan.Summary.BranchesDeleted++
			}
		}

		// Story state adjustment -- only if still in-progress in nd
		if !isInProgressInND(entry.StoryID, cfg.InProgressIssues) {
			continue // already closed or moved -- no nd mutation needed
		}

		if hasLabel(entry.NDLabels, "delivered") {
			plan.Actions = append(plan.Actions, RecoverAction{
				Kind:    ActionNoteDelivered,
				StoryID: entry.StoryID,
				Reason:  "story was delivered before context loss -- needs PM review",
			})
			plan.Summary.StoriesDelivered++
		} else {
			plan.Actions = append(plan.Actions, RecoverAction{
				Kind:    ActionResetStory,
				StoryID: entry.StoryID,
				Reason:  "story was in-progress but not delivered -- reset to open",
			})
			plan.Summary.StoriesReset++
		}
	}

	// Orphan worktrees: in git but not in snapshot
	for _, wt := range cfg.CurrentWorktrees {
		if accountedWorktrees[wt.Path] {
			continue
		}
		plan.Actions = append(plan.Actions, RecoverAction{
			Kind:         ActionRemoveWorktree,
			WorktreePath: wt.Path,
			Reason:       "orphan worktree not in snapshot",
		})
		plan.Summary.WorktreesRemoved++
		plan.Summary.OrphanWorktrees++

		if wt.Branch != "" {
			if storyID, ok := storyIDFromStoryBranch(wt.Branch); ok && isDeliveredInND(storyID, cfg.InProgressIssues) {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionPreserveBranch,
					StoryID:    storyID,
					BranchName: wt.Branch,
					Reason:     "story is delivered/accepted but not merged -- branch preserved as the record of the work",
				})
				plan.Summary.BranchesPreserved++
			} else {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionDeleteBranch,
					BranchName: wt.Branch,
					Reason:     "orphan worktree branch cleanup",
				})
				plan.Summary.BranchesDeleted++
			}
		}
	}

	// Stale merged branches: epic/*, story/*, worktree-* fully merged into main.
	// Skip any already scheduled for deletion or preservation above.
	scheduledBranches := make(map[string]bool)
	for _, a := range plan.Actions {
		if a.Kind == ActionDeleteBranch || a.Kind == ActionPreserveBranch {
			scheduledBranches[a.BranchName] = true
		}
	}
	for _, branch := range cfg.StaleBranches {
		if scheduledBranches[branch] {
			continue
		}
		plan.Actions = append(plan.Actions, RecoverAction{
			Kind:       ActionDeleteBranch,
			BranchName: branch,
			Reason:     "stale merged branch cleanup",
		})
		plan.Summary.BranchesDeleted++
		plan.Summary.StaleBranchesDeleted++
	}

	// PM isolation branches: worktree-agent-* are single-use. Delete unconditionally.
	for _, branch := range cfg.PMIsolationBranches {
		if scheduledBranches[branch] {
			continue
		}
		plan.Actions = append(plan.Actions, RecoverAction{
			Kind:       ActionDeleteBranch,
			BranchName: branch,
			Reason:     "PM isolation branch cleanup (single-use, never merged to main)",
		})
		plan.Summary.BranchesDeleted++
	}

	return plan
}

// isInProgressInND checks whether a story is currently in-progress in nd.
func isInProgressInND(storyID string, issues []ndIssue) bool {
	for _, issue := range issues {
		if issue.ID == storyID && strings.EqualFold(issue.Status, "in_progress") {
			return true
		}
	}
	return false
}

// isDeliveredInND reports whether a story carries the delivered or accepted
// label in the live nd state (cfg.InProgressIssues). Such a story's branch
// holds work that has not been merged yet and must not be deleted.
func isDeliveredInND(storyID string, issues []ndIssue) bool {
	if storyID == "" {
		return false
	}
	for _, issue := range issues {
		if issue.ID != storyID {
			continue
		}
		if hasLabel(issue.Labels, "delivered") || hasLabel(issue.Labels, "accepted") {
			return true
		}
	}
	return false
}

// storyIDFromStoryBranch extracts the story ID from a story/<ID> branch name.
func storyIDFromStoryBranch(branch string) (string, bool) {
	const prefix = "story/"
	if !strings.HasPrefix(branch, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(branch, prefix)
	return id, id != ""
}

// BuildRecoverConfig gathers snapshot, worktrees, and nd state into a
// RecoverConfig. If no snapshot exists, SnapshotStories will be nil (recover
// still works -- it cleans orphan worktrees).
func BuildRecoverConfig(projectRoot string) (RecoverConfig, error) {
	var cfg RecoverConfig

	// Read snapshot (optional -- may not exist)
	snap, err := ReadSnapshot(projectRoot)
	if err == nil && snap != nil {
		cfg.SnapshotStories = snap.Stories
	}

	// List current worktrees
	worktrees, err := ListWorktrees(projectRoot)
	if err != nil {
		return cfg, fmt.Errorf("list worktrees: %w", err)
	}
	cfg.CurrentWorktrees = worktrees

	// Query nd for in-progress issues
	issues, err := QueryInProgress(projectRoot)
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("nd state unavailable: %v", err))
	} else {
		cfg.InProgressIssues = issues
	}

	// List stale merged branches (epic/*, story/*, worktree-*)
	staleBranches, err := ListMergedBranches(projectRoot)
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("stale branch detection unavailable: %v", err))
	} else {
		cfg.StaleBranches = staleBranches
	}

	// PM isolation branches: worktree-agent-* are single-use, never merged to main.
	// Always safe to delete unconditionally.
	pmBranches, err := ListWorktreeAgentBranches(projectRoot)
	if err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("PM isolation branch detection unavailable: %v", err))
	} else {
		cfg.PMIsolationBranches = pmBranches
	}

	return cfg, nil
}

// ExecuteRecover performs the recovery actions. Returns non-fatal error strings
// for actions that failed -- cleanup continues regardless.
func ExecuteRecover(projectRoot string, plan RecoverPlan) []string {
	var errors []string

	for _, action := range plan.Actions {
		switch action.Kind {
		case ActionRemoveWorktree:
			result := worktree.SafeRemove(action.WorktreePath)
			if result.Error != "" {
				errors = append(errors, fmt.Sprintf("remove worktree %s: %s", action.WorktreePath, result.Error))
			}

		case ActionDeleteBranch:
			cmd := exec.Command("git", "branch", "-D", action.BranchName)
			cmd.Dir = projectRoot
			if out, err := cmd.CombinedOutput(); err != nil {
				errors = append(errors, fmt.Sprintf("delete branch %s: %s (%v)", action.BranchName, strings.TrimSpace(string(out)), err))
			}

		case ActionResetStory:
			// Mirror runND: resolve the shared vault and anchor the command to
			// the project root so the mutation targets the right nd state.
			vaultDir, err := ndvault.Resolve(projectRoot)
			if err != nil {
				errors = append(errors, fmt.Sprintf("reset story %s: resolve nd vault: %v", action.StoryID, err))
				continue
			}
			cmd := execCommand("nd", "--vault", vaultDir, "update", action.StoryID, "--status", "open")
			cmd.Dir = projectRoot
			if out, err := cmd.CombinedOutput(); err != nil {
				errors = append(errors, fmt.Sprintf("reset story %s: %s (%v)", action.StoryID, strings.TrimSpace(string(out)), err))
			}

		case ActionNoteDelivered:
			// No nd mutation -- story stays in-progress with delivered label.
			// This is informational: the dispatcher should spawn PM-Acceptor next.

		case ActionPreserveBranch:
			// No-op: informational action surfacing why the branch was kept.
		}
	}

	return errors
}
