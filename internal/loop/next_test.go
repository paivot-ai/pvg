package loop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// No-active-loop refusal: never silently widen to the global queue
// ---------------------------------------------------------------------------

func TestNoActiveLoopResult_Refuses(t *testing.T) {
	// QueryWorkCounts errors on an empty temp dir; counts stay zero
	// best-effort and the refusal decision is still returned.
	result := NoActiveLoopResult(t.TempDir())

	if result.Decision != DecisionNoActiveLoop {
		t.Fatalf("expected decision %q, got %q", DecisionNoActiveLoop, result.Decision)
	}
	if result.ActiveLoop {
		t.Error("expected ActiveLoop=false for a refusal")
	}
	if result.ScopeSource != "none" {
		t.Errorf("expected ScopeSource=none, got %q", result.ScopeSource)
	}
	if result.Mode != "none" {
		t.Errorf("expected Mode=none, got %q", result.Mode)
	}
	if result.Next != nil || len(result.Actions) != 0 {
		t.Errorf("refusal must not select an action: Next=%v Actions=%v", result.Next, result.Actions)
	}
	if result.Reason == "" {
		t.Error("refusal must explain how to re-establish scope")
	}
}

// ---------------------------------------------------------------------------
// Epic mode: containment tests
// ---------------------------------------------------------------------------

