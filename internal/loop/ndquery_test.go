package loop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHasLabel_Found(t *testing.T) {
	labels := []string{"bug", "delivered", "urgent"}
	if !hasLabel(labels, "delivered") {
		t.Error("expected to find 'delivered'")
	}
}

func TestHasLabel_CaseInsensitive(t *testing.T) {
	labels := []string{"Bug", "Delivered", "Urgent"}
	if !hasLabel(labels, "delivered") {
		t.Error("expected case-insensitive match for 'delivered'")
	}
}

func TestHasLabel_NotFound(t *testing.T) {
	labels := []string{"bug", "urgent"}
	if hasLabel(labels, "delivered") {
		t.Error("expected not to find 'delivered'")
	}
}

func TestHasLabel_EmptyLabels(t *testing.T) {
	if hasLabel(nil, "delivered") {
		t.Error("expected false for nil labels")
	}
	if hasLabel([]string{}, "delivered") {
		t.Error("expected false for empty labels")
	}
}

func TestNDIssue_JSONParsing_Array(t *testing.T) {
	input := `[
		{"ID": "PROJ-a1b", "Status": "in_progress", "Labels": ["delivered", "bug"], "Type": "story"},
		{"ID": "PROJ-c3d", "Status": "ready", "Labels": [], "Type": "story"}
	]`

	var issues []ndIssue
	if err := json.Unmarshal([]byte(input), &issues); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].ID != "PROJ-a1b" {
		t.Errorf("expected PROJ-a1b, got %s", issues[0].ID)
	}
	if issues[0].Status != "in_progress" {
		t.Errorf("expected in_progress, got %s", issues[0].Status)
	}
	if !hasLabel(issues[0].Labels, "delivered") {
		t.Error("expected delivered label on first issue")
	}
	if issues[1].Type != "story" {
		t.Errorf("expected story type, got %s", issues[1].Type)
	}
}

func TestNDIssue_JSONParsing_Single(t *testing.T) {
	input := `{"ID": "PROJ-x1y", "Status": "ready", "Labels": ["epic"], "Type": "epic"}`

	var issue ndIssue
	if err := json.Unmarshal([]byte(input), &issue); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if issue.ID != "PROJ-x1y" {
		t.Errorf("expected PROJ-x1y, got %s", issue.ID)
	}
	if issue.Type != "epic" {
		t.Errorf("expected epic, got %s", issue.Type)
	}
}

func TestNDIssue_JSONParsing_EmptyLabels(t *testing.T) {
	input := `{"ID": "TEST-001", "Status": "ready", "Labels": null, "Type": "story"}`

	var issue ndIssue
	if err := json.Unmarshal([]byte(input), &issue); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if issue.Labels != nil {
		t.Errorf("expected nil labels, got %v", issue.Labels)
	}
	if hasLabel(issue.Labels, "delivered") {
		t.Error("hasLabel should return false for nil labels")
	}
}

func TestRunND_UsesResolvedVault(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	if _, err := runND(t.TempDir(), "ready", "--json"); err != nil {
		t.Fatalf("runND() error: %v", err)
	}

	want := []string{"nd", "--vault", override, "ready", "--json"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("unexpected nd call: got %#v want %#v", calls, want)
	}
}

