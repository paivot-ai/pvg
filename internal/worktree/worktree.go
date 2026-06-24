// Package worktree provides safe git worktree operations that resolve the
// project root from the worktree path itself, not from CWD. This prevents
// the CWD-corruption failure mode where removing a worktree while the shell
// CWD is inside it makes the session unrecoverable.
package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RemoveResult is the JSON output of a safe-remove operation.
type RemoveResult struct {
	Removed      bool   `json:"removed"`
	WorktreePath string `json:"worktree_path"`
	ProjectRoot  string `json:"project_root"`
	Pruned       bool   `json:"pruned"`
	Error        string `json:"error,omitempty"`
}

// For testing: allow mocking exec.Command.
var execCommand = exec.Command

// ResolveProjectRoot derives the project root from a worktree path.
// It walks up from the given path looking for a .git directory (file or dir).
// This does NOT use os.Getwd(), making it immune to CWD corruption.
func ResolveProjectRoot(worktreePath string) (string, error) {
	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}

	// Strategy 1: If path contains .claude/worktrees/, the project root is
	// the parent of .claude/. This is the Paivot convention and catches 100%
	// of dispatcher-created worktrees.
	if idx := strings.Index(abs, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); idx >= 0 {
		root := abs[:idx]
		if isGitRoot(root) {
			return root, nil
		}
	}

	// Strategy 2: Use git to resolve the common dir from the worktree path.
	// git rev-parse --git-common-dir returns the shared .git dir.
	// Run with -C to avoid depending on CWD.
	if isDir(abs) {
		cmd := execCommand("git", "-C", abs, "rev-parse", "--git-common-dir")
		out, err := cmd.Output()
		if err == nil {
			commonDir := strings.TrimSpace(string(out))
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(abs, commonDir)
			}
			commonDir = filepath.Clean(commonDir)
			// .git/worktrees/<name> -> .git -> parent is project root
			// .git (if already the git dir) -> parent is project root
			root := filepath.Dir(commonDir)
			if isGitRoot(root) {
				return root, nil
			}
		}
	}

	return "", fmt.Errorf("cannot resolve project root from worktree path %q: no .claude/worktrees/ convention found and git resolution failed", abs)
}

// ownedWorktreeBase returns the directory under the project root that Paivot
// owns for agent worktrees (the allowlist root). It honors the worktree.base
// project setting (default ".claude/worktrees"), joined to root. SafeRemove
// refuses to remove any worktree outside this base -- a worktree at another
// path belongs to another tool and is not Paivot's to delete.
func ownedWorktreeBase(root string) string {
	rel := defaultWorktreeBaseRel
	settingsPath := filepath.Join(root, ".vault", "knowledge", ".settings.yaml")
	if v := strings.TrimSpace(loadSetting(settingsPath, worktreeBaseSettingKey)); v != "" {
		rel = v
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(root, rel))
}

const (
	defaultWorktreeBaseRel = ".claude/worktrees"
	worktreeBaseSettingKey = "worktree.base"
)