func TestEvaluateNext_EpicMode_ActsOnDeliveredInEpic(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		// Epic has a delivered story
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[{"ID":"PROJ-s1","Title":"Epic delivery","Status":"in_progress","Parent":"PROJ-epic","Labels":["delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		// Epic children: one delivered
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"in_progress","Labels":["delivered"]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "act" {
		t.Fatalf("expected act, got %s: %s", result.Decision, result.Reason)
	}
	if result.Next == nil || result.Next.StoryID != "PROJ-s1" {
		t.Fatalf("expected PROJ-s1, got %#v", result.Next)
	}
	if result.Next.Scope != "epic" {
		t.Fatalf("expected epic scope, got %s", result.Next.Scope)
	}
}

func TestEvaluateNext_EpicMode_DoesNotFallThroughToGlobal(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		// Epic has NO actionable work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		// Epic children: one in-progress (agents working)
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"in_progress","Labels":[]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	// Must NOT pick from global backlog -- must wait within the epic.
	if result.Decision != "wait" {
		t.Fatalf("expected wait (containment), got %s: %s", result.Decision, result.Reason)
	}
	if result.Next != nil {
		t.Fatalf("expected no action (containment), got %#v", result.Next)
	}
}

func TestEvaluateNext_EpicMode_EpicCompleteWhenAllClosed(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		// Epic has no actionable work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		// Epic children: all closed (empty result)
		"children PROJ-epic --json": `[]`,
		// AutoSelectEpic: another epic exists
		"list --type epic --status !closed --sort priority --limit 0 --json":                        `[{"ID":"PROJ-epic","Type":"epic"},{"ID":"PROJ-e2","Title":"Next Epic","Type":"epic"}]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e2": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e2":     `[]`,
		"ready --sort priority --json --parent PROJ-e2":                                             `[{"ID":"PROJ-s2","Title":"Story Two","Status":"ready"}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "epic_complete" {
		t.Fatalf("expected epic_complete, got %s: %s", result.Decision, result.Reason)
	}
	if result.NextEpic != "PROJ-e2" {
		t.Fatalf("expected rotation to PROJ-e2, got %s", result.NextEpic)
	}
}

func TestEvaluateNext_EpicMode_EpicCompleteLastEpic(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		"children PROJ-epic --json": `[]`,
		// No other epics
		"list --type epic --status !closed --sort priority --limit 0 --json": `[{"ID":"PROJ-epic","Type":"epic"}]`,
		// PROJ-epic is excluded by AutoSelectEpic, so no match
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "epic_complete" {
		t.Fatalf("expected epic_complete, got %s: %s", result.Decision, result.Reason)
	}
	if result.NextEpic != "" {
		t.Fatalf("expected empty NextEpic (last epic), got %s", result.NextEpic)
	}
}

func TestEvaluateNext_EpicMode_EpicBlockedWhenOnlyBlocked(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"blocked","Labels":[]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "epic_blocked" {
		t.Fatalf("expected epic_blocked, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateNext_EpicMode_EpicBlockedWhenChildrenOpenButGraphBlocked(t *testing.T) {
	// nd has no "ready" status: children that are open but dependency-blocked
	// keep status "open". They must classify as Blocked (via nd blocked),
	// not Other, so the loop escalates with epic_blocked instead of waiting
	// forever.
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		// Children are open but graph-blocked.
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"open","Labels":[]},{"ID":"PROJ-s2","Status":"open","Labels":[]}]`,
		"blocked --json":            `[{"ID":"PROJ-s1","Status":"open","Labels":[]},{"ID":"PROJ-s2","Status":"open","Labels":[]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "epic_blocked" {
		t.Fatalf("expected epic_blocked, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateNext_EpicMode_WaitsOnDeliveredInEpicCounts(t *testing.T) {
	// Edge case: queryQueues finds no delivered (because nd query timing),
	// but epicCounts shows delivered. Should wait, not fall through.
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		// But children shows delivered (race-safe: epicCounts catches it)
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"in_progress","Labels":["delivered"]},{"ID":"PROJ-s2","Status":"closed","Labels":[]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "wait" {
		t.Fatalf("expected wait (delivered in epic counts), got %s: %s", result.Decision, result.Reason)
	}
}

// ---------------------------------------------------------------------------
// All mode: legacy behavior (unchanged)
// ---------------------------------------------------------------------------

func TestEvaluateNext_AllMode_PrefersRejectedBeforeReady(t *testing.T) {
	withStubbedND(t, map[string]string{
		"ready --json": `[{"ID":"PROJ-rework","Status":"ready","Labels":["rejected"]},{"ID":"PROJ-ready","Status":"ready","Labels":[]}]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[]`,
		"list --status open --label rejected --limit 0 --json":     `[{"ID":"PROJ-rework","Status":"open","Labels":["rejected"]}]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[{"ID":"PROJ-rework","Status":"open","Labels":["rejected"]},{"ID":"PROJ-ready","Status":"ready","Labels":[]}]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json":     `[{"ID":"PROJ-rework","Title":"Fix me","Status":"open","Labels":["rejected"]}]`,
		"ready --sort priority --json":                                             `[{"ID":"PROJ-rework","Title":"Fix me","Status":"ready","Labels":["rejected"]},{"ID":"PROJ-ready","Title":"New work","Status":"ready","Labels":[]}]`,
	})

	result, err := EvaluateNext(t.TempDir(), "all", "", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil || result.Next.Queue != "rejected" {
		t.Fatalf("expected rejected queue to win, got %#v", result.Next)
	}
	if result.Counts.Ready != 1 {
		t.Fatalf("expected ready count to exclude rejected stories, got %+v", result.Counts)
	}
}

func TestEvaluateNext_AllMode_HardTDDReadyStartsInRedPhase(t *testing.T) {
	withStubbedND(t, map[string]string{
		"ready --json": `[{"ID":"PROJ-hard","Status":"ready","Labels":["hard-tdd"]}]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[]`,
		"list --status open --label rejected --limit 0 --json":     `[]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[{"ID":"PROJ-hard","Status":"ready","Labels":["hard-tdd"]}]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json":     `[]`,
		"ready --sort priority --json":                                             `[{"ID":"PROJ-hard","Title":"Hard story","Status":"ready","Labels":["hard-tdd"]}]`,
	})

	result, err := EvaluateNext(t.TempDir(), "all", "", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil || result.Next.Phase != "red" || !result.Next.HardTDD {
		t.Fatalf("expected hard-tdd red phase, got %#v", result.Next)
	}
}

func TestEvaluateNext_AllMode_WaitsWhenOnlyInProgressRemains(t *testing.T) {
	withStubbedND(t, map[string]string{
		"ready --json": `[]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[{"ID":"PROJ-run","Status":"in_progress","Labels":[]}]`,
		"list --status open --label rejected --limit 0 --json":     `[]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[{"ID":"PROJ-run","Status":"in_progress","Labels":[]}]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json":     `[]`,
		"ready --sort priority --json":                                             `[]`,
	})

	result, err := EvaluateNext(t.TempDir(), "all", "", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "wait" {
		t.Fatalf("expected wait, got %+v", result)
	}
}

func TestEvaluateNext_AllMode_CompleteWhenEmpty(t *testing.T) {
	withStubbedND(t, map[string]string{
		"ready --json": `[]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[]`,
		"list --status open --label rejected --limit 0 --json":     `[]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json":     `[]`,
		"ready --sort priority --json":                                             `[]`,
	})

	result, err := EvaluateNext(t.TempDir(), "all", "", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "complete" {
		t.Fatalf("expected complete, got %s: %s", result.Decision, result.Reason)
	}
}

// ---------------------------------------------------------------------------
// Wave selection (--n)
// ---------------------------------------------------------------------------

func TestChooseNextActions_DistinctStoriesUpToN(t *testing.T) {
	queues := queueSnapshot{
		Ready: []ndIssue{
			{ID: "PROJ-s1", Title: "One", Status: "open"},
			{ID: "PROJ-s2", Title: "Two", Status: "open"},
			{ID: "PROJ-s3", Title: "Three", Status: "open"},
		},
	}

	actions := chooseNextActions(queues, "epic", 3)
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(actions))
	}
	seen := map[string]bool{}
	for _, a := range actions {
		if seen[a.StoryID] {
			t.Fatalf("duplicate story ID in wave: %s", a.StoryID)
		}
		seen[a.StoryID] = true
		if a.Kind != "developer_new" {
			t.Fatalf("expected developer_new, got %s", a.Kind)
		}
	}
}

func TestChooseNextActions_RespectsN(t *testing.T) {
	queues := queueSnapshot{
		Ready: []ndIssue{
			{ID: "PROJ-s1"}, {ID: "PROJ-s2"}, {ID: "PROJ-s3"}, {ID: "PROJ-s4"},
		},
	}

	actions := chooseNextActions(queues, "backlog", 2)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
}

func TestChooseNextActions_MaxOnePMReviewPerWave(t *testing.T) {
	queues := queueSnapshot{
		Delivered: []ndIssue{
			{ID: "PROJ-d1", Labels: []string{"delivered"}},
			{ID: "PROJ-d2", Labels: []string{"delivered"}},
		},
		Ready: []ndIssue{
			{ID: "PROJ-s1"}, {ID: "PROJ-s2"},
		},
	}

	actions := chooseNextActions(queues, "epic", 3)
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(actions))
	}
	pmCount := 0
	for _, a := range actions {
		if a.Kind == "pm_review" {
			pmCount++
		}
	}
	if pmCount != 1 {
		t.Fatalf("expected exactly 1 pm_review per wave, got %d", pmCount)
	}
	if actions[0].Kind != "pm_review" || actions[0].StoryID != "PROJ-d1" {
		t.Fatalf("expected pm_review first, got %#v", actions[0])
	}
}

func TestChooseNextActions_RejectedBeforeReady(t *testing.T) {
	queues := queueSnapshot{
		Rejected: []ndIssue{{ID: "PROJ-r1", Labels: []string{"rejected"}}},
		Ready:    []ndIssue{{ID: "PROJ-s1"}},
	}

	actions := chooseNextActions(queues, "epic", 2)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Kind != "developer_rework" || actions[0].StoryID != "PROJ-r1" {
		t.Fatalf("expected rework first, got %#v", actions[0])
	}
	if actions[1].Kind != "developer_new" || actions[1].StoryID != "PROJ-s1" {
		t.Fatalf("expected new work second, got %#v", actions[1])
	}
}

func TestChooseNextActions_SkipsDuplicateStoryAcrossQueues(t *testing.T) {
	// A story appearing in both rejected and ready (nd race) must not be
	// dispatched twice in one wave.
	queues := queueSnapshot{
		Rejected: []ndIssue{{ID: "PROJ-r1", Labels: []string{"rejected"}}},
		Ready:    []ndIssue{{ID: "PROJ-r1", Labels: []string{"rejected"}}, {ID: "PROJ-s1"}},
	}

	actions := chooseNextActions(queues, "epic", 3)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions (dedup), got %d", len(actions))
	}
}

func TestChooseNextActions_ClampsWaveSize(t *testing.T) {
	var ready []ndIssue
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		ready = append(ready, ndIssue{ID: "PROJ-" + id})
	}
	queues := queueSnapshot{Ready: ready}

	if got := len(chooseNextActions(queues, "epic", 99)); got != MaxWaveSize {
		t.Fatalf("expected wave capped at %d, got %d", MaxWaveSize, got)
	}
	if got := len(chooseNextActions(queues, "epic", 0)); got != 1 {
		t.Fatalf("expected wave of 1 for n=0, got %d", got)
	}
}

func TestEvaluateNext_EpicMode_WavePopulatesActionsAndNext(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[{"ID":"PROJ-d1","Title":"Done","Status":"in_progress","Labels":["delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[{"ID":"PROJ-s1","Title":"One","Status":"open"},{"ID":"PROJ-s2","Title":"Two","Status":"open"}]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-d1","Status":"in_progress","Labels":["delivered"]},{"ID":"PROJ-s1","Status":"open","Labels":[]},{"ID":"PROJ-s2","Status":"open","Labels":[]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 3)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "act" {
		t.Fatalf("expected act, got %s: %s", result.Decision, result.Reason)
	}
	if len(result.Actions) != 3 {
		t.Fatalf("expected wave of 3, got %d: %#v", len(result.Actions), result.Actions)
	}
	if result.Next == nil || result.Next.StoryID != result.Actions[0].StoryID {
		t.Fatalf("expected Next to mirror first wave action, got %#v", result.Next)
	}
	if result.Actions[0].Kind != "pm_review" {
		t.Fatalf("expected pm_review first in wave, got %s", result.Actions[0].Kind)
	}
}

func TestEvaluateNext_AllMode_WaveSelectsDistinctStories(t *testing.T) {
	withStubbedND(t, map[string]string{
		"ready --json": `[{"ID":"PROJ-s1","Status":"open"},{"ID":"PROJ-s2","Status":"open"}]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[]`,
		"list --status open --label rejected --limit 0 --json":     `[]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json":     `[]`,
		"ready --sort priority --json":                                             `[{"ID":"PROJ-s1","Title":"One","Status":"open"},{"ID":"PROJ-s2","Title":"Two","Status":"open"}]`,
	})

	result, err := EvaluateNext(t.TempDir(), "all", "", 2)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "act" {
		t.Fatalf("expected act, got %s: %s", result.Decision, result.Reason)
	}
	if len(result.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(result.Actions))
	}
	if result.Actions[0].StoryID == result.Actions[1].StoryID {
		t.Fatal("expected distinct story IDs in wave")
	}
}

// ---------------------------------------------------------------------------
// Per-role model overrides
// ---------------------------------------------------------------------------

// writeSettingsFile writes a .settings.yaml under projectRoot so that
// applyModelOverrides can resolve model.<role> keys.
func writeSettingsFile(t *testing.T, projectRoot string, lines ...string) {
	t.Helper()
	dir := filepath.Join(projectRoot, ".vault", "knowledge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".settings.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateNext_DeveloperActionGetsModelOverride(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[{"ID":"PROJ-s1","Title":"New work","Status":"open"}]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"open","Labels":[]}]`,
	}))

	projectRoot := t.TempDir()
	writeSettingsFile(t, projectRoot, "model.developer: sonnet")

	result, err := EvaluateNext(projectRoot, "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil || result.Next.Role != "developer" {
		t.Fatalf("expected developer action, got %#v", result.Next)
	}
	if result.Next.Model != "sonnet" {
		t.Fatalf("expected model 'sonnet' on developer action, got %q", result.Next.Model)
	}

	// The override must be present in the JSON encoding too.
	data, err := json.Marshal(result.Next)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"model":"sonnet"`) {
		t.Fatalf("expected \"model\":\"sonnet\" in JSON, got %s", data)
	}
}

func TestEvaluateNext_NoModelSettingOmitsField(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[{"ID":"PROJ-s1","Title":"New work","Status":"open"}]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"open","Labels":[]}]`,
	}))

	// No settings file under this projectRoot -> no override.
	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil {
		t.Fatalf("expected an action, got nil")
	}
	if result.Next.Model != "" {
		t.Fatalf("expected empty model when unset, got %q", result.Next.Model)
	}

	// omitempty must drop the field entirely from JSON.
	data, err := json.Marshal(result.Next)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"model"`) {
		t.Fatalf("expected model field to be omitted, got %s", data)
	}
}

func TestEvaluateNext_PMReviewActionResolvesModelPM(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[{"ID":"PROJ-s1","Title":"Delivered","Status":"in_progress","Labels":["delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"in_progress","Labels":["delivered"]}]`,
	}))

	projectRoot := t.TempDir()
	writeSettingsFile(t, projectRoot, "model.pm: opus", "model.developer: sonnet")

	result, err := EvaluateNext(projectRoot, "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil || result.Next.Role != "pm_acceptor" {
		t.Fatalf("expected pm_acceptor action, got %#v", result.Next)
	}
	if result.Next.Model != "opus" {
		t.Fatalf("expected pm_acceptor to resolve model.pm=opus, got %q", result.Next.Model)
	}
}