func TestQueryWorkCounts_EpicModeStillQueriesWholeBacklog(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	if _, err := QueryWorkCounts(t.TempDir(), "epic", "PROJ-epic"); err != nil {
		t.Fatalf("QueryWorkCounts() error: %v", err)
	}

	want := [][]string{
		{"nd", "--vault", override, "list", "--status", "!closed", "--label", "delivered", "--limit", "0", "--json"},
		{"nd", "--vault", override, "ready", "--json"},
		{"nd", "--vault", override, "list", "--status", "in_progress", "--limit", "0", "--json"},
		{"nd", "--vault", override, "list", "--status", "open", "--label", "rejected", "--limit", "0", "--json"},
		{"nd", "--vault", override, "blocked", "--json"},
		{"nd", "--vault", override, "list", "--status", "!closed", "--limit", "0", "--json"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected nd calls:\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestCountOtherIssues(t *testing.T) {
	ready := []ndIssue{{ID: "PROJ-ready"}}
	inProgress := []ndIssue{{ID: "PROJ-dev"}}
	blocked := []ndIssue{{ID: "PROJ-blocked"}}
	all := []ndIssue{
		{ID: "PROJ-ready", Status: "open"},
		{ID: "PROJ-dev", Status: "in_progress"},
		{ID: "PROJ-blocked", Status: "blocked"},
		{ID: "PROJ-review", Status: "review"},
		{ID: "PROJ-qa", Status: "qa"},
	}

	if got := countOtherIssues(ready, inProgress, blocked, all); got != 2 {
		t.Fatalf("expected 2 other issues, got %d", got)
	}
}

func TestAutoSelectEpic_PicksHighestPriorityWithActionableWork(t *testing.T) {
	withStubbedND(t, map[string]string{
		// List non-closed epics sorted by priority
		"list --type epic --status !closed --sort priority --limit 0 --json": `[
			{"ID":"PROJ-e1","Title":"Epic One","Type":"epic","Priority":0},
			{"ID":"PROJ-e2","Title":"Epic Two","Type":"epic","Priority":1}
		]`,
		// Epic One has no actionable work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e1": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e1":         `[]`,
		"ready --sort priority --json --parent PROJ-e1":                                                 `[]`,
		// Epic Two has ready work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e2": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e2":         `[]`,
		"ready --sort priority --json --parent PROJ-e2":                                                 `[{"ID":"PROJ-s1","Title":"Story","Status":"ready"}]`,
	})

	id, title, err := AutoSelectEpic(t.TempDir())
	if err != nil {
		t.Fatalf("AutoSelectEpic() error: %v", err)
	}
	if id != "PROJ-e2" {
		t.Fatalf("expected PROJ-e2, got %s", id)
	}
	if title != "Epic Two" {
		t.Fatalf("expected 'Epic Two', got %s", title)
	}
}

func TestAutoSelectEpic_RespectsExcludeList(t *testing.T) {
	withStubbedND(t, map[string]string{
		"list --type epic --status !closed --sort priority --limit 0 --json": `[
			{"ID":"PROJ-e1","Title":"Epic One","Type":"epic","Priority":0},
			{"ID":"PROJ-e2","Title":"Epic Two","Type":"epic","Priority":1}
		]`,
		// Epic One has work but is excluded
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e1": `[{"ID":"PROJ-d1","Title":"Delivered","Status":"in_progress","Labels":["delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e1":         `[]`,
		"ready --sort priority --json --parent PROJ-e1":                                                 `[]`,
		// Epic Two also has work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e2": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e2":         `[]`,
		"ready --sort priority --json --parent PROJ-e2":                                                 `[{"ID":"PROJ-s2","Title":"Story","Status":"ready"}]`,
	})

	id, _, err := AutoSelectEpic(t.TempDir(), "PROJ-e1")
	if err != nil {
		t.Fatalf("AutoSelectEpic() error: %v", err)
	}
	if id != "PROJ-e2" {
		t.Fatalf("expected PROJ-e2 after excluding PROJ-e1, got %s", id)
	}
}

func TestAutoSelectEpic_ReturnsEmptyWhenNoActionableEpics(t *testing.T) {
	withStubbedND(t, map[string]string{
		"list --type epic --status !closed --sort priority --limit 0 --json": `[
			{"ID":"PROJ-e1","Title":"Epic One","Type":"epic"}
		]`,
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e1": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e1":         `[]`,
		"ready --sort priority --json --parent PROJ-e1":                                                 `[]`,
	})

	id, title, err := AutoSelectEpic(t.TempDir())
	if err != nil {
		t.Fatalf("AutoSelectEpic() error: %v", err)
	}
	if id != "" || title != "" {
		t.Fatalf("expected empty result, got id=%s title=%s", id, title)
	}
}

func TestAutoSelectEpic_ReturnsEmptyWhenNoEpicsExist(t *testing.T) {
	withStubbedND(t, map[string]string{
		"list --type epic --status !closed --sort priority --limit 0 --json": `[]`,
	})

	id, _, err := AutoSelectEpic(t.TempDir())
	if err != nil {
		t.Fatalf("AutoSelectEpic() error: %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty id, got %s", id)
	}
}

func TestAutoSelectEpic_PrefersDeliveredOverReady(t *testing.T) {
	withStubbedND(t, map[string]string{
		"list --type epic --status !closed --sort priority --limit 0 --json": `[
			{"ID":"PROJ-e1","Title":"Epic One","Type":"epic","Priority":0},
			{"ID":"PROJ-e2","Title":"Epic Two","Type":"epic","Priority":1}
		]`,
		// Epic One has delivered work (pipeline needs unblocking)
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e1": `[{"ID":"PROJ-d1","Title":"Delivered","Status":"in_progress","Labels":["delivered"]}]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e1":         `[]`,
		"ready --sort priority --json --parent PROJ-e1":                                                 `[]`,
		// Epic Two has ready work
		"list --status !closed --label delivered --sort priority --limit 0 --json --parent PROJ-e2": `[]`,
		"list --status open --label rejected --sort priority --limit 0 --json --parent PROJ-e2":         `[]`,
		"ready --sort priority --json --parent PROJ-e2":                                                 `[{"ID":"PROJ-s2","Title":"Story","Status":"ready"}]`,
	})

	id, _, err := AutoSelectEpic(t.TempDir())
	if err != nil {
		t.Fatalf("AutoSelectEpic() error: %v", err)
	}
	// PROJ-e1 is higher priority (P0) and has delivered work
	if id != "PROJ-e1" {
		t.Fatalf("expected PROJ-e1 (has delivered work, higher priority), got %s", id)
	}
}

func TestQueryEpicCounts_ClassifiesOpenChildrenByGraphBlockedSet(t *testing.T) {
	withStubbedND(t, map[string]string{
		"children PROJ-epic --json": `[
			{"ID":"PROJ-s1","Status":"open","Labels":[]},
			{"ID":"PROJ-s2","Status":"open","Labels":[]},
			{"ID":"PROJ-s3","Status":"open","Labels":["rejected"]},
			{"ID":"PROJ-s4","Status":"in_progress","Labels":["delivered"]},
			{"ID":"PROJ-s5","Status":"in_progress","Labels":[]},
			{"ID":"PROJ-s6","Status":"blocked","Labels":[]},
			{"ID":"PROJ-s7","Status":"deferred","Labels":[]},
			{"ID":"PROJ-s8","Status":"closed","Labels":[]}
		]`,
		// PROJ-s2 is open but graph-blocked (open blockers).
		"blocked --json": `[{"ID":"PROJ-s2","Status":"open","Labels":[]}]`,
	})

	wc, err := QueryEpicCounts(t.TempDir(), "PROJ-epic")
	if err != nil {
		t.Fatalf("QueryEpicCounts() error: %v", err)
	}

	want := WorkCounts{Ready: 1, Rejected: 1, Delivered: 1, InProgress: 1, Blocked: 2, Other: 1}
	if !reflect.DeepEqual(wc, want) {
		t.Fatalf("unexpected counts: got %+v want %+v", wc, want)
	}
}

func TestQueryEpicCounts_AllOpenChildrenGraphBlockedYieldsBlockedOnly(t *testing.T) {
	withStubbedND(t, map[string]string{
		"children PROJ-epic --json": `[
			{"ID":"PROJ-s1","Status":"open","Labels":[]},
			{"ID":"PROJ-s2","Status":"open","Labels":[]}
		]`,
		"blocked --json": `[{"ID":"PROJ-s1"},{"ID":"PROJ-s2"}]`,
	})

	wc, err := QueryEpicCounts(t.TempDir(), "PROJ-epic")
	if err != nil {
		t.Fatalf("QueryEpicCounts() error: %v", err)
	}
	if wc.Blocked != 2 || wc.Ready != 0 || wc.Other != 0 {
		t.Fatalf("expected 2 blocked / 0 ready / 0 other, got %+v", wc)
	}
}

func TestQueryWorkCounts_ReturnsErrorWhenNDQueriesFail(t *testing.T) {
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	defer func() { execCommand = oldExec }()

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	if _, err := QueryWorkCounts(t.TempDir(), "all", ""); err == nil {
		t.Fatal("expected QueryWorkCounts to return an error when nd queries fail")
	}
}
