package loop

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/ndvault"
	"github.com/paivot-ai/pvg/internal/settings"
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
	// ActionSkipForeignWorktree is informational (no-op in ExecuteRecover): the
	// worktree carries NO Paivot ownership marker (it was not created by
	// `pvg worktree add`), so Paivot does not own it and must NEVER remove it or
	// delete its branch. This holds regardless of path -- including a foreign
	// worktree inside .claude/worktrees/ created by a concurrent non-Paivot
	// session. Surfaced so the operator can see it was preserved.
	ActionSkipForeignWorktree ActionKind = "skip_foreign_worktree"
)

// paivotBranchPrefixes are the only branch-name prefixes Paivot is allowed to
// delete. Any branch that does not match one of these is treated as foreign and
// is preserved -- deleting a branch Paivot does not own could destroy another
// tool's (or the user's) work.
var paivotBranchPrefixes = []string{"story/", "epic/", "worktree-agent-", "worktree-"}

// isPaivotBranch reports whether a branch name matches a Paivot naming scheme
// and is therefore safe for Paivot to delete.
func isPaivotBranch(branch string) bool {
	for _, p := range paivotBranchPrefixes {
		if strings.HasPrefix(branch, p) {
			return true
		}
	}
	return false
}

// isOwnedWorktreePath reports whether worktree path `path` lies within Paivot's
// owned base directory `base`.
//
// DEPRECATED as the ownership authority: ownership is now decided by the
// per-worktree marker (worktree.IsPaivotOwned, resolved into the Owned flag),
// NOT by path. This helper is retained only as a path-segment utility and must
// NOT gate worktree removal or branch deletion -- a path under the base is no
// longer proof of ownership (a concurrent non-Paivot session can create an
// unmarked worktree inside .claude/worktrees/).
//
// If base is empty, it falls back to detecting the Paivot path segment
// <sep>.claude<sep>worktrees<sep> anywhere in the path. Comparison is done on
// filepath.Clean'd paths: the path must equal base exactly or sit under it as
// base+separator prefix.
func isOwnedWorktreePath(path, base string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)

	if base == "" {
		seg := string(filepath.Separator) + ".claude" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
		return strings.Contains(clean+string(filepath.Separator), seg)
	}

	// Resolve symlinks on BOTH sides so that, e.g., macOS's /var -> /private/var
	// indirection (git worktree list reports the resolved path; the configured
	// base may not be) does not make an owned worktree look foreign. Falls back
	// to the cleaned path when a side cannot be resolved (e.g. it does not exist
	// in a pure-function test).
	clean = resolveSymlinks(clean)
	cleanBase := resolveSymlinks(filepath.Clean(base))
	if clean == cleanBase {
		return true
	}
	return strings.HasPrefix(clean, cleanBase+string(filepath.Separator))
}

// resolveSymlinks returns the symlink-resolved form of path, or the longest
// resolvable ancestor with the unresolved tail re-appended. It never fails: a
// fully-unresolvable path is returned cleaned and unchanged. This lets ownership
// comparisons line up across /var vs /private/var even when the leaf directory
// has already been removed.
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	// Walk up to the nearest existing ancestor, resolve it, then re-join the
	// remainder so a not-yet/no-longer-existing leaf still normalizes /private.
	dir := path
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		base := filepath.Base(dir)
		tail = append([]string{base}, tail...)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		dir = parent
	}
	return filepath.Clean(path)
}

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
	// WorktreeBase is the absolute path of the directory Paivot owns and is the
	// ONLY place it may remove worktrees or delete their branches (default
	// <projectRoot>/.claude/worktrees). Empty means "fall back to detecting the
	// .claude/worktrees path segment" -- see isOwnedWorktreePath.
	WorktreeBase string
}