func TestEvaluateNext_WaveAppliesModelToEveryAction(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[{"ID":"PROJ-s1","Title":"One","Status":"open"},{"ID":"PROJ-s2","Title":"Two","Status":"open"}]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"open","Labels":[]},{"ID":"PROJ-s2","Status":"open","Labels":[]}]`,
	}))

	projectRoot := t.TempDir()
	writeSettingsFile(t, projectRoot, "model.developer: haiku")

	result, err := EvaluateNext(projectRoot, "epic", "PROJ-epic", 2)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if len(result.Actions) != 2 {
		t.Fatalf("expected wave of 2, got %d", len(result.Actions))
	}
	for i, a := range result.Actions {
		if a.Model != "haiku" {
			t.Fatalf("action %d: expected model 'haiku', got %q", i, a.Model)
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// epicModeStubs merges epic-specific stubs with the global-count stubs
// that EvaluateNext always queries (for the counts in the result).
func epicModeStubs(epicStubs map[string]string) map[string]string {
	base := map[string]string{
		// Global counts (always queried by QueryWorkCounts)
		"ready --json": `[]`,
		"list --status !closed --label delivered --limit 0 --json": `[]`,
		"list --status in_progress --limit 0 --json":               `[]`,
		"list --status open --label rejected --limit 0 --json":     `[]`,
		"blocked --json":                         `[]`,
		"list --status !closed --limit 0 --json": `[]`,
	}
	for k, v := range epicStubs {
		base[k] = v
	}
	return base
}

func withStubbedND(t *testing.T, responses map[string]string) {
	t.Helper()

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatalf("set ND_VAULT_DIR: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("ND_VAULT_DIR")
	})

	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name != "nd" {
			return exec.Command(name, args...)
		}

		keyParts := make([]string, 0, len(args))
		skipNext := false
		for i, arg := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if arg == "--vault" && i+1 < len(args) {
				skipNext = true
				continue
			}
			keyParts = append(keyParts, arg)
		}
		key := strings.Join(keyParts, " ")
		response, ok := responses[key]
		if !ok {
			return exec.Command("python3", "-c", "import sys; print(sys.argv[1], file=sys.stderr); sys.exit(1)", "missing stub for "+key)
		}
		return exec.Command("python3", "-c", "import sys; sys.stdout.write(sys.argv[1])", response)
	}
	t.Cleanup(func() {
		execCommand = oldExec
	})
}

// Regression: a developer that adds the delivered label without claiming the
// story (status open + delivered) must route to PM review, NOT back into the
// ready queue -- that re-dispatched a RED developer forever.
func TestEvaluateNext_EpicMode_OpenPlusDeliveredRoutesToPMReview(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[{"ID":"PROJ-s1","Title":"RED delivery","Status":"open","Priority":0,"Parent":"PROJ-epic","Labels":["hard-tdd","delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		// The unclaimed delivery still shows up in nd ready -- the loop must
		// filter it out of the ready queue.
		"ready --sort priority --json --parent PROJ-epic": `[{"ID":"PROJ-s1","Title":"RED delivery","Status":"open","Priority":0,"Parent":"PROJ-epic","Labels":["hard-tdd","delivered"]}]`,
		"children PROJ-epic --json":                       `[{"ID":"PROJ-s1","Status":"open","Labels":["hard-tdd","delivered"]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 2)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "act" {
		t.Fatalf("expected act, got %s: %s", result.Decision, result.Reason)
	}
	if result.Next == nil || result.Next.Kind != "pm_review" || result.Next.StoryID != "PROJ-s1" {
		t.Fatalf("expected pm_review for PROJ-s1, got %#v", result.Next)
	}
	if result.Next.Phase != "red" {
		t.Fatalf("expected phase red on hard-tdd pm_review, got %q", result.Next.Phase)
	}
	if result.Next.Priority != "0" {
		t.Fatalf("expected story priority 0, got %q", result.Next.Priority)
	}
	// The same story must NOT also appear as a developer_new action.
	for _, a := range result.Actions[1:] {
		if a.StoryID == "PROJ-s1" {
			t.Fatalf("delivered story re-offered as %s", a.Kind)
		}
	}
}

