package loop

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/paivot-ai/pvg/internal/ndvault"
)

// WorkCounts holds the counts of issues in each state.
type WorkCounts struct {
	Ready      int
	Delivered  int
	Rejected   int
	InProgress int
	Blocked    int
	Other      int
}

// ndIssue matches the PascalCase JSON output of nd.
type ndIssue struct {
	ID       string   `json:"ID"`
	Title    string   `json:"Title"`
	Status   string   `json:"Status"`
	Parent   string   `json:"Parent"`
	Labels   []string `json:"Labels"`
	Type     string   `json:"Type"`
	Priority int      `json:"Priority"`
}

var execCommand = exec.Command

// QueryWorkCounts returns work counts across the live backlog.
//
// Even in epic mode, stop decisions stay backlog-wide so the loop can continue
// past a single epic instead of terminating when that epic empties.
func QueryWorkCounts(projectRoot, mode, targetEpic string) (WorkCounts, error) {
	return queryAllCounts(projectRoot)
}

// queryAllCounts uses nd subcommands to gather counts across the whole backlog.
//
// The delivered label is AUTHORITATIVE regardless of status: a story whose
// developer added the label without claiming it (status open + delivered)
// is awaiting PM review, not ready work. Bucketing it as ready would
// re-dispatch a developer on already-delivered work forever.
func queryAllCounts(projectRoot string) (WorkCounts, error) {
	var wc WorkCounts

	// nd list caps results at 50 unless --limit is explicit; pass --limit 0
	// so counts stay correct on backlogs over 50 issues.
	deliveredIssues, err := runND(projectRoot, "list", "--status", "!closed", "--label", "delivered", "--limit", "0", "--json")
	if err != nil {
		return wc, fmt.Errorf("query delivered work: %w", err)
	}
	wc.Delivered = len(deliveredIssues)

	readyIssues, err := runND(projectRoot, "ready", "--json")
	if err != nil {
		return wc, fmt.Errorf("query ready work: %w", err)
	}
	readyIssues = filterOutLabel(readyIssues, "delivered")
	wc.Ready = len(readyIssues)

	ipIssues, err := runND(projectRoot, "list", "--status", "in_progress", "--limit", "0", "--json")
	if err != nil {
		return wc, fmt.Errorf("query in-progress work: %w", err)
	}
	for _, issue := range ipIssues {
		if !hasLabel(issue.Labels, "delivered") {
			wc.InProgress++
		}
	}

	rejectedIssues, err := runND(projectRoot, "list", "--status", "open", "--label", "rejected", "--limit", "0", "--json")
	if err != nil {
		return wc, fmt.Errorf("query rejected work: %w", err)
	}
	wc.Rejected = len(filterOutLabel(rejectedIssues, "delivered"))

	// Blocked issues
	blockedIssues, err := runND(projectRoot, "blocked", "--json")
	if err != nil {
		return wc, fmt.Errorf("query blocked work: %w", err)
	}
	wc.Blocked = len(blockedIssues)

	allIssues, err := runND(projectRoot, "list", "--status", "!closed", "--limit", "0", "--json")
	if err != nil {
		return wc, fmt.Errorf("query non-closed work: %w", err)
	}
	wc.Other = countOtherIssues(append(append([]ndIssue{}, readyIssues...), deliveredIssues...), ipIssues, blockedIssues, allIssues)

	return wc, nil
}

// queryEpicCounts uses nd children to count work within a specific epic.
//
// nd has 5 statuses: open, in_progress, blocked, deferred, closed. There is
// no "ready" status -- readiness is a graph property. An open child with open
// blockers (graph-blocked) still has status "open", so the graph-blocked set
// is fetched via `nd blocked --json` to classify open children as Ready vs
// Blocked. Without this, dependency-blocked epics would never surface as
// epic_blocked.
func queryEpicCounts(projectRoot, epicID string) (WorkCounts, error) {
	var wc WorkCounts

	issues, err := runND(projectRoot, "children", epicID, "--json")
	if err != nil {
		return wc, fmt.Errorf("query epic children: %w", err)
	}

	blockedIssues, err := runND(projectRoot, "blocked", "--json")
	if err != nil {
		return wc, fmt.Errorf("query blocked set: %w", err)
	}
	blockedSet := make(map[string]bool, len(blockedIssues))
	for _, issue := range blockedIssues {
		blockedSet[issue.ID] = true
	}

	for _, issue := range issues {
		switch strings.ToLower(issue.Status) {
		case "in_progress":
			if hasLabel(issue.Labels, "delivered") {
				wc.Delivered++
			} else {
				wc.InProgress++
			}
		case "open":
			switch {
			case hasLabel(issue.Labels, "delivered"):
				// Delivered label is authoritative: an unclaimed delivery
				// (open + delivered) awaits PM review, it is not ready work.
				wc.Delivered++
			case hasLabel(issue.Labels, "rejected"):
				wc.Rejected++
			case blockedSet[issue.ID]:
				wc.Blocked++
			default:
				wc.Ready++
			}
		case "blocked":
			wc.Blocked++
		case "closed":
			// done issues are not counted
		default:
			// deferred and any custom statuses
			wc.Other++
		}
	}

	return wc, nil
}

