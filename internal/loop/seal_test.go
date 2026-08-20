package loop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nestedIssue is one row of the fake nd backlog the seal tests answer from.
type nestedIssue struct {
	ID       string
	Title    string
	Type     string
	Status   string
	Parent   string
	Priority int
	Labels   []string
}

// stubNestedND installs an execCommand that answers `nd show`, `nd children`
// and `nd list --type epic` from an in-memory nested backlog. Every other nd
// query answers with an empty list, so queue counts read as zero (every
// story closed), which is exactly the state a seal decision is made in.
func stubNestedND(t *testing.T, projectRoot string, issues []nestedIssue) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".vault", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	byID := map[string]nestedIssue{}
	for _, i := range issues {
		byID[i.ID] = i
	}
	emit := func(list []nestedIssue) *exec.Cmd {
		type wire struct {
			ID       string   `json:"ID"`
			Title    string   `json:"Title"`
			Type     string   `json:"Type"`
			Status   string   `json:"Status"`
			Parent   string   `json:"Parent"`
			Priority int      `json:"Priority"`
			Labels   []string `json:"Labels"`
		}
		out := make([]wire, 0, len(list))
		for _, i := range list {
			out = append(out, wire{i.ID, i.Title, i.Type, i.Status, i.Parent, i.Priority, i.Labels})
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return exec.Command("echo", string(b))
	}

	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name != "nd" {
			return old(name, args...)
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "show "):
			for _, a := range args {
				if i, ok := byID[a]; ok {
					return emit([]nestedIssue{i})
				}
			}
			return exec.Command("echo", "[]")
		case strings.Contains(joined, "children "):
			var kids []nestedIssue
			for _, a := range args {
				if _, ok := byID[a]; !ok {
					continue
				}
				for _, i := range issues {
					if i.Parent == a {
						kids = append(kids, i)
					}
				}
				break
			}
			return emit(kids)
		case strings.Contains(joined, "list --type epic"):
			var epics []nestedIssue
			for _, i := range issues {
				if strings.EqualFold(i.Type, "epic") && !strings.EqualFold(i.Status, "closed") {
					epics = append(epics, i)
				}
			}
			return emit(epics)
		}
		return exec.Command("echo", "[]")
	}
	t.Cleanup(func() { execCommand = old })
}

func milestoneBacklog(sliceStatuses ...string) []nestedIssue {
	out := []nestedIssue{
		{ID: "PROJ-m1", Title: "M1 Trust substrate", Type: "epic", Status: "open", Priority: 1, Labels: []string{"milestone"}},
	}
	for i, st := range sliceStatuses {
		out = append(out, nestedIssue{
			ID: []string{"PROJ-s1", "PROJ-s2", "PROJ-s3"}[i], Title: "Slice", Type: "epic",
			Status: st, Parent: "PROJ-m1", Priority: 2,
		})
	}
	return out
}

// TestValidateDispatchEpicRefusesContainerEpic is the G8 dispatch rule: a
// milestone epic is never a loop target. Targeting it would run one
// completion gate over the whole layer and skip every slice gate.
func TestValidateDispatchEpicRefusesContainerEpic(t *testing.T) {
	root := t.TempDir()
	stubNestedND(t, root, milestoneBacklog("open", "open"))

	err := ValidateDispatchEpic(root, "PROJ-m1")
	if err == nil {
		t.Fatal("a container epic must be refused as a dispatch target")
	}
	if !strings.Contains(err.Error(), "PROJ-s1") || !strings.Contains(err.Error(), "target a slice epic") {
		t.Errorf("the error must name the slices and the remedy: %v", err)
	}

	if err := ValidateDispatchEpic(root, "PROJ-s1"); err != nil {
		t.Fatalf("a slice epic is a valid target: %v", err)
	}
	if !IsContainerEpic(root, "PROJ-m1") || IsContainerEpic(root, "PROJ-s1") {
		t.Error("IsContainerEpic")
	}
}

// TestSealableParent: the milestone is sealable exactly when every OTHER
// slice is already closed, so the loop can announce the seal as it finishes
// the last slice's own gate.
func TestSealableParent(t *testing.T) {
	root := t.TempDir()

	stubNestedND(t, root, milestoneBacklog("closed", "open"))
	if id, _ := SealableParent(root, "PROJ-s2"); id != "PROJ-m1" {
		t.Errorf("last open slice: expected PROJ-m1, got %q", id)
	}

	root2 := t.TempDir()
	stubNestedND(t, root2, milestoneBacklog("open", "open", "open"))
	if id, _ := SealableParent(root2, "PROJ-s1"); id != "" {
		t.Errorf("open siblings remain: expected no seal, got %q", id)
	}

	// A slice with no parent epic (flat model) never announces a seal:
	// backward compatibility for every existing project.
	root3 := t.TempDir()
	stubNestedND(t, root3, []nestedIssue{{ID: "PROJ-e1", Type: "epic", Status: "open"}})
	if id, _ := SealableParent(root3, "PROJ-e1"); id != "" {
		t.Errorf("flat epic: expected no seal, got %q", id)
	}
}