// Regression (double-dispatch hazard): nd ready returns issues that are
// "open OR in_progress with no open blockers", so a story the dispatcher just
// claimed (status in_progress) still appears in nd ready. The loop must
// filter it out and wait -- NOT return act/developer_new for the same story,
// which would spawn a second developer onto the same story branch.
func TestEvaluateNext_EpicMode_ClaimedInProgressStoryIsNotRedispatched(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		// Field report shape: a single hard-tdd story, claimed (in_progress),
		// labels [hard-tdd] only, still present in nd ready output.
		"ready --sort priority --json --parent PROJ-epic": `[{"ID":"PROJ-s1","Title":"Claimed RED work","Status":"in_progress","Priority":0,"Parent":"PROJ-epic","Labels":["hard-tdd"]}]`,
		"children PROJ-epic --json":                       `[{"ID":"PROJ-s1","Status":"in_progress","Labels":["hard-tdd"]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Decision != "wait" {
		t.Fatalf("expected wait (claimed story must leave the dispatch queue), got %s: %s", result.Decision, result.Reason)
	}
	if result.Next != nil {
		t.Fatalf("expected no action for claimed story, got %#v", result.Next)
	}
}

// Regression: after PM approves RED (red-approved label, back in ready), the
// next developer dispatch must be the GREEN phase, and a GREEN delivery's PM
// review must carry phase green.
func TestEvaluateNext_EpicMode_RedApprovedAdvancesToGreen(t *testing.T) {
	withStubbedND(t, epicModeStubs(map[string]string{
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-epic": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-epic":     `[]`,
		"ready --sort priority --json --parent PROJ-epic":                                             `[{"ID":"PROJ-s1","Title":"GREEN work","Status":"open","Priority":1,"Parent":"PROJ-epic","Labels":["hard-tdd","red-approved"]}]`,
		"children PROJ-epic --json": `[{"ID":"PROJ-s1","Status":"open","Labels":["hard-tdd","red-approved"]}]`,
	}))

	result, err := EvaluateNext(t.TempDir(), "epic", "PROJ-epic", 1)
	if err != nil {
		t.Fatalf("EvaluateNext() error: %v", err)
	}
	if result.Next == nil || result.Next.Kind != "developer_new" {
		t.Fatalf("expected developer_new, got %#v", result.Next)
	}
	if result.Next.Phase != "green" {
		t.Fatalf("expected phase green after red-approved, got %q", result.Next.Phase)
	}
}