func countOtherIssues(readyIssues, ipIssues, blockedIssues, allIssues []ndIssue) int {
	known := make(map[string]bool, len(readyIssues)+len(ipIssues)+len(blockedIssues))
	for _, issue := range readyIssues {
		known[issue.ID] = true
	}
	for _, issue := range ipIssues {
		known[issue.ID] = true
	}
	for _, issue := range blockedIssues {
		known[issue.ID] = true
	}

	other := 0
	for _, issue := range allIssues {
		if issue.ID == "" || known[issue.ID] {
			continue
		}
		other++
	}
	return other
}

// ValidateEpic checks that an epic ID exists and is a valid epic.
func ValidateEpic(projectRoot, epicID string) error {
	issues, err := runND(projectRoot, "show", epicID, "--json")
	if err != nil {
		return fmt.Errorf("epic %s not found: %w", epicID, err)
	}
	if len(issues) == 0 {
		return fmt.Errorf("epic %s not found", epicID)
	}
	issue := issues[0]
	if !strings.EqualFold(issue.Type, "epic") {
		return fmt.Errorf("%s is not an epic (type: %s)", epicID, issue.Type)
	}
	return nil
}

// runND executes an nd command and parses JSON output.
// Returns empty slice (not error) when nd outputs nothing.
func runND(projectRoot string, args ...string) ([]ndIssue, error) {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve nd vault: %w", err)
	}

	ndArgs := append([]string{"--vault", vaultDir}, args...)
	cmd := execCommand("nd", ndArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nd %s: %w", strings.Join(ndArgs, " "), err)
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil, nil
	}

	// nd may return a single object or an array
	var issues []ndIssue
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &issues); err != nil {
			return nil, fmt.Errorf("parse nd output: %w", err)
		}
	} else {
		var single ndIssue
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("parse nd output: %w", err)
		}
		issues = []ndIssue{single}
	}

	return issues, nil
}

// QueryInProgress returns all in-progress issues from nd.
func QueryInProgress(projectRoot string) ([]ndIssue, error) {
	return runND(projectRoot, "list", "--status", "in_progress", "--limit", "0", "--json")
}

// QueryDelivered returns non-closed stories labeled delivered. Status is
// deliberately NOT restricted to in_progress: a developer that labeled the
// story without claiming it (status open) has still delivered.
func QueryDelivered(projectRoot string, filters ...string) ([]ndIssue, error) {
	args := []string{"list", "--status", "!closed", "--label", "delivered", "--sort", "priority", "--limit", "0", "--json"}
	args = append(args, filters...)
	return runND(projectRoot, args...)
}

// QueryRejected returns open stories labeled rejected, excluding delivered
// ones (a delivered+rejected combination is inconsistent; PM review wins).
func QueryRejected(projectRoot string, filters ...string) ([]ndIssue, error) {
	args := []string{"list", "--status", "open", "--label", "rejected", "--sort", "priority", "--limit", "0", "--json"}
	args = append(args, filters...)
	issues, err := runND(projectRoot, args...)
	if err != nil {
		return nil, err
	}
	return filterOutLabel(issues, "delivered"), nil
}

// QueryReady returns ready work, excluding rejected stories that must be reworked first.
func QueryReady(projectRoot string, filters ...string) ([]ndIssue, error) {
	args := []string{"ready", "--sort", "priority", "--json"}
	args = append(args, filters...)
	issues, err := runND(projectRoot, args...)
	if err != nil {
		return nil, err
	}
	return filterOutLabel(filterOutLabel(issues, "rejected"), "delivered"), nil
}

// hasLabel checks if a label exists in a slice (case-insensitive).
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, target) {
			return true
		}
	}
	return false
}

// AutoSelectEpic picks the highest-priority non-closed epic that has
// actionable work (delivered, rejected, or ready children).
// Returns the epic ID and title, or empty strings when no epic qualifies.
func AutoSelectEpic(projectRoot string, exclude ...string) (string, string, error) {
	epics, err := runND(projectRoot, "list", "--type", "epic", "--status", "!closed", "--sort", "priority", "--limit", "0", "--json")
	if err != nil {
		return "", "", fmt.Errorf("list epics: %w", err)
	}

	excludeSet := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		excludeSet[id] = true
	}

	for _, epic := range epics {
		if excludeSet[epic.ID] {
			continue
		}
		queues, err := queryQueues(projectRoot, epic.ID)
		if err != nil {
			continue // skip epics we can't query
		}
		if len(queues.Delivered) > 0 || len(queues.Rejected) > 0 || len(queues.Ready) > 0 {
			return epic.ID, epic.Title, nil
		}
	}

	return "", "", nil
}

// QueryEpicCounts is the exported wrapper for queryEpicCounts.
func QueryEpicCounts(projectRoot, epicID string) (WorkCounts, error) {
	return queryEpicCounts(projectRoot, epicID)
}

func filterOutLabel(issues []ndIssue, label string) []ndIssue {
	if len(issues) == 0 {
		return nil
	}

	filtered := make([]ndIssue, 0, len(issues))
	for _, issue := range issues {
		if hasLabel(issue.Labels, label) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}
