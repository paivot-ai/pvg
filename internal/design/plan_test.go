package design

import (
	"path/filepath"
	"strings"
	"testing"
)

const planFixture = `# BUILD

## 9. Build plan (sealed trust layers)

Preamble prose that mentions M1 in passing and the Tenant machine, which
must not leak into any milestone block.

1. **M1 - Trust substrate (walking skeleton)**: the wall, the ledger, and the
   ` + "`ErasureRequest`" + ` machine conformant to its transition oracle; the Trial and
   QualificationSubmission machines.
   DoD: P-authz-oracle and T-isolation-oracle green over every row of their
   committed oracle files; ErasureRequest rows green; DEAL-eb0c40 exercised.
   Slices (epics, in order; each ends in its demo): (1) login and the wall;
   demo: two tenants, each admin sees only their own. (2) erase and certify;
   demo: a filed erasure certifies.
2. **M2 - Custody (the DMS)**: Document, DocumentVersion, and the
   documentRepository machine.
   DoD: custody transition oracles green. SEALED.

## 10. Language realization

3. **M9 - Not a milestone**: this marker sits under another section and is
   parsed as a block of its own, never as part of M2.
`

func TestParseMilestones(t *testing.T) {
	ms := ParseMilestones(planFixture)
	if len(ms) != 3 {
		t.Fatalf("expected 3 markers, got %d: %+v", len(ms), ms)
	}
	if ms[0].ID != "M1" || ms[0].Title != "Trust substrate (walking skeleton)" || ms[0].Line != 8 {
		t.Fatalf("M1 header: %+v", ms[0])
	}
	if ms[1].ID != "M2" || ms[1].Title != "Custody (the DMS)" {
		t.Fatalf("M2 header: %+v", ms[1])
	}
	if strings.Contains(ms[0].Text, "Custody") || !strings.Contains(ms[0].Text, "Slices (epics") {
		t.Fatalf("M1 block must end at the M2 marker: %q", ms[0].Text)
	}
	if strings.Contains(ms[1].Text, "Language realization") || strings.Contains(ms[1].Text, "M9") {
		t.Fatalf("M2 block must end at the next section heading: %q", ms[1].Text)
	}
	if strings.Contains(ms[0].Text, "Preamble") {
		t.Fatal("preamble prose before the first marker belongs to no milestone")
	}
}

func TestFindMilestone(t *testing.T) {
	ms := ParseMilestones(planFixture)
	if m, err := FindMilestone(ms, "m2"); err != nil || m.ID != "M2" {
		t.Fatalf("case-insensitive lookup: %+v %v", m, err)
	}
	_, err := FindMilestone(ms, "M7")
	if err == nil || !strings.Contains(err.Error(), "M1, M2, M9") {
		t.Fatalf("a miss must list the ids found, got %v", err)
	}
	if _, err := FindMilestone(nil, "M1"); err == nil || !strings.Contains(err.Error(), "no `**M<n> - title**` markers") {
		t.Fatalf("no markers must say so, got %v", err)
	}
}

func TestMilestonesReadsTheDesignPlan(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	cfg, _ := Load(root)
	if _, err := Milestones(root, cfg); err == nil || !strings.Contains(err.Error(), "design/BUILD.md") {
		t.Fatalf("a missing plan must fail loudly naming the file, got %v", err)
	}
	write(t, filepath.Join(root, "design", "BUILD.md"), planFixture)
	ms, err := Milestones(root, cfg)
	if err != nil || len(ms) != 3 {
		t.Fatalf("got %d milestones, err %v", len(ms), err)
	}
}

func TestMilestoneOracleScope(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	write(t, filepath.Join(root, "design", "machines", "ErasureRequest.oracle.md"),
		"# Generated transition oracle: `erasureRequest`\n\n"+strings.ReplaceAll(oracleFixture, "DEAL-", "ERAS-"))
	write(t, filepath.Join(root, "design", "machines", "Deal.oracle.md"), oracleFixture)
	write(t, filepath.Join(root, "design", "machines", "Document.oracle.md"),
		"# Generated transition oracle: `document`\n\n"+strings.ReplaceAll(oracleFixture, "DEAL-", "DOCU-"))
	write(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), policyFixture)
	write(t, filepath.Join(root, "design", "formal", "Isolation.oracle.md"), isolationFixture)
	cfg, _ := Load(root)
	oracles, err := LoadOracles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ms := ParseMilestones(planFixture)
	m1, _ := FindMilestone(ms, "M1")

	ids, named := MilestoneOracleScope(m1, oracles)
	wantNamed := []string{"formal/Isolation.oracle.md", "formal/Policy.oracle.md", "machines/ErasureRequest.oracle.md"}
	if strings.Join(named, ",") != strings.Join(wantNamed, ",") {
		t.Fatalf("named oracles = %v, want %v", named, wantNamed)
	}
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	// rule 2: every row of a named oracle
	for _, id := range []string{"ERAS-eb0c40", "ERAS-38ba11", "AUTHZ-a0788c", "AUTHZ-805492", "TENANT-3d8e8e"} {
		if !idSet[id] {
			t.Errorf("named-oracle id %s must be in scope", id)
		}
	}
	// rule 1: a directly cited id from an oracle the block does not name
	if !idSet["DEAL-eb0c40"] {
		t.Error("a stable id cited in the block is in scope even when its oracle is not named")
	}
	if idSet["DEAL-38ba11"] {
		t.Error("the uncited sibling row of an unnamed oracle stays out of scope")
	}
	// Document is M2's machine and appears nowhere in the M1 block
	if idSet["DOCU-eb0c40"] {
		t.Error("an oracle named only in another milestone is out of scope")
	}
	if len(ids) != 6 {
		t.Fatalf("expected 6 ids in scope, got %d: %v", len(ids), ids)
	}

	m2, _ := FindMilestone(ms, "M2")
	ids2, named2 := MilestoneOracleScope(m2, oracles)
	if strings.Join(named2, ",") != "machines/Document.oracle.md" || len(ids2) != 2 {
		t.Fatalf("M2 scope = %v / %v", named2, ids2)
	}
}
