// Package ndsync mirrors the live nd vault into a git-tracked snapshot
// directory (.vault/backlog-snapshot/) and restores it after a fresh clone
// or vault loss. The live vault lives under the git common dir (or a
// gitignored local .vault/) and is NOT part of git history; the snapshot
// gives the backlog git durability.
package ndsync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// For testing: allow mocking exec.Command.
var execCommand = exec.Command

const snapshotRelDir = ".vault/backlog-snapshot"
const ndConfigName = ".nd.yaml"

const readmeContent = `# Backlog Snapshot

This directory is a point-in-time EXPORT of the live nd vault, committed to
git for durability. It is NOT the live queue: the live queue is the shared
nd vault (resolved by ` + "`pvg nd root`" + `), which lives outside git history.

- Never hand-edit files in this directory. They are overwritten by the next
  export and never read by nd at runtime.
- Refresh this snapshot with ` + "`pvg nd sync`" + `.
- After a fresh clone (or any vault loss), restore the live vault from this
  snapshot with ` + "`pvg nd restore`" + `.
`

// SyncResult summarizes a sync operation.
type SyncResult struct {
	Issues      int    // issue files exported
	Removed     int    // stale snapshot files removed
	SnapshotDir string // absolute snapshot directory
}

// RestoreResult summarizes a restore operation.
type RestoreResult struct {
	Issues         int    // issue files restored
	Replaced       int    // pre-existing live issues removed under --force
	VaultDir       string // absolute live vault directory
	ConfigRestored bool   // .nd.yaml copied because the vault lacked one
}

// SnapshotDir returns the snapshot directory for a project root.
func SnapshotDir(projectRoot string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(snapshotRelDir))
}

// Sync mirrors the live vault's issues/*.md plus its .nd.yaml into the
// snapshot directory. It is a full mirror: snapshot issue files that no
// longer exist in the live vault are deleted. The snapshot README is
// rewritten on every sync.
func Sync(projectRoot, vaultDir string) (SyncResult, error) {
	res := SyncResult{SnapshotDir: SnapshotDir(projectRoot)}

	liveConfig := filepath.Join(vaultDir, ndConfigName)
	if _, err := os.Stat(liveConfig); err != nil {
		return res, fmt.Errorf("nd vault %s is not initialized (%s missing)", vaultDir, ndConfigName)
	}

	snapIssuesDir := filepath.Join(res.SnapshotDir, "issues")
	if err := os.MkdirAll(snapIssuesDir, 0o755); err != nil {
		return res, fmt.Errorf("create snapshot dir: %w", err)
	}

	liveNames, err := issueFiles(filepath.Join(vaultDir, "issues"))
	if err != nil {
		return res, fmt.Errorf("read live issues: %w", err)
	}

	liveSet := make(map[string]bool, len(liveNames))
	for _, name := range liveNames {
		liveSet[name] = true
		src := filepath.Join(vaultDir, "issues", name)
		dst := filepath.Join(snapIssuesDir, name)
		if err := copyFile(src, dst); err != nil {
			return res, fmt.Errorf("export %s: %w", name, err)
		}
		res.Issues++
	}

	snapNames, err := issueFiles(snapIssuesDir)
	if err != nil {
		return res, fmt.Errorf("read snapshot issues: %w", err)
	}
	for _, name := range snapNames {
		if liveSet[name] {
			continue
		}
		if err := os.Remove(filepath.Join(snapIssuesDir, name)); err != nil {
			return res, fmt.Errorf("remove stale snapshot file %s: %w", name, err)
		}
		res.Removed++
	}

	if err := copyFile(liveConfig, filepath.Join(res.SnapshotDir, ndConfigName)); err != nil {
		return res, fmt.Errorf("export %s: %w", ndConfigName, err)
	}

	if err := os.WriteFile(filepath.Join(res.SnapshotDir, "README.md"), []byte(readmeContent), 0o644); err != nil {
		return res, fmt.Errorf("write snapshot README: %w", err)
	}

	return res, nil
}

// Restore copies the snapshot back into the live vault. If the live vault
// already contains issues, it refuses unless force is true (force = the
// snapshot wins and existing issues are replaced). The snapshot's .nd.yaml
// is copied only when the vault lacks one.
func Restore(projectRoot, vaultDir string, force bool) (RestoreResult, error) {
	res := RestoreResult{VaultDir: vaultDir}

	snapDir := SnapshotDir(projectRoot)
	snapIssuesDir := filepath.Join(snapDir, "issues")
	snapNames, err := issueFiles(snapIssuesDir)
	if err != nil {
		return res, fmt.Errorf("read snapshot issues: %w", err)
	}
	if len(snapNames) == 0 {
		return res, fmt.Errorf("no backlog snapshot found at %s (run 'pvg nd sync' first)", snapDir)
	}

	liveIssuesDir := filepath.Join(vaultDir, "issues")
	liveNames, err := issueFiles(liveIssuesDir)
	if err != nil {
		return res, fmt.Errorf("read live issues: %w", err)
	}

	if len(liveNames) > 0 {
		if !force {
			return res, fmt.Errorf(
				"live vault %s already contains %d issue(s); refusing to overwrite the live queue.\nUse 'pvg nd restore --force' to replace it with the snapshot",
				vaultDir, len(liveNames))
		}
		for _, name := range liveNames {
			if err := os.Remove(filepath.Join(liveIssuesDir, name)); err != nil {
				return res, fmt.Errorf("remove live issue %s: %w", name, err)
			}
			res.Replaced++
		}
	}

	if err := os.MkdirAll(liveIssuesDir, 0o755); err != nil {
		return res, fmt.Errorf("create live issues dir: %w", err)
	}

	for _, name := range snapNames {
		src := filepath.Join(snapIssuesDir, name)
		dst := filepath.Join(liveIssuesDir, name)
		if err := copyFile(src, dst); err != nil {
			return res, fmt.Errorf("restore %s: %w", name, err)
		}
		res.Issues++
	}

	liveConfig := filepath.Join(vaultDir, ndConfigName)
	snapConfig := filepath.Join(snapDir, ndConfigName)
	if _, err := os.Stat(liveConfig); os.IsNotExist(err) {
		if _, serr := os.Stat(snapConfig); serr == nil {
			if cerr := copyFile(snapConfig, liveConfig); cerr != nil {
				return res, fmt.Errorf("restore %s: %w", ndConfigName, cerr)
			}
			res.ConfigRestored = true
		}
	}

	return res, nil
}

// CommitSnapshot stages and commits the snapshot directory, making
// `pvg nd sync --commit` one atomic durability step. Returns false with no
// error when the snapshot already matches HEAD -- a perpetually dirty
// tracked snapshot breaks worktree-cleanliness checks mid-loop, so sync
// and commit must never be separated in unattended runs.
func CommitSnapshot(projectRoot string) (bool, error) {
	rel := filepath.FromSlash(snapshotRelDir)

	add := execCommand("git", "-C", projectRoot, "add", "--", rel)
	if out, err := add.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add %s: %w\n%s", rel, err, strings.TrimSpace(string(out)))
	}

	// Exit 0 = no staged changes for the snapshot, nothing to commit.
	diff := execCommand("git", "-C", projectRoot, "diff", "--cached", "--quiet", "--", rel)
	if diff.Run() == nil {
		return false, nil
	}

	commit := execCommand("git", "-C", projectRoot, "commit", "-m", "chore(paivot): backlog snapshot", "--", rel)
	if out, err := commit.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit snapshot: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// issueFiles lists the *.md filenames in dir, sorted. A missing directory
// yields an empty list, not an error.
func issueFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
