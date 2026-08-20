package rtm

import (
	"path/filepath"
	"strings"
	"testing"
)

const scopePlan = `# BUILD

## 9. Build plan

1. **M1 - Trust substrate (walking skeleton)**: the ErasureRequest machine end to end.
   DoD: P-authz-oracle and T-isolation-oracle green over every row; ErasureRequest rows green.
   Slices: (1) login and the wall; demo: two tenants. (2) erase and certify; demo: a certificate.
2. **M2 - Custody**: the Document machine.
   DoD: custody transition oracles green. SEALED.
`

const scopePolicy = `# Generated authorization oracle: policy

## Decisions

| test id | stable id | verb | role | owner case | target | expectation | invariants |
|---|---|---|---|---|---|---|---|
| O-AUTHZ-01 | AUTHZ-a0788c | create | platform_admin | - | - | allow | rbac |
| O-AUTHZ-02 | AUTHZ-805492 | create | tenant_admin | - | - | allow | rbac |
`

const scopeIsolation = `# Generated tenant-scoping oracle: isolation

## Decisions

| test id | stable id | reference | tenant case | expectation | invariants |
|---|---|---|---|---|---|
| O-TENANT-01 | TENANT-3d8e8e | ComplianceTask.person -> Person | same-tenant | allow | same-tenant |
`

func machineOracle(name, prefix string) string {
	return "# Generated transition oracle: `" + name + "`\n\n## Transitions\n\n" +
		"| test id | stable id | source | trigger | guard | target | actions |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| T-X-01 | " + prefix + "-aaaaaa | A | on:x | - | B | act |\n" +
		"| T-X-02 | " + prefix + "-bbbbbb | B | on:y | - | C | act |\n"
}

// scopedProject: two machines (ErasureRequest for M1, Document for M2), the
// two formal oracles, a build plan, and a backlog with a milestone epic, a
// slice epic under it, and stories at both levels.
func scopedProject(t *testing.T) (root, vault string) {
	t.Helper()
	root = t.TempDir()
	vault = t.TempDir()
	optIn(t, root, "on")
	writeF(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeF(t, filepath.Join(root, "design", "BUILD.md"), scopePlan)
	writeF(t, filepath.Join(root, "design", "machines", "ErasureRequest.oracle.md"), machineOracle("erasureRequest", "ERAS"))
	writeF(t, filepath.Join(root, "design", "machines", "Document.oracle.md"), machineOracle("document", "DOCU"))
	writeF(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), scopePolicy)
	writeF(t, filepath.Join(root, "design", "formal", "Isolation.oracle.md"), scopeIsolation)

	// M1 milestone epic -> slice epic -> stories; one closed story covers a formal id.
	writeF(t, filepath.Join(vault, "issues", "H-m1.md"), "---\nid: H-m1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\nM1 Trust substrate.\n")
	writeF(t, filepath.Join(vault, "issues", "H-s1.md"), "---\nid: H-s1\nstatus: open\ntype: epic\nparent: H-m1\n---\nSlice 1: login and the wall.\n")
	writeF(t, filepath.Join(vault, "issues", "H-a.md"), "---\nid: H-a\nstatus: open\ntype: task\nparent: H-s1\n---\nAC: ERAS-aaaaaa and AUTHZ-a0788c.\n")
	writeF(t, filepath.Join(vault, "issues", "H-b.md"), "---\nid: H-b\nstatus: closed\ntype: task\nparent: H-s1\n---\nAC: TENANT-3d8e8e delivered.\n")
	// M2 epic with a story covering Document rows and, wrongly, one M1 row.
	writeF(t, filepath.Join(vault, "issues", "H-m2.md"), "---\nid: H-m2\nstatus: open\ntype: epic\nlabels: [milestone]\n---\nM2 Custody.\n")
	writeF(t, filepath.Join(vault, "issues", "H-c.md"), "---\nid: H-c\nstatus: open\ntype: task\nparent: H-m2\n---\nAC: DOCU-aaaaaa, DOCU-bbbbbb, and AUTHZ-805492.\n")
	return root, vault
}

// TestFormalOracleIDsAreRequirements: Policy and Isolation rows are [ORACLE]
// requirements keyed on their stable ids, with their formal/ source.
func TestFormalOracleIDsAreRequirements(t *testing.T) {
	root, vault := scopedProject(t)
	res, err := CheckCoverage(root, vault)
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]int{}
	for _, r := range res.Requirements {
		if r.Tag == "[ORACLE]" {
			bySource[r.Source]++
		}
	}
	if bySource["design/formal/Policy.oracle.md"] != 2 || bySource["design/formal/Isolation.oracle.md"] != 1 {
		t.Fatalf("formal oracle rows must be requirements: %v", bySource)
	}
	if res.Total != 7 || res.Covered != 6 || res.Uncovered != 1 {
		t.Fatalf("full run: %+v", res)
	}
	if res.Passed {
		t.Fatal("ERAS-bbbbbb has no story and must fail the full RTM")
	}
	if len(res.ByOracle) != 4 {
		t.Fatalf("per-oracle breakdown must list every oracle: %+v", res.ByOracle)
	}
	for _, oc := range res.ByOracle {
		if oc.Oracle == "machines/ErasureRequest.oracle.md" && (oc.Total != 2 || oc.Covered != 1 || len(oc.Uncovered) != 1 || oc.Uncovered[0] != "ERAS-bbbbbb") {
			t.Fatalf("ErasureRequest breakdown: %+v", oc)
		}
	}
	text := FormatText(res)
	if !strings.Contains(text, "machines/ErasureRequest.oracle.md: 1/2 covered") || !strings.Contains(text, "ERAS-bbbbbb") {
		t.Fatalf("text report must carry the breakdown and the uncovered list:\n%s", text)
	}
}

