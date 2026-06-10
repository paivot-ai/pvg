package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/ndvault"
)

var gitIntegrationRe = regexp.MustCompile(`(?:^|[;&|]\s*)(?:\S*/)?git\s+(merge|pull|rebase|cherry-pick)\b`)
var storyRefRe = regexp.MustCompile(`(?:^|[\s"'=])(?:refs/(?:remotes/origin|heads)/|origin/)?story/([A-Za-z0-9._-]+)`)
var shaRefRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// gitCheckoutRe matches git checkout commands. Handles both:
//
//	git checkout <branch>
//	git checkout -b <branch> [start-point]
//	git checkout -B <branch> [start-point]
var gitCheckoutRe = regexp.MustCompile(`(?:^|[;&|]\s*)(?:\S*/)?git\s+checkout\s+(?:-[bB]\s+)?([\w/.-]+)`)

const mergeGateBlockMsg = "BLOCKED: Cannot merge story branch before PM-Acceptor completion.\n\n" +
	"Story %s must be both labeled 'accepted' and closed in nd before merge.\n" +
	"The merge gate requires PM-Acceptor to fully finish review first.\n\n" +
	"Workflow: Developer marks delivered -> PM-Acceptor reviews -> PM-Acceptor closes story -> PM-Acceptor adds 'accepted' label -> Dispatcher merges.\n\n" +
	"To proceed:\n" +
	"  1. Ensure the story has the 'delivered' label\n" +
	"  2. Spawn paivot-graph:pm agent to review the story\n" +
	"  3. PM-Acceptor will add 'accepted' and close the story\n" +
	"  4. Then merge the story branch"

const projectSettingsPath = ".vault/knowledge/.settings.yaml"

// CheckMergeGate blocks git merge of story branches when the story has not been
// fully accepted by PM-Acceptor. Active for Paivot-managed repos, not just when
// dispatcher mode is currently enabled.
//
// Compatibility behavior:
// - If the repo does not look Paivot-managed, allow.
// - If the story issue cannot be found, allow.
// - Once an nd issue is found for the story, require accepted label + closed status.
func CheckMergeGate(projectRoot, command string) Result {
	if projectRoot == "" || command == "" {
		return Result{Allowed: true}
	}

	if !mergeGateEnabled(projectRoot) {
		return Result{Allowed: true}
	}

	storyIDs := parseStoryRefs(command)
	for _, storyID := range resolveStoryRefs(projectRoot, command) {
		if !containsStoryID(storyIDs, storyID) {
			storyIDs = append(storyIDs, storyID)
		}
	}
	if len(storyIDs) == 0 {
		return Result{Allowed: true}
	}

	for _, storyID := range storyIDs {
		labels := ReadIssueLabels(projectRoot, storyID)
		if labels == nil {
			// No matching nd issue: likely not a Paivot-managed story branch.
			continue
		}

		hasAccepted := false
		for _, label := range labels {
			if label == "accepted" {
				hasAccepted = true
				break
			}
		}

		if !hasAccepted {
			return Result{
				Allowed: false,
				Reason:  fmt.Sprintf(mergeGateBlockMsg, storyID),
			}
		}

		if status := ReadIssueStatus(projectRoot, storyID); status != "closed" {
			return Result{
				Allowed: false,
				Reason:  fmt.Sprintf(mergeGateBlockMsg, storyID),
			}
		}

		parentEpic := ReadIssueParent(projectRoot, storyID)
		issueType := ReadIssueType(projectRoot, storyID)

		// Determine effective target branch: if the command contains
		// "git checkout <branch>" before the merge, use that branch
		// instead of the current HEAD (which hasn't changed yet because
		// the PreToolUse hook fires before the command executes).
		targetBranch, ok := effectiveTargetBranch(projectRoot, command)
		if ok {
			if !strings.HasPrefix(targetBranch, "epic/") {
				// Bugs without a parent epic are allowed to merge into main.
				if issueType == "bug" && parentEpic == "" && targetBranch == "main" {
					continue
				}
				next := "the owning epic branch"
				if parentEpic != "" {
					next = "epic/" + parentEpic
				}
				return Result{
					Allowed: false,
					Reason: fmt.Sprintf(
						"BLOCKED: story branches may only merge into epic branches.\n\nCurrent branch: %s\nAttempted story branch: story/%s\n\nCheckout %s before merging the accepted story branch.",
						targetBranch, storyID, next,
					),
				}
			}

			if parentEpic == "" {
				// Bugs without a parent epic are allowed to merge into any epic branch.
				if issueType == "bug" {
					continue
				}
				return Result{
					Allowed: false,
					Reason: fmt.Sprintf(
						"BLOCKED: Cannot verify merge target for story/%s.\n\nThe nd issue is accepted and closed, but it has no parent epic recorded. Set the story parent before merging.",
						storyID,
					),
				}
			}

			expectedBranch := "epic/" + parentEpic
			if targetBranch != expectedBranch {
				return Result{
					Allowed: false,
					Reason: fmt.Sprintf(
						"BLOCKED: story branches must merge into their owning epic branch.\n\nCurrent branch: %s\nExpected branch: %s\nAttempted story branch: story/%s",
						targetBranch, expectedBranch, storyID,
					),
				}
			}
		}
	}

	return Result{
		Allowed: true,
	}
}

