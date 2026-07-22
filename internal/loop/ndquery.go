package loop

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
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

// QueryWorkCounts returns the work counts that drive stop decisions.
//
// In epic mode the counts are scoped to the TARGET epic's recursive subtree
// (--epic, consistent with the dispatch queues in queryQueues). This is what
// lets the epic completion gate fire when the target epic drains regardless
// of other epics' state: with backlog-wide counts, a blocked sibling epic
// kept the total above zero and the drained target epic could be abandoned
// unmerged with its loop state deleted.
func QueryWorkCounts(projectRoot, mode, targetEpic string) (WorkCounts, error) {
	if mode == "epic" && targetEpic != "" {
		return queryEpicSubtreeCounts(projectRoot, targetEpic)
	}
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
	// nd ready also returns unblocked in_progress issues. A claimed story
	// (status in_progress) is already counted in InProgress below; leaving it
	// here would double-count it as Ready.
	readyIssues = filterOutStatus(filterOutLabel(readyIssues, "delivered"), "in_progress")
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

// queryEpicSubtreeCounts mirrors queryAllCounts with every count scoped to
// the target epic's recursive subtree via --epic (the same filter the
// dispatch queues use). Epic-type issues -- the target epic itself plus any
// nested sub-epics, which --epic includes in the subtree -- are containers,
// never dispatchable work, so they are excluded from every bucket: counting
// the still-open target epic would keep the subtree total above zero forever
// and the completion gate could never fire.
//
// `nd blocked` has no --epic flag, so the graph-blocked set is fetched
// globally and intersected with the subtree membership.
func queryEpicSubtreeCounts(projectRoot, epicID string) (WorkCounts, error) {
	var wc WorkCounts
	scope := []string{"--epic", epicID}

	deliveredIssues, err := runND(projectRoot, append([]string{"list", "--status", "!closed", "--label", "delivered", "--limit", "0", "--json"}, scope...)...)
	if err != nil {
		return wc, fmt.Errorf("query delivered work: %w", err)
	}
	deliveredIssues = filterOutType(deliveredIssues, "epic")
	wc.Delivered = len(deliveredIssues)

	readyIssues, err := runND(projectRoot, append([]string{"ready", "--json"}, scope...)...)
	if err != nil {
		return wc, fmt.Errorf("query ready work: %w", err)
	}
	// Same corrections as queryAllCounts (delivered label is authoritative,
	// claimed stories belong to InProgress), plus the epic-container filter.
	readyIssues = filterOutType(filterOutStatus(filterOutLabel(readyIssues, "delivered"), "in_progress"), "epic")
	wc.Ready = len(readyIssues)

	ipIssues, err := runND(projectRoot, append([]string{"list", "--status", "in_progress", "--limit", "0", "--json"}, scope...)...)
	if err != nil {
		return wc, fmt.Errorf("query in-progress work: %w", err)
	}
	ipIssues = filterOutType(ipIssues, "epic")
	for _, issue := range ipIssues {
		if !hasLabel(issue.Labels, "delivered") {
			wc.InProgress++
		}
	}

	rejectedIssues, err := runND(projectRoot, append([]string{"list", "--status", "open", "--label", "rejected", "--limit", "0", "--json"}, scope...)...)
	if err != nil {
		return wc, fmt.Errorf("query rejected work: %w", err)
	}
	wc.Rejected = len(filterOutType(filterOutLabel(rejectedIssues, "delivered"), "epic"))

	allIssues, err := runND(projectRoot, append([]string{"list", "--status", "!closed", "--limit", "0", "--json"}, scope...)...)
	if err != nil {
		return wc, fmt.Errorf("query non-closed work: %w", err)
	}
	allIssues = filterOutType(allIssues, "epic")
	memberSet := make(map[string]bool, len(allIssues))
	for _, issue := range allIssues {
		memberSet[issue.ID] = true
	}

	blockedGlobal, err := runND(projectRoot, "blocked", "--json")
	if err != nil {
		return wc, fmt.Errorf("query blocked work: %w", err)
	}
	var blockedIssues []ndIssue
	for _, issue := range blockedGlobal {
		if memberSet[issue.ID] {
			blockedIssues = append(blockedIssues, issue)
		}
	}
	wc.Blocked = len(blockedIssues)

	wc.Other = countOtherIssues(append(append([]ndIssue{}, readyIssues...), deliveredIssues...), ipIssues, blockedIssues, allIssues)

	return wc, nil
}

// queryEpicCounts uses nd children to count work within a specific epic.
//
// DIRECT-CHILDREN ASSUMPTION: `nd children` returns direct children only,
// deliberately NOT the recursive subtree the dispatch queues use (--epic).
// A nested sub-epic is counted here as a single child issue whose own status
// stands in for its subtree: the parent epic cannot reach epic_complete
// (epicTotal == 0) until the sub-epic itself is closed by its completion
// gate. Making this recursive would change rotation semantics (when
// epic_complete fires) in ways the current tests do not cover, so the
// counting stays direct on purpose.
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

// QueryInProgressIDs returns the sorted ids of in_progress issues, scoped to
// the target epic's subtree in epic mode. Best-effort: returns nil on error
// (callers use it for work-state signatures, where a missing set degrades to
// counts-only fingerprinting).
func QueryInProgressIDs(projectRoot, mode, targetEpic string) []string {
	args := []string{"list", "--status", "in_progress", "--limit", "0", "--json"}
	if mode == "epic" && targetEpic != "" {
		args = append(args, "--epic", targetEpic)
	}
	issues, err := runND(projectRoot, args...)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.ID != "" {
			ids = append(ids, issue.ID)
		}
	}
	sort.Strings(ids)
	return ids
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

// QueryReady returns ready work, excluding rejected stories that must be
// reworked first and in_progress stories that are already claimed.
//
// The in_progress filter exists because nd ready returns issues that are
// "open OR in_progress with no open blockers": a story the dispatcher just
// claimed (`pvg story claim` sets status in_progress) would otherwise stay in
// the dispatch queue and the next `pvg loop next` would spawn a SECOND
// developer onto the same story branch. Claiming is what closes that
// double-dispatch window, so a claimed story must leave the ready queue.
// Hard-TDD GREEN dispatch is unaffected: `pvg story approve-red` returns the
// story to status open, so it re-enters the queue for the GREEN phase.
//
// Epic-type issues are filtered out: with `--epic` scoping the epic itself
// (and any nested sub-epics) are inside the recursive subtree and nd ready
// does not exclude them, but an epic is a container, never dispatchable
// developer work.
func QueryReady(projectRoot string, filters ...string) ([]ndIssue, error) {
	args := []string{"ready", "--sort", "priority", "--json"}
	args = append(args, filters...)
	issues, err := runND(projectRoot, args...)
	if err != nil {
		return nil, err
	}
	issues = filterOutLabel(filterOutLabel(issues, "rejected"), "delivered")
	issues = filterOutType(issues, "epic")
	return filterOutStatus(issues, "in_progress"), nil
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

// filterOutType drops issues whose type matches (case-insensitive).
func filterOutType(issues []ndIssue, issueType string) []ndIssue {
	if len(issues) == 0 {
		return nil
	}

	filtered := make([]ndIssue, 0, len(issues))
	for _, issue := range issues {
		if strings.EqualFold(issue.Type, issueType) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// filterOutStatus drops issues whose status matches (case-insensitive).
func filterOutStatus(issues []ndIssue, status string) []ndIssue {
	if len(issues) == 0 {
		return nil
	}

	filtered := make([]ndIssue, 0, len(issues))
	for _, issue := range issues {
		if strings.EqualFold(issue.Status, status) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}