// TestMilestoneScope: --milestone M1 selects the ids the M1 block names
// (ErasureRequest rows, Policy and Isolation rows) and nothing from M2.
func TestMilestoneScope(t *testing.T) {
	root, vault := scopedProject(t)
	res, err := CheckCoverageWithOptions(root, vault, Options{Milestone: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope == nil || res.Scope.Milestone != "M1" || res.Scope.MilestoneTitle != "Trust substrate (walking skeleton)" {
		t.Fatalf("scope: %+v", res.Scope)
	}
	want := "formal/Isolation.oracle.md,formal/Policy.oracle.md,machines/ErasureRequest.oracle.md"
	if strings.Join(res.Scope.Oracles, ",") != want {
		t.Fatalf("named oracles = %v", res.Scope.Oracles)
	}
	if res.Total != 5 {
		t.Fatalf("M1 scope must carry 5 ids (2 ERAS + 2 AUTHZ + 1 TENANT), got %d: %+v", res.Total, res.Requirements)
	}
	for _, r := range res.Requirements {
		if strings.HasPrefix(r.Text, "DOCU-") {
			t.Fatalf("M2's Document rows must be out of M1 scope: %+v", r)
		}
	}
	// Unscoped story set: AUTHZ-805492 is covered by the M2 story H-c.
	if res.Covered != 4 || res.Uncovered != 1 || res.Passed {
		t.Fatalf("M1 scope over the whole backlog: %+v", res)
	}
	if !strings.Contains(FormatText(res), "[RTM] scope: milestone M1 (Trust substrate (walking skeleton))") {
		t.Fatalf("report header must name the scope:\n%s", FormatText(res))
	}

	// M2 scope is clean: both Document rows are covered by H-c.
	res2, err := CheckCoverageWithOptions(root, vault, Options{Milestone: "M2"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Total != 2 || !res2.Passed {
		t.Fatalf("M2 scope: %+v", res2)
	}

	if _, err := CheckCoverageWithOptions(root, vault, Options{Milestone: "M7"}); err == nil || !strings.Contains(err.Error(), "M1, M2") {
		t.Fatalf("unknown milestone must list the markers found, got %v", err)
	}
}

// TestEpicScopeRestrictsCoveringStories: --epic restricts which stories may
// cover; combined with --milestone it answers "which M1 ids does the M1 epic
// (and its slice epics) leave uncovered".
func TestEpicScopeRestrictsCoveringStories(t *testing.T) {
	root, vault := scopedProject(t)
	res, err := CheckCoverageWithOptions(root, vault, Options{Milestone: "M1", Epic: "H-m1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope.Epic != "H-m1" || res.Stories != 4 || res.StoriesClosed != 1 {
		t.Fatalf("epic subtree must hold the milestone epic, the slice epic, and both stories: %+v", res)
	}
	uncovered := map[string]bool{}
	for _, r := range res.Requirements {
		if !r.Covered {
			uncovered[r.Text] = true
		}
	}
	// AUTHZ-805492 is covered only by the M2 story, which is outside the H-m1 subtree.
	if !uncovered["AUTHZ-805492"] || !uncovered["ERAS-bbbbbb"] || len(uncovered) != 2 {
		t.Fatalf("uncovered within the M1 epic = %v", uncovered)
	}
	for _, r := range res.Requirements {
		if r.Text == "TENANT-3d8e8e" && (!r.Covered || !r.CoveredByClosed || r.CoveredBy != "H-b") {
			t.Fatalf("the closed slice story covers TENANT-3d8e8e: %+v", r)
		}
	}

	if _, err := CheckCoverageWithOptions(root, vault, Options{Epic: "H-none"}); err == nil || !strings.Contains(err.Error(), "no issues in its subtree") {
		t.Fatalf("an unknown epic must fail loudly, got %v", err)
	}
}

// TestOracleScope: --oracle selects oracle files by name, stem, or path.
func TestOracleScope(t *testing.T) {
	root, vault := scopedProject(t)
	res, err := CheckCoverageWithOptions(root, vault, Options{Oracles: []string{"policy", "Isolation"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || !res.Passed {
		t.Fatalf("formal-only scope must be the 3 decision rows, all covered: %+v", res)
	}
	if strings.Join(res.Scope.Oracles, ",") != "formal/Isolation.oracle.md,formal/Policy.oracle.md" {
		t.Fatalf("scope oracles = %v", res.Scope.Oracles)
	}
	if _, err := CheckCoverageWithOptions(root, vault, Options{Oracles: []string{"Nope"}}); err == nil || !strings.Contains(err.Error(), "matches no committed oracle") {
		t.Fatalf("unknown selector must fail loudly listing the oracles, got %v", err)
	}
}

// TestScopeRequiresSubstrate: scoping is meaningless off the substrate and
// must refuse rather than report zero requirements as a pass.
func TestScopeRequiresSubstrate(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	writeF(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	if _, err := CheckCoverageWithOptions(root, vault, Options{Milestone: "M1"}); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("scope without the substrate must refuse, got %v", err)
	}
}