// loadSetting reads a single key from a ".settings.yaml"-style file (flat
// "key: value" lines). Returns "" if the file or key is absent. Kept local to
// avoid importing the settings package (which would create an import cycle:
// settings has no dependency on worktree and must stay that way).
func loadSetting(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// SafeRemove removes a git worktree by resolving the project root from the
// worktree path, then running git operations from that root. It always prunes
// stale worktree metadata afterward.
//
// SafeRemove is safe-by-default: it REFUSES to remove any worktree that does
// not carry Paivot's ownership marker (written only by `pvg worktree add`).
// This is the mechanism-layer defense that stops Paivot from ever deleting a
// worktree it does not own -- decided by the marker, not by path, so a foreign
// worktree inside .claude/worktrees/ is still refused. Use SafeRemoveForce for
// emergency removal that bypasses the ownership refusal (the CWD-inside guard
// still applies).
func SafeRemove(worktreePath string) RemoveResult {
	return safeRemove(worktreePath, true)
}

// SafeRemoveForce removes a worktree WITHOUT the ownership refusal. It still
// enforces the CWD-inside guard (which prevents session-fatal CWD corruption).
// Intended only for emergency/manual use via `pvg worktree remove --force`.
func SafeRemoveForce(worktreePath string) RemoveResult {
	return safeRemove(worktreePath, false)
}

func safeRemove(worktreePath string, enforceOwnership bool) RemoveResult {
	result := RemoveResult{
		WorktreePath: worktreePath,
	}

	root, err := ResolveProjectRoot(worktreePath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.ProjectRoot = root

	// Ownership guard (safe-by-default): refuse to remove a worktree that does
	// not carry Paivot's ownership marker. The marker -- written only by
	// `pvg worktree add` (worktree.Add) -- is the SOLE proof of ownership. A
	// worktree without it belongs to another tool or a concurrent non-Paivot
	// session and removing it would destroy work Paivot does not own. This is
	// decided by the marker, NOT by path: a foreign worktree that happens to
	// live inside .claude/worktrees/ is still refused. Runs BEFORE any git
	// remove call. SafeRemoveForce bypasses this guard.
	if enforceOwnership && !IsPaivotOwned(worktreePath) {
		// Surface the managed base as an additional diagnostic, but the decisive
		// refusal is marker-absence, not the path.
		base := ownedWorktreeBase(root)
		result.Error = fmt.Sprintf(
			"REFUSED: %q has no Paivot ownership marker (not created by `pvg worktree add`); refusing to remove a worktree Paivot may not own. Use --force to override. (Paivot's managed base is %q.)",
			worktreePath, base,
		)
		return result
	}

	// CWD safety guard: refuse to remove a worktree if the caller's CWD is
	// inside it, because deleting the directory would permanently break the
	// shell session (all subsequent exec calls fail with ENOENT on the CWD).
	if cwd, err := os.Getwd(); err == nil {
		cwdClean := filepath.Clean(cwd)
		wtClean := filepath.Clean(worktreePath)
		// Resolve relative worktree path from project root, not from CWD.
		// filepath.Abs() is wrong here because CWD may have drifted into the
		// worktree, causing double-nested wrong path resolution.
		if !filepath.IsAbs(wtClean) {
			wtClean = filepath.Join(root, wtClean)
		}
		wtClean = filepath.Clean(wtClean)
		if cwdAbs, err := filepath.Abs(cwdClean); err == nil {
			cwdClean = cwdAbs
		}
		// Resolve symlinks so /tmp vs /private/tmp don't defeat the check.
		if resolved, err := filepath.EvalSymlinks(cwdClean); err == nil {
			cwdClean = resolved
		}
		if resolved, err := filepath.EvalSymlinks(wtClean); err == nil {
			wtClean = resolved
		}
		if cwdClean == wtClean || strings.HasPrefix(cwdClean, wtClean+string(filepath.Separator)) {
			result.Error = fmt.Sprintf(
				"REFUSED: CWD %q is inside worktree %q -- removing it would permanently break this shell session. Run 'cd %s' first, then retry.",
				cwdClean, wtClean, root,
			)
			return result
		}
	}
	// If os.Getwd() fails the CWD is already gone; proceed with removal.

	// Remove the worktree using -C to run from the project root.
	cmd := execCommand("git", "-C", root, "worktree", "remove", "--force", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		outStr := strings.TrimSpace(string(out))
		// If the directory doesn't exist, try prune instead (stale metadata).
		if !isDir(worktreePath) {
			pruneCmd := execCommand("git", "-C", root, "worktree", "prune")
			if _, pruneErr := pruneCmd.CombinedOutput(); pruneErr == nil {
				result.Removed = true
				result.Pruned = true
				return result
			}
		}
		// A dev/PM toolchain CONTAINER that runs as root commonly leaves
		// root-owned build artifacts (_build/, deps/, tmp/) inside the
		// worktree. The unprivileged host process cannot unlink them, so git
		// fails with a permission error. pvg cannot remove root-owned files
		// without elevation, so name the contract and the remediation instead
		// of echoing the bare git error.
		if isPermissionDenied(outStr) {
			result.Error = fmt.Sprintf(
				"git worktree remove failed with a permission error on %q: %s\n"+
					"Cause: root-owned files inside the worktree (a container running as "+
					"root left build artifacts the host user cannot delete).\n"+
					"Fix: re-own the artifacts, then remove and prune, e.g.\n"+
					"  docker run --rm -v %q:/wt alpine:3 chown -R %d:%d /wt\n"+
					"  rm -rf %q && git -C %q worktree prune",
				worktreePath, outStr,
				worktreePath, os.Getuid(), os.Getgid(),
				worktreePath, root,
			)
			return result
		}
		result.Error = fmt.Sprintf("git worktree remove: %s (%v)", outStr, err)
		return result
	}
	result.Removed = true

	// Always prune to clean up any stale metadata.
	pruneCmd := execCommand("git", "-C", root, "worktree", "prune")
	if _, err := pruneCmd.CombinedOutput(); err == nil {
		result.Pruned = true
	}

	return result
}

// FormatJSON returns the result as indented JSON.
func (r RemoveResult) FormatJSON() string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// FormatText returns a human-readable summary.
func (r RemoveResult) FormatText() string {
	if r.Error != "" {
		return fmt.Sprintf("FAIL: %s", r.Error)
	}
	msg := fmt.Sprintf("Removed worktree %s (project root: %s)", r.WorktreePath, r.ProjectRoot)
	if r.Pruned {
		msg += " [pruned]"
	}
	return msg
}

func isGitRoot(path string) bool {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	// .git can be a directory (normal repo) or file (worktree pointer)
	return info.IsDir() || info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isPermissionDenied reports whether git's output indicates an unlink was
// refused by the OS -- the signature of root-owned files inside the worktree
// that an unprivileged process cannot delete.
func isPermissionDenied(output string) bool {
	return strings.Contains(strings.ToLower(output), "permission denied")
}
