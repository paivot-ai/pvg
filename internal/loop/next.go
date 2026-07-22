package loop

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

// StalledStory identifies one in_progress story whose developer is presumed
// dead with its worktree still on disk (see DetectStall).
type StalledStory struct {
	StoryID      string `json:"story_id"`
	WorktreePath string `json:"worktree_path"`
}

// NextResult is the host-agnostic orchestration decision derived from nd state.
type NextResult struct {
	Mode           string         `json:"mode"`
	TargetEpic     string         `json:"target_epic,omitempty"`
	ActiveLoop     bool           `json:"active_loop"`
	ScopeSource    string         `json:"scope_source,omitempty"`
	Decision       string         `json:"decision"`
	Reason         string         `json:"reason"`
	Counts         WorkCounts     `json:"counts"`
	Next           *NextAction    `json:"next,omitempty"`
	Actions        []NextAction   `json:"actions,omitempty"`   // full wave when --n > 1; Next stays the first action
	NextEpic       string         `json:"next_epic,omitempty"` // populated on "epic_complete" when a next epic exists
	NextEpicTitle  string         `json:"next_epic_title,omitempty"`
	Stalled        []StalledStory `json:"stalled,omitempty"`         // populated on "stalled"
	EscalatedStory string         `json:"escalated_story,omitempty"` // populated on "escalate"
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

// DecisionStalled is returned by `pvg loop next` when the identical non-empty
// in_progress story set has been observed on stalledWaitThreshold consecutive
// wait evaluations. ReconcileOrphans only heals stories whose worktree is
// GONE; a dead developer that left its worktree behind parks the story
// in_progress forever, and epic mode would wait on it indefinitely.
const DecisionStalled = "stalled"

// stalledWaitThreshold is the number of consecutive wait evaluations that
// must observe the identical non-empty in_progress story set before the loop
// reports the claims as stalled.
const stalledWaitThreshold = 3

// DecisionEscalate is returned when a rejected story has reached RejectionCap
// PM rejections: the loop stops dispatching automated rework and surfaces the
// story to the user instead. The PM's verdict is never overridden.
const DecisionEscalate = "escalate"

// RejectionCap is the number of PM rejections after which the loop escalates
// a story to the user instead of dispatching another rework cycle. `pvg story
// reject` maintains the counter labels (rejected-x2, rejected-x3) this reads.
const RejectionCap = 3

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

	// Rejection cap: a story the PM rejected RejectionCap times must not
	// ping-pong through yet another automated rework cycle.
	if escalated := applyRejectionCap(&result, epicQueues.Rejected); escalated {
		return result, nil
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

	// Rejection cap: same escalation rule as epic mode.
	if escalated := applyRejectionCap(&result, globalQueues.Rejected); escalated {
		return result, nil
	}

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
		// --epic scopes to the epic's whole subtree (recursive), unlike
		// --parent which only matches direct children. Nested epics would
		// otherwise hide their stories from the dispatch queues. Both
		// nd ready and nd list support --epic.
		filters = append(filters, "--epic", parent)
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

// applyRejectionCap scans the rejected queue for a story whose rejection
// counter has reached RejectionCap and, if found, converts the result into an
// "escalate" decision instead of letting a developer_rework action be
// selected for it. Returns true when the result was escalated.
func applyRejectionCap(result *NextResult, rejected []ndIssue) bool {
	for _, issue := range rejected {
		if rejectionCountFromLabels(issue.Labels) < RejectionCap {
			continue
		}
		result.Decision = DecisionEscalate
		result.EscalatedStory = issue.ID
		result.Reason = fmt.Sprintf("story %s rejected %d times; surface to the user; never override the PM", issue.ID, RejectionCap)
		return true
	}
	return false
}

// rejectionCountFromLabels reads the deterministic rejection counter that
// `pvg story reject` maintains via labels: plain "rejected" counts as 1, a
// "rejected-xN" counter label counts as N (the highest wins).
func rejectionCountFromLabels(labels []string) int {
	count := 0
	for _, label := range labels {
		lower := strings.ToLower(label)
		if lower == "rejected" && count < 1 {
			count = 1
			continue
		}
		if rest, ok := strings.CutPrefix(lower, "rejected-x"); ok {
			if n, err := strconv.Atoi(rest); err == nil && n > count {
				count = n
			}
		}
	}
	return count
}

// DetectStall updates the loop state's wait-set tracking after a `pvg loop
// next` evaluation and converts a wait decision into DecisionStalled when the
// identical non-empty in_progress story set has been observed on
// stalledWaitThreshold consecutive wait evaluations. ReconcileOrphans (which
// runs before every evaluation) already healed stories whose worktree is
// gone, so a story still stuck in_progress at this point has its worktree on
// disk with a presumed-dead developer -- epic mode would otherwise wait on it
// forever. No-op when there is no persistent loop state to track across
// evaluations. Any non-wait decision resets the streak.
func DetectStall(projectRoot string, result *NextResult) {
	state, root, err := ReadStateRoot(projectRoot)
	if err != nil || !state.Active {
		return
	}

	clear := func() {
		if state.WaitStorySet != "" || state.WaitStoryStreak != 0 {
			state.WaitStorySet = ""
			state.WaitStoryStreak = 0
			_ = WriteState(root, state)
		}
	}

	if result.Decision != "wait" {
		clear()
		return
	}

	ids := stalledCandidateIDs(projectRoot, result.Mode, result.TargetEpic)
	if len(ids) == 0 {
		clear()
		return
	}

	set := strings.Join(ids, ",")
	if set == state.WaitStorySet {
		state.WaitStoryStreak++
	} else {
		state.WaitStorySet = set
		state.WaitStoryStreak = 1
	}
	_ = WriteState(root, state)

	if state.WaitStoryStreak < stalledWaitThreshold {
		return
	}

	base := ResolveWorktreeBase(root)
	result.Decision = DecisionStalled
	result.Stalled = result.Stalled[:0]
	for _, id := range ids {
		result.Stalled = append(result.Stalled, StalledStory{
			StoryID:      id,
			WorktreePath: filepath.Join(base, "dev-"+id),
		})
	}
	result.Reason = fmt.Sprintf(
		"%d consecutive wait evaluations observed the identical in_progress story set (%s); the owning developer(s) are presumed dead with their worktrees still on disk. Run `pvg loop recover`, then respawn a developer for each story or release it with `pvg story release <STORY_ID>`.",
		state.WaitStoryStreak, set)
}

// stalledCandidateIDs returns the sorted in_progress story ids that a stalled
// wait could be parked on, scoped to the target epic in epic mode. Stories
// labeled delivered are excluded: they await PM review and need no live
// developer (matching the orphan-healing rule in ReconcileOrphans).
func stalledCandidateIDs(projectRoot, mode, targetEpic string) []string {
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
		if issue.ID == "" || hasLabel(issue.Labels, "delivered") {
			continue
		}
		ids = append(ids, issue.ID)
	}
	sort.Strings(ids)
	return ids
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
