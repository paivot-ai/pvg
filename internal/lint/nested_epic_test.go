package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nestedBacklog is the milestone-over-slices shape: one milestone epic
// (PROJ-m1) holding two slice epics, each with its own stories, capstone and
// (for the first slice) the walking skeleton.
func nestedBacklog(t *testing.T) *Backlog {
	t.Helper()
	return buildBacklog(t, map[string]string{
		"PROJ-m1.md": "---\nid: PROJ-m1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n# M1 Trust substrate\n",
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: epic\nparent: PROJ-m1\n---\n# Slice 1 tenant boundary\nDoD: I create a tenant, see it isolated.\n",
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: epic\nparent: PROJ-m1\nblocked_by: [PROJ-s1]\n---\n# Slice 2 policy core\nDoD: I deny a cross-tenant read.\n",
		"PROJ-a1.md": "---\nid: PROJ-a1\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [walking-skeleton]\n---\n# Skeleton\n@spec, audit log, rate limit, config registry, error handling, DLP.\n",
		"PROJ-a2.md": "---\nid: PROJ-a2\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [capstone]\nblocked_by: [PROJ-a1]\n---\n# Slice 1 demo\n",
		"PROJ-b1.md": "---\nid: PROJ-b1\nstatus: open\ntype: task\nparent: PROJ-s2\n---\n# Policy decision core\n",
		"PROJ-b2.md": "---\nid: PROJ-b2\nstatus: open\ntype: task\nparent: PROJ-s2\nlabels: [capstone]\nblocked_by: [PROJ-b1]\n---\n# Slice 2 demo\n",
	})
}

// TestNestedMilestoneEpicIsAccepted is the G8 contract for lint: a milestone
// epic over slice epics needs no capstone story of its own (it seals when
// every slice closes), and its walking skeleton may live inside the first
// slice rather than as a direct child.
func TestNestedMilestoneEpicIsAccepted(t *testing.T) {
	b := nestedBacklog(t)

	if skel := checkWalkingSkeleton(b, scope{}, nil); len(skel) > 0 {
		t.Fatalf("the skeleton in a slice epic satisfies the milestone: %+v", skel)
	}
	caps := checkCapstone(b, scope{})
	for _, f := range caps {
		if f.Severity == SeverityError {
			t.Fatalf("nested model must not error: %+v", f)
		}
	}
}

// TestContainerEpicRejectsStrayStories: a story hanging directly off a
// milestone epic beside its slice epics belongs to no slice, so no slice
// completion gate would ever run it. That is an error, not a style note.
func TestContainerEpicRejectsStrayStories(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-m1.md":    "---\nid: PROJ-m1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n# M1\n",
		"PROJ-s1.md":    "---\nid: PROJ-s1\nstatus: open\ntype: epic\nparent: PROJ-m1\n---\n# Slice 1\n",
		"PROJ-a1.md":    "---\nid: PROJ-a1\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [walking-skeleton]\n---\n# Skeleton\n",
		"PROJ-a2.md":    "---\nid: PROJ-a2\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [capstone]\nblocked_by: [PROJ-a1]\n---\n# Demo\n",
		"PROJ-stray.md": "---\nid: PROJ-stray\nstatus: open\ntype: task\nparent: PROJ-m1\n---\n# Orphan work\n",
	})
	findings := findingsFor(checkCapstone(b, scope{}), "capstone")
	var errs []Finding
	for _, f := range findings {
		if f.Severity == SeverityError {
			errs = append(errs, f)
		}
	}
	if len(errs) != 1 || errs[0].IssueID != "PROJ-m1" {
		t.Fatalf("expected exactly one container error on the milestone: %+v", findings)
	}
	if !strings.Contains(errs[0].Message, "PROJ-stray") {
		t.Errorf("the message must name the stray story: %s", errs[0].Message)
	}
}

// TestContainerEpicWantsOrderedSlices: slices are sequential (each consumes
// the earlier ones), so unordered siblings are a review finding.
func TestContainerEpicWantsOrderedSlices(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-m1.md": "---\nid: PROJ-m1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n# M1\n",
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: epic\nparent: PROJ-m1\n---\n# Slice 1\n",
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: epic\nparent: PROJ-m1\n---\n# Slice 2\n",
		"PROJ-a1.md": "---\nid: PROJ-a1\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [walking-skeleton]\n---\n# Skeleton\n",
		"PROJ-a2.md": "---\nid: PROJ-a2\nstatus: open\ntype: task\nparent: PROJ-s1\nlabels: [capstone]\n---\n# Demo 1\n",
		"PROJ-b2.md": "---\nid: PROJ-b2\nstatus: open\ntype: task\nparent: PROJ-s2\nlabels: [capstone]\n---\n# Demo 2\n",
	})
	var reviews []Finding
	for _, f := range checkCapstone(b, scope{}) {
		if f.Severity == SeverityReview {
			reviews = append(reviews, f)
		}
	}
	if len(reviews) != 1 || !strings.Contains(reviews[0].Message, "blocked_by ordering") {
		t.Fatalf("expected the ordering review finding: %+v", reviews)
	}
	if nestedBacklogOrdered := nestedBacklog(t); len(findingsFor(checkCapstone(nestedBacklogOrdered, scope{}), "capstone")) != 0 {
		t.Error("ordered slices produce no capstone finding")
	}
}