func parseStoryRefs(command string) []string {
	if !gitIntegrationRe.MatchString(command) {
		return nil
	}

	var storyIDs []string
	seen := make(map[string]bool)
	for _, match := range storyRefRe.FindAllStringSubmatch(command, -1) {
		if len(match) < 2 {
			continue
		}
		storyID := strings.TrimSpace(match[1])
		if storyID == "" || seen[storyID] {
			continue
		}
		seen[storyID] = true
		storyIDs = append(storyIDs, storyID)
	}
	return storyIDs
}

func resolveStoryRefs(projectRoot, command string) []string {
	if projectRoot == "" || !gitIntegrationRe.MatchString(command) {
		return nil
	}

	var storyIDs []string
	seen := make(map[string]bool)
	for _, ref := range shaRefRe.FindAllString(command, -1) {
		for _, storyID := range storyIDsForRef(projectRoot, ref) {
			if storyID == "" || seen[storyID] {
				continue
			}
			seen[storyID] = true
			storyIDs = append(storyIDs, storyID)
		}
	}
	return storyIDs
}

func storyIDsForRef(projectRoot, ref string) []string {
	cmd := exec.Command("git", "branch", "-a", "--contains", ref, "--format", "%(refname:short)")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var storyIDs []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" {
			continue
		}
		storyID := storyIDFromBranch(branch)
		if storyID == "" || seen[storyID] {
			continue
		}
		seen[storyID] = true
		storyIDs = append(storyIDs, storyID)
	}
	return storyIDs
}

func storyIDFromBranch(branch string) string {
	for _, prefix := range []string{"story/", "origin/story/", "remotes/origin/story/"} {
		if strings.HasPrefix(branch, prefix) {
			return strings.TrimPrefix(branch, prefix)
		}
	}
	return ""
}

func containsStoryID(storyIDs []string, target string) bool {
	for _, storyID := range storyIDs {
		if storyID == target {
			return true
		}
	}
	return false
}

func mergeGateEnabled(projectRoot string) bool {
	if isLoopActiveFrom(projectRoot) {
		return true
	}

	state, _, err := dispatcher.ReadStateRoot(projectRoot)
	if err == nil && state.Enabled {
		return true
	}

	_, root, found := findAncestorPath(projectRoot, filepath.Join(".vault", "knowledge", ".settings.yaml"))
	return found && root != ""
}

