package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/ndvault"
)

// OrphanReset describes one story reset by ReconcileOrphans.
type OrphanReset struct {
	StoryID string
	Reason  string
}

// ReconcileOrphans resets in_progress stories left behind by dead sessions
// back to open. A story is an orphan when it is in_progress, NOT labeled
// delivered (delivered+in_progress legitimately waits for PM review with no
// worktree), and has no developer worktree on disk. Without this sweep, a
// crashed session's claims shadow real state forever: the loop counts them
// as active work and never re-dispatches them.
func ReconcileOrphans(projectRoot string) ([]OrphanReset, error) {
	issues, err := QueryInProgress(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("query in-progress work: %w", err)
	}

	var resets []OrphanReset
	for _, issue := range issues {
		if hasLabel(issue.Labels, "delivered") {
			continue
		}
		worktree := filepath.Join(projectRoot, ".claude", "worktrees", "dev-"+issue.ID)
		if _, statErr := os.Stat(worktree); statErr == nil {
			continue
		}

		if err := mutateND(projectRoot, "update", issue.ID, "--status", "open"); err != nil {
			return resets, fmt.Errorf("reset orphaned story %s: %w", issue.ID, err)
		}
		// Best-effort breadcrumb; the reset itself already succeeded.
		_ = mutateND(projectRoot, "comments", "add", issue.ID,
			"loop: reset orphaned in_progress to open (no developer worktree found; prior session presumed dead)")
		resets = append(resets, OrphanReset{
			StoryID: issue.ID,
			Reason:  "in_progress without delivered label and no developer worktree",
		})
	}
	return resets, nil
}

// mutateND runs a state-changing nd command against the shared vault,
// anchored to the project root.
func mutateND(projectRoot string, args ...string) error {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve nd vault: %w", err)
	}
	ndArgs := append([]string{"--vault", vaultDir}, args...)
	cmd := execCommand("nd", ndArgs...)
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nd %s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}