// TestEpicScopeCoversTheWholeSubtree: `pvg lint --backlog --epic <milestone>`
// must reach the stories under its slice epics, not just its direct children.
func TestEpicScopeCoversTheWholeSubtree(t *testing.T) {
	b := nestedBacklog(t)
	sc, err := buildScope(b, "PROJ-m1")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PROJ-s1", "PROJ-s2"} {
		if !sc.epicIDs[id] {
			t.Errorf("slice epic %s must be in the milestone's scope", id)
		}
	}
	for _, id := range []string{"PROJ-a1", "PROJ-a2", "PROJ-b1", "PROJ-b2"} {
		if !sc.storyIDs[id] {
			t.Errorf("grandchild story %s must be in the milestone's scope", id)
		}
	}
}

// --- G14: hard-tdd pre-authorization ------------------------------------

func TestHardTDDPreauthorizedRequiresLabelOrRecordedExemption(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nlabels: [hard-tdd]\n---\n# Visual regression suite\n",
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: task\n---\n# Metamorphic fuzz suite\n",
		"PROJ-s3.md": "---\nid: PROJ-s3\nstatus: open\ntype: task\nlabels: [hard-tdd-exempt]\n---\n# Write the runbook\nHARD-TDD EXEMPT: documentation only, no product code.\n",
		"PROJ-s4.md": "---\nid: PROJ-s4\nstatus: open\ntype: task\nlabels: [hard-tdd-exempt]\n---\n# Config tweak\n",
		"PROJ-s5.md": "---\nid: PROJ-s5\nstatus: closed\ntype: task\n---\n# Already delivered\n",
		"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n# Epic carries no label\n",
	})

	// Setting off (the default): the check is silent everywhere.
	if f := checkHardTDDPreauthorized(b, scope{}, nil); len(f) != 0 {
		t.Fatalf("off by default: %+v", f)
	}

	findings := checkHardTDDPreauthorized(b, scope{}, map[string]string{"hard_tdd.preauthorized": "true"})
	byID := map[string]Finding{}
	for _, f := range findings {
		byID[f.IssueID] = f
	}
	if len(findings) != 2 {
		t.Fatalf("expected exactly PROJ-s2 (no label) and PROJ-s4 (exempt, unjustified): %+v", findings)
	}
	if _, ok := byID["PROJ-s2"]; !ok {
		t.Error("a story with no hard-tdd label must be flagged")
	}
	if f, ok := byID["PROJ-s4"]; !ok || !strings.Contains(f.Message, "recorded decision") {
		t.Errorf("an exemption without a justification line must be flagged: %+v", byID["PROJ-s4"])
	}
	for _, id := range []string{"PROJ-s1", "PROJ-s3", "PROJ-s5", "PROJ-e1"} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s must not be flagged", id)
		}
	}
}

// --- G9: paths-exist on a greenfield tree --------------------------------

// TestBrownfieldExplicitFalseWins: a greenfield monorepo whose design tree
// has a long commit history is not brownfield, and saying so must work.
func TestBrownfieldExplicitFalseWins(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", repo)
	for i := 0; i < brownfieldCommitThreshold+2; i++ {
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		run("-C", repo, "add", "f.txt")
		run("-C", repo, "commit", "-q", "-m", "c")
	}

	if !brownfieldEnabled(repo, nil) {
		t.Fatal("unset: the commit heuristic still applies")
	}
	if brownfieldEnabled(repo, map[string]string{"lint.brownfield": "false"}) {
		t.Fatal("an explicit lint.brownfield=false must turn the check off")
	}
	if !brownfieldEnabled(repo, map[string]string{"lint.brownfield": "true"}) {
		t.Fatal("an explicit true still forces it on")
	}
}

func TestPathsExistExcludesGreenfieldPrefixes(t *testing.T) {
	if got := pathsExistExcludes(map[string]string{"lint.paths_exist.exclude": " lib/ , test/ ,, "}); len(got) != 2 || got[0] != "lib/" || got[1] != "test/" {
		t.Fatalf("parse: %v", got)
	}
	if pathExcluded("lib/hextropian/policy.ex", []string{"lib/"}) != true ||
		pathExcluded("priv/repo/seeds.exs", []string{"lib/"}) != false {
		t.Fatal("prefix matching")
	}

	root := t.TempDir()
	b := buildBacklog(t, map[string]string{
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\n" +
			"Implement lib/hextropian/policy.ex and its test/hextropian/policy_test.exs, plus config/runtime.exs.\n",
	})

	flagged := checkPathsExistExcluding(b, scope{}, root, nil)
	if len(flagged) == 0 {
		t.Fatal("without excludes, the nonexistent paths are flagged")
	}
	kept := checkPathsExistExcluding(b, scope{}, root, []string{"lib/", "test/"})
	for _, f := range kept {
		if strings.Contains(f.Message, "lib/") || strings.Contains(f.Message, "test/") {
			t.Errorf("excluded prefix still flagged: %s", f.Message)
		}
	}
	if len(kept) == 0 {
		t.Error("config/runtime.exs is outside the excludes and stays flagged")
	}
}
