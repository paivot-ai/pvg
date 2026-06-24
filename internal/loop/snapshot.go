package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const snapshotFile = ".piv-loop-snapshot.json"

// Worktree represents a single git worktree parsed from porcelain output.
//
// Owned records whether the worktree carries Paivot's ownership marker
// (worktree.IsPaivotOwned). It is resolved in BuildRecoverConfig (I/O), NOT in
// the pure EvaluateRecover. Ownership is the SOLE license to remove a worktree
// or delete its branch: an unmarked worktree is foreign and must be preserved,
// regardless of its path (so a foreign worktree inside .claude/worktrees/ is
// still preserved) or branch name.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Owned  bool   `json:"owned"`
}

// SnapshotEntry records the state of one story at snapshot time.
//
// Owned is an eval-time field (json:"-" -- never persisted to the snapshot
// file). BuildRecoverConfig resolves it from the worktree's ownership marker so
// the pure EvaluateRecover can treat an unmarked snapshot worktree as
// foreign/preserved without doing any I/O itself.
type SnapshotEntry struct {
	StoryID      string   `json:"story_id"`
	NDStatus     string   `json:"nd_status"`
	NDLabels     []string `json:"nd_labels,omitempty"`
	AgentType    string   `json:"agent_type,omitempty"`
	WorktreePath string   `json:"worktree_path,omitempty"`
	BranchName   string   `json:"branch_name,omitempty"`
	Owned        bool     `json:"-"`
}

// Snapshot is the full checkpoint written before compaction or context loss.
type Snapshot struct {
	TakenAt string          `json:"taken_at"`
	Stories []SnapshotEntry `json:"stories"`
}

// SnapshotPath returns the full path to the snapshot file.
func SnapshotPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vault", snapshotFile)
}

// SnapshotFileName returns the snapshot file basename (for guard exemption checks).
func SnapshotFileName() string {
	return snapshotFile
}

// ReadSnapshot reads the snapshot from disk.
func ReadSnapshot(projectRoot string) (*Snapshot, error) {
	path := SnapshotPath(projectRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	return &snap, nil
}

// WriteSnapshot persists the snapshot to disk.
func WriteSnapshot(projectRoot string, snap *Snapshot) error {
	path := SnapshotPath(projectRoot)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// RemoveSnapshot deletes the snapshot file. No-op if it doesn't exist.
func RemoveSnapshot(projectRoot string) error {
	path := SnapshotPath(projectRoot)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListWorktrees shells out to git and returns non-main worktrees.
func ListWorktrees(projectRoot string) ([]Worktree, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreePorcelain(string(out)), nil
}

// parseWorktreePorcelain parses porcelain output from git worktree list.
// Skips the first block (main worktree).
func parseWorktreePorcelain(output string) []Worktree {
	if strings.TrimSpace(output) == "" {
		return nil
	}

	// Split into blocks separated by blank lines
	blocks := splitWorktreeBlocks(output)

	// Skip the first block (main worktree)
	if len(blocks) <= 1 {
		return nil
	}

	var worktrees []Worktree
	for _, block := range blocks[1:] {
		wt := parseOneWorktreeBlock(block)
		if wt.Path != "" {
			worktrees = append(worktrees, wt)
		}
	}
	return worktrees
}

// splitWorktreeBlocks splits porcelain output into blocks separated by blank lines.
func splitWorktreeBlocks(output string) [][]string {
	lines := strings.Split(output, "\n")
	var blocks [][]string
	var current []string

	for _, line := range lines {
		if line == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
		} else {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

// parseOneWorktreeBlock extracts Path and Branch from a porcelain block.
func parseOneWorktreeBlock(lines []string) Worktree {
	var wt Worktree
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			wt.Path = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			// branch refs/heads/foo -> foo
			ref := strings.TrimPrefix(line, "branch ")
			wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
		// "detached" lines have no branch -- Branch stays empty
	}
	return wt
}

// ListMergedBranches returns local branches matching epic/*, story/*, or
// worktree-* that are fully merged into main. These are stale leftovers
// from completed work that should be cleaned up.
func ListMergedBranches(projectRoot string) ([]string, error) {
	cmd := exec.Command("git", "branch", "--merged", "main")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch --merged: %w", err)
	}
	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" || branch == "main" || strings.HasPrefix(branch, "* ") {
			continue
		}
		if strings.HasPrefix(branch, "epic/") ||
			strings.HasPrefix(branch, "story/") ||
			strings.HasPrefix(branch, "worktree-") {
			branches = append(branches, branch)
		}
	}
	return branches, nil
}

// ListWorktreeAgentBranches returns all local branches matching worktree-agent-*.
// These are single-use PM isolation branches created by Claude Code's
// isolation: "worktree" feature. Unlike story/epic branches, they are never
// merged into main -- so ListMergedBranches cannot catch them. They are always
// safe to delete after the PM agent completes.
func ListWorktreeAgentBranches(projectRoot string) ([]string, error) {
	cmd := exec.Command("git", "branch", "--list", "worktree-agent-*")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch --list worktree-agent-*: %w", err)
	}
	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(line)
		// Strip leading "* " or "+ " markers from git branch output
		branch = strings.TrimPrefix(branch, "* ")
		branch = strings.TrimPrefix(branch, "+ ")
		branch = strings.TrimSpace(branch)
		if branch != "" {
			branches = append(branches, branch)
		}
	}
	return branches, nil
}

// EpicBranchExists checks whether a local branch named epic/<epicID> exists.
// Returns false on any error (fail open).
func EpicBranchExists(projectRoot, epicID string) bool {
	if epicID == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "epic/"+epicID)
	cmd.Dir = projectRoot
	return cmd.Run() == nil
}

// BuildSnapshot queries nd for in-progress issues, lists worktrees, and
// assembles a snapshot. agentAssignments maps story IDs to agent types
// (e.g. "PROJ-a1b" -> "developer"). It is optional.
func BuildSnapshot(projectRoot string, agentAssignments map[string]string) (*Snapshot, error) {
	issues, err := QueryInProgress(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("query in-progress issues: %w", err)
	}

	worktrees, err := ListWorktrees(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	// Index worktrees by branch for matching
	branchToWT := make(map[string]Worktree, len(worktrees))
	for _, wt := range worktrees {
		if wt.Branch != "" {
			branchToWT[wt.Branch] = wt
		}
	}

	snap := &Snapshot{
		TakenAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, issue := range issues {
		entry := SnapshotEntry{
			StoryID:  issue.ID,
			NDStatus: issue.Status,
			NDLabels: issue.Labels,
		}

		if agentAssignments != nil {
			entry.AgentType = agentAssignments[issue.ID]
		}

		// Try to match a worktree branch to this story ID.
		// Convention: branch contains the story ID (e.g. "worktree-agent-PROJ-a1b").
		for branch, wt := range branchToWT {
			if strings.Contains(branch, issue.ID) {
				entry.WorktreePath = wt.Path
				entry.BranchName = branch
				break
			}
		}

		snap.Stories = append(snap.Stories, entry)
	}

	return snap, nil
}