// RecoverSummary counts what recovery did.
type RecoverSummary struct {
	WorktreesRemoved          int `json:"worktrees_removed"`
	BranchesDeleted           int `json:"branches_deleted"`
	BranchesPreserved         int `json:"branches_preserved"`
	StoriesReset              int `json:"stories_reset"`
	StoriesDelivered          int `json:"stories_delivered"`
	OrphanWorktrees           int `json:"orphan_worktrees"`
	StaleBranchesDeleted      int `json:"stale_branches_deleted"`
	ForeignWorktreesPreserved int `json:"foreign_worktrees_preserved"`
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
		// Worktree cleanup -- ONLY if Paivot owns the worktree (it carries
		// Paivot's ownership marker, resolved into entry.Owned by
		// BuildRecoverConfig). A snapshot entry could point at a worktree that a
		// concurrent non-Paivot session created (no marker, possibly even inside
		// .claude/worktrees/); removing it would destroy a worktree Paivot does
		// not own. Surface it as preserved and skip BOTH the worktree removal and
		// the branch deletion below.
		if entry.WorktreePath != "" {
			if !entry.Owned {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:         ActionSkipForeignWorktree,
					StoryID:      entry.StoryID,
					WorktreePath: entry.WorktreePath,
					BranchName:   entry.BranchName,
					Reason:       "worktree has no Paivot ownership marker -- not owned by Paivot, preserved (no removal, no branch deletion)",
				})
				plan.Summary.ForeignWorktreesPreserved++
				accountedWorktrees[entry.WorktreePath] = true
				// Adjust nd story state if needed, but never touch the worktree
				// or branch.
				appendStoryStateAction(&plan, entry, cfg.InProgressIssues)
				continue
			}
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
			} else if isPaivotBranch(entry.BranchName) {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionDeleteBranch,
					StoryID:    entry.StoryID,
					BranchName: entry.BranchName,
					Reason:     "snapshot branch cleanup",
				})
				plan.Summary.BranchesDeleted++
			} else {
				// Snapshot points at a non-Paivot branch name -- never delete a
				// branch Paivot does not own. Preserve it.
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionPreserveBranch,
					StoryID:    entry.StoryID,
					BranchName: entry.BranchName,
					Reason:     "branch name is not a Paivot branch -- preserved (Paivot only deletes story/*, epic/*, worktree-agent-*, worktree-* branches)",
				})
				plan.Summary.BranchesPreserved++
			}
		}

		// Story state adjustment -- only if still in-progress in nd.
		appendStoryStateAction(&plan, entry, cfg.InProgressIssues)
	}

	// Orphan worktrees: in git but not in snapshot.
	for _, wt := range cfg.CurrentWorktrees {
		if accountedWorktrees[wt.Path] {
			continue
		}

		// Ownership gate: Paivot may remove a worktree (and delete its branch)
		// ONLY when the worktree carries Paivot's ownership marker (resolved into
		// wt.Owned by BuildRecoverConfig). A worktree without the marker --
		// created by another tool (.codex-worktrees/, an external absolute path)
		// OR by a concurrent non-Paivot session even inside .claude/worktrees/ --
		// is left COMPLETELY untouched: no removal, no branch deletion, just an
		// informational note.
		if !wt.Owned {
			plan.Actions = append(plan.Actions, RecoverAction{
				Kind:         ActionSkipForeignWorktree,
				WorktreePath: wt.Path,
				BranchName:   wt.Branch,
				Reason:       "worktree has no Paivot ownership marker -- preserved (no removal, no branch deletion)",
			})
			plan.Summary.ForeignWorktreesPreserved++
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
			} else if isPaivotBranch(wt.Branch) {
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionDeleteBranch,
					BranchName: wt.Branch,
					Reason:     "orphan worktree branch cleanup",
				})
				plan.Summary.BranchesDeleted++
			} else {
				// The worktree is owned, but its branch is NOT a Paivot branch.
				// Never delete a branch Paivot does not own -- preserve it.
				plan.Actions = append(plan.Actions, RecoverAction{
					Kind:       ActionPreserveBranch,
					BranchName: wt.Branch,
					Reason:     "branch name is not a Paivot branch -- preserved (Paivot only deletes story/*, epic/*, worktree-agent-*, worktree-* branches)",
				})
				plan.Summary.BranchesPreserved++
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

// appendStoryStateAction adjusts the nd story state for a snapshot entry,
// independent of any worktree/branch handling. It mutates nd state only when
// the story is still in-progress in nd: a delivered story is flagged for PM
// review (ActionNoteDelivered), otherwise it is reset to open (ActionResetStory).
func appendStoryStateAction(plan *RecoverPlan, entry SnapshotEntry, issues []ndIssue) {
	if !isInProgressInND(entry.StoryID, issues) {
		return // already closed or moved -- no nd mutation needed
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

// defaultWorktreeBase is the directory under the project root that Paivot owns
// for its agent worktrees, and the ONLY place recover may remove worktrees.
const defaultWorktreeBase = ".claude/worktrees"

// worktreeBaseSetting is the settings key that overrides the owned worktree
// base (e.g. so the codex variant can own .codex-worktrees instead). Empty
// value or missing setting falls back to defaultWorktreeBase.
const worktreeBaseSetting = "worktree.base"

// ResolveWorktreeBase returns the absolute path of the directory Paivot owns
// for agent worktrees. It honors the worktree.base project setting (default
// ".claude/worktrees"), joined to projectRoot. This is the allowlist root used
// by recover and SafeRemove to decide ownership: worktrees outside it are
// foreign and must never be removed.
func ResolveWorktreeBase(projectRoot string) string {
	rel := defaultWorktreeBase
	settingsPath := filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml")
	if v := strings.TrimSpace(settings.LoadFile(settingsPath)[worktreeBaseSetting]); v != "" {
		rel = v
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(projectRoot, rel))
}

// BuildRecoverConfig gathers snapshot, worktrees, and nd state into a
// RecoverConfig. If no snapshot exists, SnapshotStories will be nil (recover
// still works -- it cleans ONLY Paivot-owned orphan worktrees).
func BuildRecoverConfig(projectRoot string) (RecoverConfig, error) {
	var cfg RecoverConfig

	// Resolve the owned worktree base up front -- the allowlist that bounds
	// every worktree removal and branch deletion to Paivot's own directory.
	cfg.WorktreeBase = ResolveWorktreeBase(projectRoot)

	// Read snapshot (optional -- may not exist)
	snap, err := ReadSnapshot(projectRoot)
	if err == nil && snap != nil {
		cfg.SnapshotStories = snap.Stories
		// Resolve ownership (I/O) for each snapshot worktree so the pure
		// EvaluateRecover can treat an unmarked snapshot worktree as
		// foreign/preserved without doing any I/O itself.
		for i := range cfg.SnapshotStories {
			if path := cfg.SnapshotStories[i].WorktreePath; path != "" {
				cfg.SnapshotStories[i].Owned = worktree.IsPaivotOwned(path)
			}
		}
	}

	// List current worktrees
	worktrees, err := ListWorktrees(projectRoot)
	if err != nil {
		return cfg, fmt.Errorf("list worktrees: %w", err)
	}
	// Resolve ownership (I/O) for every current worktree: a worktree is owned
	// IFF it carries Paivot's ownership marker (written only by `pvg worktree
	// add`). Unmarked worktrees -- including any a concurrent non-Paivot session
	// created inside .claude/worktrees/ -- are foreign and must be preserved.
	for i := range worktrees {
		worktrees[i].Owned = worktree.IsPaivotOwned(worktrees[i].Path)
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

		case ActionSkipForeignWorktree:
			// No-op: informational action surfacing a worktree Paivot does not
			// own (outside its owned base). It is deliberately left untouched --
			// no worktree removal, no branch deletion.
		}
	}

	return errors
}