// ReadIssueLabels reads labels from an nd issue's frontmatter.
// Returns nil on any error (fail-open). Returns empty slice if no labels.
func ReadIssueLabels(projectRoot, issueID string) []string {
	frontmatter, ok := readIssueFrontmatter(projectRoot, issueID)
	if !ok {
		return nil
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "labels:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "labels:"))
		return parseYAMLArray(value)
	}
	return []string{}
}

// ReadIssueParent reads the parent epic ID from an nd issue's frontmatter.
// Returns "" on any error or when no parent is recorded.
func ReadIssueParent(projectRoot, issueID string) string {
	frontmatter, ok := readIssueFrontmatter(projectRoot, issueID)
	if !ok {
		return ""
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "parent:") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "parent:")), `"'`)
	}
	return ""
}

func issuePath(projectRoot, issueID string) (string, error) {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(vaultDir, "issues", issueID+".md"), nil
}

func readIssueFrontmatter(projectRoot, issueID string) (string, bool) {
	path, err := issuePath(projectRoot, issueID)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return "", false
	}
	end := strings.Index(content[3:], "---")
	if end == -1 {
		return "", false
	}
	return content[3 : 3+end], true
}

// parseYAMLArray parses a YAML inline array like [a, b, c] into a string slice.
func parseYAMLArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return []string{}
	}

	// Strip brackets
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")

	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		// Strip quotes
		item = strings.Trim(item, `"'`)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func currentBranch(projectRoot string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", false
	}
	return branch, true
}

// effectiveTargetBranch returns the branch that will be current when the merge
// executes. If the command contains "git checkout <branch> && git merge ...",
// the checkout target is returned instead of the actual current branch. This
// fixes the PreToolUse timing issue where the hook fires before the chained
// command executes.
func effectiveTargetBranch(projectRoot, command string) (string, bool) {
	if loc := gitCheckoutRe.FindStringSubmatchIndex(command); loc != nil && loc[2] >= 0 {
		target := strings.TrimSpace(command[loc[2]:loc[3]])
		// Only trust the checkout target if it looks like a branch name
		// (not a flag like -b or --detach)
		if target != "" && !strings.HasPrefix(target, "-") {
			// Only trust the checkout when it precedes the merge and is
			// &&-chained to it. With ';' (or '|', '&', newline) the merge
			// still runs after a FAILED checkout -- e.g. a dirty working
			// tree aborts the checkout and the story merges onto whatever
			// branch HEAD was actually on (this landed a story on main).
			// gitIntegrationRe's match starts ON the separator char, so a
			// "&&" pair straddles the slice boundary -- include the matched
			// char so the pair is seen whole.
			if mloc := gitIntegrationRe.FindStringIndex(command); mloc != nil && mloc[0] > loc[1] && safeAndChain(command[loc[1]:mloc[0]+1]) {
				return target, true
			}
			return currentBranch(projectRoot)
		}
	}
	return currentBranch(projectRoot)
}

// safeAndChain reports whether a shell command fragment contains only
// separators that stop execution on failure (&&). Any ';', '|', newline,
// or single '&' means a later command runs even when an earlier one fails.
func safeAndChain(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';', '\n', '|':
			return false
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				i++ // consume the pair
				continue
			}
			return false // single & backgrounds the checkout
		}
	}
	return true
}

// ReadIssueType reads the type from an nd issue's frontmatter.
// Returns "" on any error or when no type is recorded.
func ReadIssueType(projectRoot, issueID string) string {
	frontmatter, ok := readIssueFrontmatter(projectRoot, issueID)
	if !ok {
		return ""
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "type:") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "type:")), `"'`)
	}
	return ""
}

func isLoopActiveFrom(projectRoot string) bool {
	path, root, found := findAncestorPath(projectRoot, filepath.Join(".vault", loop.StateFileName()))
	if !found || root == "" || path == "" {
		return false
	}
	return loop.IsActive(root)
}

func findAncestorPath(start, rel string) (string, string, bool) {
	dir := filepath.Clean(start)
	for {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}