// TestFindSealableEpic: after the last slice closed, a scan finds the
// milestone that still owes its seal.
func TestFindSealableEpic(t *testing.T) {
	root := t.TempDir()
	stubNestedND(t, root, milestoneBacklog("closed", "closed"))
	id, title, err := FindSealableEpic(root)
	if err != nil {
		t.Fatal(err)
	}
	if id != "PROJ-m1" || title != "M1 Trust substrate" {
		t.Fatalf("expected the milestone, got %q %q", id, title)
	}

	root2 := t.TempDir()
	stubNestedND(t, root2, milestoneBacklog("closed", "open"))
	if id, _, err := FindSealableEpic(root2); err != nil || id != "" {
		t.Fatalf("an open slice means no seal yet: %q %v", id, err)
	}

	// A flat epic with only stories under it is not a container and never
	// produces a seal decision.
	root3 := t.TempDir()
	stubNestedND(t, root3, []nestedIssue{
		{ID: "PROJ-e1", Type: "epic", Status: "open"},
		{ID: "PROJ-a1", Type: "task", Status: "closed", Parent: "PROJ-e1"},
	})
	if id, _, err := FindSealableEpic(root3); err != nil || id != "" {
		t.Fatalf("flat epic with closed stories is not a seal: %q %v", id, err)
	}
}

// TestAutoSelectEpicSkipsContainers: the loop's automatic epic choice must
// land on a slice, never on the milestone that contains it.
func TestAutoSelectEpicSkipsContainers(t *testing.T) {
	root := t.TempDir()
	stubNestedND(t, root, milestoneBacklog("open", "open"))
	// Queue queries answer empty, so no epic has ready work and the
	// selection returns nothing -- what matters is that it never returns
	// the container.
	id, _, _ := AutoSelectEpic(root)
	if id == "PROJ-m1" {
		t.Fatal("AutoSelectEpic must never pick a container (milestone) epic")
	}
}

// TestMilestoneSealDecisionBlocksExit: `pvg loop next` in all-mode with no
// work left announces milestone_seal instead of complete, and EvaluateStop
// refuses to let the loop exit past it.
func TestMilestoneSealDecisionBlocksExit(t *testing.T) {
	root := t.TempDir()
	stubNestedND(t, root, milestoneBacklog("closed", "closed"))

	res, err := evaluateAllMode(root, NextResult{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionMilestoneSeal {
		t.Fatalf("expected %s, got %q (%s)", DecisionMilestoneSeal, res.Decision, res.Reason)
	}
	if res.SealEpic != "PROJ-m1" || res.SealEpicTitle != "M1 Trust substrate" {
		t.Fatalf("seal epic not reported: %+v", res)
	}
	if !strings.Contains(res.Reason, "pvg gates --seal") {
		t.Errorf("the reason must name the seal gate: %s", res.Reason)
	}

	decision := EvaluateStop(StopConfig{Active: true, SealPending: "PROJ-m1"})
	if decision.Allow {
		t.Fatal("a pending milestone seal must block exit")
	}
	if !strings.Contains(decision.Reason, "PROJ-m1") || !strings.Contains(decision.Reason, "seal") {
		t.Errorf("stop reason: %s", decision.Reason)
	}

	// No seal pending and no work: the loop exits as before.
	if d := EvaluateStop(StopConfig{Active: true}); !d.Allow {
		t.Fatalf("backward compatibility: an empty backlog still exits: %s", d.Reason)
	}
}

// TestEpicCompleteAnnouncesTheSealAhead: while the last slice is still open,
// finishing it yields epic_complete whose reason chains the slice gate, the
// milestone seal, and the rotation, so the dispatcher runs all three.
func TestEpicCompleteAnnouncesTheSealAhead(t *testing.T) {
	root := t.TempDir()
	stubNestedND(t, root, milestoneBacklog("closed", "open"))

	res, err := evaluateEpicMode(root, NextResult{TargetEpic: "PROJ-s2"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "epic_complete" {
		t.Fatalf("decision = %q", res.Decision)
	}
	if res.SealEpic != "PROJ-m1" {
		t.Fatalf("the seal must be announced with the completing slice: %+v", res)
	}
	if !strings.Contains(res.Reason, "milestone seal gate for PROJ-m1") {
		t.Errorf("reason: %s", res.Reason)
	}
}
