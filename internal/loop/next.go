package loop

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/paivot-ai/pvg/internal/settings"
)

type queueSnapshot struct {
	Delivered []ndIssue
	Rejected  []ndIssue
	Ready     []ndIssue
}

// NextAction describes the next deterministic orchestration step for a host platform.
type NextAction struct {
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	StoryID  string `json:"story_id"`
	Story    string `json:"story,omitempty"`
	Queue    string `json:"queue"`
	Scope    string `json:"scope"`
	HardTDD  bool   `json:"hard_tdd"`
	Phase    string `json:"phase,omitempty"`
	Priority string `json:"priority,omitempty"`
	// Model is the per-role spawn-time model override resolved from
	// `pvg settings model.<role>`. Empty (omitted) means "no override" --
	// the agent's frontmatter default wins. See applyModelOverrides.
	Model string `json:"model,omitempty"`
}

// NextResult is the host-agnostic orchestration decision derived from nd state.
type NextResult struct {
	Mode          string       `json:"mode"`
	TargetEpic    string       `json:"target_epic,omitempty"`
	ActiveLoop    bool         `json:"active_loop"`
	ScopeSource   string       `json:"scope_source,omitempty"`
	Decision      string       `json:"decision"`
	Reason        string       `json:"reason"`
	Counts        WorkCounts   `json:"counts"`
	Next          *NextAction  `json:"next,omitempty"`
	Actions       []NextAction `json:"actions,omitempty"`   // full wave when --n > 1; Next stays the first action
	NextEpic      string       `json:"next_epic,omitempty"` // populated on "epic_complete" when a next epic exists
	NextEpicTitle string       `json:"next_epic_title,omitempty"`
}

// MaxWaveSize caps the number of actions a single `pvg loop next --n N` call
// may select. Matches the light-stack total concurrency limit.
const MaxWaveSize = 6

// DecisionNoActiveLoop is returned by `pvg loop next` when there is no active
// loop state and no explicit --all/--epic scope was given. Rather than silently
// running the global cross-epic queue -- which breaks epic containment after a
// session or compaction restart drops the loop state -- the loop refuses and
// asks the dispatcher to re-establish scope explicitly. Silent loss of
// containment is the dangerous failure mode; this makes it loud.
const DecisionNoActiveLoop = "no_active_loop"

// NoActiveLoopResult builds the refusal returned when no loop scope can be
// resolved and none was passed explicitly. Global counts are populated
// best-effort for situational awareness; no action is selected and the caller
// is told how to re-establish scope.
func NoActiveLoopResult(projectRoot string) NextResult {
	result := NextResult{
		Mode:        "none",
		ActiveLoop:  false,
		ScopeSource: "none",
		Decision:    DecisionNoActiveLoop,
		Reason: "No active loop and no explicit scope. Re-establish scope with " +
			"`pvg loop setup --epic <EPIC_ID> --max <N>` and verify active_loop=true, " +
			"or pass --all to run the global queue intentionally. Refusing to dispatch " +
			"unscoped so epic containment cannot break silently.",
	}
	if counts, err := QueryWorkCounts(projectRoot, "all", ""); err == nil {
		result.Counts = counts
	}
	return result
}

// EvaluateNext selects the next orchestration step without mutating state.
// n is the maximum wave size: up to n distinct-story actions are selected
// (values < 1 are treated as 1; values above MaxWaveSize are capped).
//
// In epic mode (the default), the loop is CONTAINED to the target epic:
//   - If the epic has actionable work: return it
//   - If the epic has only in-progress work: wait (don't leave the epic)
//   - If all stories are closed: epic_complete (run the completion gate)
//   - If all remaining stories are blocked: epic_blocked (escalate)
//   - After epic completion: rotate to the next highest-priority epic
//
// In "all" mode (legacy escape hatch), behavior is unchanged: global priority queue.
func EvaluateNext(projectRoot, mode, targetEpic string, n int) (NextResult, error) {
	result := NextResult{
		Mode:       mode,
		TargetEpic: targetEpic,
	}

	if result.Mode == "" {
		result.Mode = "all"
	}
	n = clampWaveSize(n)

	// Global counts are always needed for reporting, regardless of mode.
	counts, err := QueryWorkCounts(projectRoot, result.Mode, result.TargetEpic)
	if err != nil {
		return result, err
	}
	result.Counts = counts

	if result.Mode == "epic" && result.TargetEpic != "" {
		return evaluateEpicMode(projectRoot, result, n)
	}

	return evaluateAllMode(projectRoot, result, n)
}

// roleSettingKey maps an action's agent role to its model.<role> setting key.
// Roles without a configurable override return "".
func roleSettingKey(role string) string {
	switch role {
	case "developer":
		return "model.developer"
	case "pm_acceptor":
		return "model.pm"
	default:
		return ""
	}
}

