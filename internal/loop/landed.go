package loop

import (
	"fmt"
	"strings"
)

// LandedReroute describes one already-landed story rerouted to PM review.
type LandedReroute struct {
	StoryID string
	Epic    string
	Commit  string
}

// ReconcileLanded detects open stories whose work already landed on their
// epic branch (merged by a prior session or platform) and routes them to PM
// review by marking them in_progress + delivered. Without this, the loop
// re-dispatches a developer "from scratch" onto work that is already merged
// -- wasting a full build and risking the developer clobbering the landed
// foundation.
//
// Deliberately NOT an auto-close: landed code may not satisfy every AC, so
// acceptance stays with the PM gate. Hard-TDD stories also get red-approved
// (the landed merge includes the implementation, so the pending review is
// the GREEN one).
func ReconcileLanded(projectRoot string) ([]LandedReroute, error) {
	issues, err := runND(projectRoot, "list", "--status", "open", "--limit", "0", "--json")
	if err != nil {
		return nil, fmt.Errorf("query open work: %w", err)
	}

	var reroutes []LandedReroute
	for _, issue := range issues {
		if strings.EqualFold(issue.Type, "epic") || issue.Parent == "" {
			continue
		}
		if hasLabel(issue.Labels, "delivered") || hasLabel(issue.Labels, "rejected") || hasLabel(issue.Labels, "accepted") {
			continue
		}

		epicBranch := "epic/" + issue.Parent
		if !branchExists(projectRoot, epicBranch) {
			continue
		}
		commit := storyMergeCommit(projectRoot, epicBranch, issue.ID)
		if commit == "" {
			continue
		}

		if err := mutateND(projectRoot, "update", issue.ID, "--status", "in_progress"); err != nil {
			return reroutes, fmt.Errorf("reroute landed story %s: %w", issue.ID, err)
		}
		if err := mutateND(projectRoot, "labels", "add", issue.ID, "delivered"); err != nil {
			return reroutes, fmt.Errorf("mark landed story %s delivered: %w", issue.ID, err)
		}
		if hasLabel(issue.Labels, "hard-tdd") && !hasLabel(issue.Labels, "red-approved") {
			// Best effort: the landed merge contains the implementation, so
			// the pending review is the GREEN one.
			_ = mutateND(projectRoot, "labels", "add", issue.ID, "red-approved")
		}
		_ = mutateND(projectRoot, "comments", "add", issue.ID, fmt.Sprintf(
			"loop: story branch already merged into %s (%s) by a prior session; routed to PM review against the epic branch instead of re-dispatching a developer. PM: review the LANDED code on %s, then accept or reject.",
			epicBranch, commit, epicBranch))

		reroutes = append(reroutes, LandedReroute{StoryID: issue.ID, Epic: epicBranch, Commit: commit})
	}
	return reroutes, nil
}

// branchExists reports whether a local branch ref exists.
func branchExists(projectRoot, branch string) bool {
	cmd := execCommand("git", "-C", projectRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// storyMergeCommit returns the newest commit unique to the epic branch
// whose SUBJECT references the story ID. Matching the bare ID (not just
// story/<ID>) covers merges from other platforms that commit directly with
// subjects like "GREEN: ... (EPIC/STORY)". Subjects only: bodies routinely
// carry "Unblocks: <ID>" trailers naming OTHER stories, which would
// false-positive. A false positive is cheap anyway (one extra PM review
// cycle); a false negative re-dispatches a developer onto landed work.
func storyMergeCommit(projectRoot, branch, storyID string) string {
	// Limit to commits not on main when main exists: landing commits for an
	// active epic live there, and it keeps old main history out of scope.
	rangeSpec := branch
	if branchExists(projectRoot, "main") {
		rangeSpec = "main.." + branch
	}
	cmd := execCommand("git", "-C", projectRoot, "log", rangeSpec, "--format=%h\t%s")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		hash, subject, ok := strings.Cut(line, "\t")
		if ok && subjectContainsStoryToken(subject, storyID) {
			return hash
		}
	}
	return ""
}

// subjectContainsStoryToken reports whether subject contains storyID as a
// whole token: not preceded or followed by [A-Za-z0-9-]. Substring matching
// falsely marked PROJ-a1b delivered when a subject referenced PROJ-a1b2 --
// nd ids share prefixes, so the boundary must exclude id characters.
func subjectContainsStoryToken(subject, storyID string) bool {
	if storyID == "" {
		return false
	}
	for start := 0; ; {
		idx := strings.Index(subject[start:], storyID)
		if idx < 0 {
			return false
		}
		idx += start
		end := idx + len(storyID)
		boundedLeft := idx == 0 || !isStoryIDByte(subject[idx-1])
		boundedRight := end == len(subject) || !isStoryIDByte(subject[end])
		if boundedLeft && boundedRight {
			return true
		}
		start = idx + 1
	}
}

// isStoryIDByte reports whether b belongs to the story-id token alphabet.
func isStoryIDByte(b byte) bool {
	return b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
