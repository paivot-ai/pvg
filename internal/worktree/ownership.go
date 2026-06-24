package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ownershipMarkerName is the basename of the marker file Paivot writes into a
// worktree's git admin dir to claim ownership. Its EXISTENCE is what proves
// ownership; the JSON content is diagnostic only.
const ownershipMarkerName = "paivot-owned"

// OwnershipMarker is the small JSON payload written to the ownership marker
// file. It exists for diagnostics -- recover and SafeRemove only test for the
// file's presence, never its content.
type OwnershipMarker struct {
	Owner     string `json:"owner"`
	StoryID   string `json:"story_id"`
	CreatedAt string `json:"created_at"`
}

// AddResult is the JSON/text output of a `pvg worktree add` operation.
type AddResult struct {
	Added        bool   `json:"added"`
	WorktreePath string `json:"worktree_path"`
	ProjectRoot  string `json:"project_root"`
	Branch       string `json:"branch"`
	StoryID      string `json:"story_id,omitempty"`
	MarkerPath   string `json:"marker_path,omitempty"`
	Error        string `json:"error,omitempty"`
}

// resolveAdminDir returns the absolute git admin directory of the worktree at
// worktreePath -- i.e. `.git/worktrees/<name>` for a linked worktree, or `.git`
// for the main checkout. It runs `git -C <path> rev-parse --git-dir` from the
// worktree path so it does NOT depend on the caller's CWD, and joins a relative
// result back onto the worktree path. Returns an error if the directory does
// not exist or git cannot resolve it (e.g. the worktree was already deleted).
func resolveAdminDir(worktreePath string) (string, error) {
	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	cmd := execCommand("git", "-C", abs, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-dir in %q: %w", abs, err)
	}
	adminDir := strings.TrimSpace(string(out))
	if adminDir == "" {
		return "", fmt.Errorf("git rev-parse --git-dir returned empty for %q", abs)
	}
	// git may report a relative admin dir (relative to the worktree path).
	if !filepath.IsAbs(adminDir) {
		adminDir = filepath.Join(abs, adminDir)
	}
	return filepath.Clean(adminDir), nil
}

// ownershipMarkerPath returns the absolute path of the ownership marker for the
// worktree at worktreePath, resolving its git admin dir first.
func ownershipMarkerPath(worktreePath string) (string, error) {
	adminDir, err := resolveAdminDir(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(adminDir, ownershipMarkerName), nil
}

// IsPaivotOwned reports whether the worktree at worktreePath carries Paivot's
// ownership marker. Ownership is decided SOLELY by the presence of the marker
// file in the worktree's git admin dir -- never by path and never by branch
// name. A worktree Paivot created via Add (or WriteOwnershipMarker) is owned;
// any other worktree (created by `git worktree add` directly, by another tool,
// or by a concurrent non-Paivot session even inside .claude/worktrees/) is
// foreign.
//
// It fails CLOSED: any error (git unavailable, the worktree directory already
// removed so rev-parse fails, the marker simply absent) yields false, so an
// unmarked or unresolvable worktree is treated as foreign and preserved. When
// the worktree directory no longer exists there is nothing to destroy, so a
// false result is safe -- removal degrades to a metadata prune.
func IsPaivotOwned(worktreePath string) bool {
	markerPath, err := ownershipMarkerPath(worktreePath)
	if err != nil {
		return false
	}
	info, err := os.Stat(markerPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// WriteOwnershipMarker stamps Paivot's ownership marker into the git admin dir
// of the worktree at worktreePath. storyID is recorded for diagnostics and may
// be empty. The marker lives in the admin dir so git manages its lifecycle:
// `git worktree remove`/`prune` deletes the admin dir and the marker with it.
func WriteOwnershipMarker(worktreePath, storyID string) error {
	markerPath, err := ownershipMarkerPath(worktreePath)
	if err != nil {
		return err
	}
	marker := OwnershipMarker{
		Owner:     "paivot",
		StoryID:   storyID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ownership marker: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(markerPath, data, 0644); err != nil {
		return fmt.Errorf("write ownership marker %q: %w", markerPath, err)
	}
	return nil
}

// Add creates a git worktree at path on branch (running `git -C <root> worktree
// add <path> <branch>`), then stamps Paivot's ownership marker into the new
// worktree's git admin dir. The marker is what licenses recover/SafeRemove to
// remove the worktree later; without it the worktree would be treated as
// foreign and preserved.
//
// If `git worktree add` fails, Add returns the error and writes NO marker. If
// the worktree is created but the marker write fails, the result reports Added
// with the error so the caller can surface that the worktree exists but is
// unmarked (and therefore will not be auto-cleaned).
func Add(root, path, branch, storyID string) AddResult {
	result := AddResult{
		WorktreePath: path,
		ProjectRoot:  root,
		Branch:       branch,
		StoryID:      storyID,
	}

	cmd := execCommand("git", "-C", root, "worktree", "add", path, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		result.Error = fmt.Sprintf("git worktree add: %s (%v)", strings.TrimSpace(string(out)), err)
		return result
	}
	result.Added = true

	if markerPath, err := ownershipMarkerPath(path); err == nil {
		result.MarkerPath = markerPath
	}
	if err := WriteOwnershipMarker(path, storyID); err != nil {
		result.Error = fmt.Sprintf("worktree created but ownership marker not written: %v", err)
		return result
	}

	return result
}

// FormatJSON returns the AddResult as indented JSON.
func (r AddResult) FormatJSON() string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// FormatText returns a human-readable summary of the AddResult.
func (r AddResult) FormatText() string {
	if r.Error != "" {
		return fmt.Sprintf("FAIL: %s", r.Error)
	}
	msg := fmt.Sprintf("Added worktree %s on %s (project root: %s)", r.WorktreePath, r.Branch, r.ProjectRoot)
	msg += " [ownership marker stamped]"
	return msg
}