// applyModelOverrides loads the project's settings once and stamps each
// action's Model field with the per-role override (if any). An empty override
// leaves Model empty, so JSON output is byte-for-byte unchanged when no
// model.<role> setting is present.
func applyModelOverrides(projectRoot string, actions []NextAction) {
	if len(actions) == 0 {
		return
	}
	s := settings.LoadFile(filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml"))
	cache := make(map[string]string)
	for i := range actions {
		key := roleSettingKey(actions[i].Role)
		if key == "" {
			continue
		}
		model, ok := cache[key]
		if !ok {
			model = s[key]
			cache[key] = model
		}
		actions[i].Model = model
	}
}

func clampWaveSize(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxWaveSize {
		return MaxWaveSize
	}
	return n
}

// evaluateEpicMode enforces epic containment: never fall through to global.
func evaluateEpicMode(projectRoot string, result NextResult, n int) (NextResult, error) {
	// Query work within the target epic only.
	epicQueues, err := queryQueues(projectRoot, result.TargetEpic)
	if err != nil {
		return result, err
	}

	// Update counts to reflect epic-scoped reality for the decision.
	epicCounts, err := QueryEpicCounts(projectRoot, result.TargetEpic)
	if err != nil {
		return result, err
	}

	// If the epic has actionable work, do it.
	if actions := chooseNextActions(epicQueues, "epic", n); len(actions) > 0 {
		applyModelOverrides(projectRoot, actions)
		result.Decision = "act"
		result.Reason = fmt.Sprintf("Epic %s has actionable work", result.TargetEpic)
		result.Next = &actions[0]
		result.Actions = actions
		return result, nil
	}

	// No actionable work in this epic. Why?
	epicTotal := epicCounts.Ready + epicCounts.Rejected + epicCounts.Delivered +
		epicCounts.InProgress + epicCounts.Blocked + epicCounts.Other

	// All stories closed: epic is complete. Run the completion gate.
	if epicTotal == 0 {
		// Check if there's a next epic to rotate to.
		nextID, nextTitle, err := AutoSelectEpic(projectRoot, result.TargetEpic)
		if err != nil {
			return result, err
		}
		if nextID != "" {
			result.Decision = "epic_complete"
			result.Reason = fmt.Sprintf("All stories in epic %s are closed -- run completion gate, then rotate to %s", result.TargetEpic, nextID)
			result.NextEpic = nextID
			result.NextEpicTitle = nextTitle
			return result, nil
		}
		// No next epic: this was the last one.
		result.Decision = "epic_complete"
		result.Reason = fmt.Sprintf("All stories in epic %s are closed -- run completion gate (last epic)", result.TargetEpic)
		return result, nil
	}

	// Stories still in-progress: agents are working. Wait.
	if epicCounts.InProgress > 0 || epicCounts.Delivered > 0 {
		result.Decision = "wait"
		if epicCounts.Delivered > 0 {
			result.Reason = fmt.Sprintf("Epic %s: %d delivered stories await PM review", result.TargetEpic, epicCounts.Delivered)
		} else {
			result.Reason = fmt.Sprintf("Epic %s: %d stories in progress -- waiting", result.TargetEpic, epicCounts.InProgress)
		}
		return result, nil
	}

	// Only blocked stories remain: no forward progress possible.
	if epicCounts.Blocked > 0 {
		result.Decision = "epic_blocked"
		result.Reason = fmt.Sprintf("Epic %s: %d stories blocked with no actionable work -- escalate to user", result.TargetEpic, epicCounts.Blocked)
		return result, nil
	}

	// Other non-dispatcher states.
	if epicCounts.Other > 0 {
		result.Decision = "wait"
		result.Reason = fmt.Sprintf("Epic %s: %d stories in non-dispatcher workflow states", result.TargetEpic, epicCounts.Other)
		return result, nil
	}

	// Fallback: shouldn't reach here, but be safe.
	result.Decision = "wait"
	result.Reason = fmt.Sprintf("Epic %s: no actionable work selected", result.TargetEpic)
	return result, nil
}

// evaluateAllMode is the legacy global priority queue (--all escape hatch).
func evaluateAllMode(projectRoot string, result NextResult, n int) (NextResult, error) {
	globalQueues, err := queryQueues(projectRoot, "")
	if err != nil {
		return result, err
	}
	// Refresh counts from the actual queues.
	result.Counts.Delivered = len(globalQueues.Delivered)
	result.Counts.Rejected = len(globalQueues.Rejected)
	result.Counts.Ready = len(globalQueues.Ready)

	if actions := chooseNextActions(globalQueues, "backlog", n); len(actions) > 0 {
		applyModelOverrides(projectRoot, actions)
		result.Decision = "act"
		result.Reason = reasonForAction(&actions[0])
		result.Next = &actions[0]
		result.Actions = actions
		return result, nil
	}

	total := result.Counts.Ready + result.Counts.Rejected + result.Counts.Delivered +
		result.Counts.InProgress + result.Counts.Blocked + result.Counts.Other

	switch {
	case total == 0:
		result.Decision = "complete"
		result.Reason = "All work complete"
	case result.Counts.InProgress > 0:
		result.Decision = "wait"
		result.Reason = "Only in-progress work remains"
	case result.Counts.Blocked > 0 && result.Counts.Other == 0:
		result.Decision = "blocked"
		result.Reason = "All remaining work is blocked"
	case result.Counts.Other > 0:
		result.Decision = "other"
		result.Reason = "Only non-dispatcher workflow states remain"
	default:
		result.Decision = "wait"
		result.Reason = "No actionable work selected"
	}

	return result, nil
}

func queryQueues(projectRoot, parent string) (queueSnapshot, error) {
	var (
		snapshot queueSnapshot
		filters  []string
	)

	if parent != "" {
		filters = append(filters, "--parent", parent)
	}

	delivered, err := QueryDelivered(projectRoot, filters...)
	if err != nil {
		return snapshot, fmt.Errorf("query delivered queue: %w", err)
	}
	rejected, err := QueryRejected(projectRoot, filters...)
	if err != nil {
		return snapshot, fmt.Errorf("query rejected queue: %w", err)
	}
	ready, err := QueryReady(projectRoot, filters...)
	if err != nil {
		return snapshot, fmt.Errorf("query ready queue: %w", err)
	}

	snapshot.Delivered = delivered
	snapshot.Rejected = rejected
	snapshot.Ready = ready
	return snapshot, nil
}

// chooseNextActions selects a wave of up to n actions with distinct story IDs.
// Priority order: at most one pm_review (delivered queue), then developer
// rework (rejected queue), then new developer work (ready queue). Pure
// function -- no mutation of the queues.
func chooseNextActions(queues queueSnapshot, scope string, n int) []NextAction {
	n = clampWaveSize(n)

	var actions []NextAction
	seen := make(map[string]bool)

	// Max 1 PM review per wave: PM review unblocks the pipeline but PM
	// concurrency is capped at one in heavy stacks.
	if len(queues.Delivered) > 0 {
		issue := queues.Delivered[0]
		actions = append(actions, pmReviewAction(issue, scope))
		seen[issue.ID] = true
	}

	for _, issue := range queues.Rejected {
		if len(actions) >= n {
			return actions
		}
		if seen[issue.ID] {
			continue
		}
		actions = append(actions, developerReworkAction(issue, scope))
		seen[issue.ID] = true
	}

	for _, issue := range queues.Ready {
		if len(actions) >= n {
			return actions
		}
		if seen[issue.ID] {
			continue
		}
		actions = append(actions, developerNewAction(issue, scope))
		seen[issue.ID] = true
	}

	return actions
}

// hardTDDPhase reports which hard-TDD phase a story is in. The red-approved
// label is the phase boundary: absent means the story is in (or awaiting
// review of) the RED test-writing phase; present means RED was approved by
// the PM and the story is in the GREEN implementation phase. Empty string
// for stories without the hard-tdd label.
func hardTDDPhase(issue ndIssue) string {
	if !hasLabel(issue.Labels, "hard-tdd") {
		return ""
	}
	if hasLabel(issue.Labels, "red-approved") {
		return "green"
	}
	return "red"
}

func actionPhase(issue ndIssue) string {
	if phase := hardTDDPhase(issue); phase != "" {
		return phase
	}
	return "normal"
}

func pmReviewAction(issue ndIssue, scope string) NextAction {
	return NextAction{
		Kind:     "pm_review",
		Role:     "pm_acceptor",
		StoryID:  issue.ID,
		Story:    issue.Title,
		Queue:    "delivered",
		Scope:    scope,
		HardTDD:  hasLabel(issue.Labels, "hard-tdd"),
		Phase:    hardTDDPhase(issue),
		Priority: strconv.Itoa(issue.Priority),
	}
}

func developerReworkAction(issue ndIssue, scope string) NextAction {
	return NextAction{
		Kind:     "developer_rework",
		Role:     "developer",
		StoryID:  issue.ID,
		Story:    issue.Title,
		Queue:    "rejected",
		Scope:    scope,
		HardTDD:  hasLabel(issue.Labels, "hard-tdd"),
		Phase:    actionPhase(issue),
		Priority: strconv.Itoa(issue.Priority),
	}
}

func developerNewAction(issue ndIssue, scope string) NextAction {
	return NextAction{
		Kind:     "developer_new",
		Role:     "developer",
		StoryID:  issue.ID,
		Story:    issue.Title,
		Queue:    "ready",
		Scope:    scope,
		HardTDD:  hasLabel(issue.Labels, "hard-tdd"),
		Phase:    actionPhase(issue),
		Priority: strconv.Itoa(issue.Priority),
	}
}

func reasonForAction(action *NextAction) string {
	switch action.Queue {
	case "delivered":
		return "Delivered work needs PM review before new execution"
	case "rejected":
		return "Rejected work must be repaired before new ready work"
	case "ready":
		return "Ready work is available for developer execution"
	default:
		return "Actionable work remains"
	}
}
