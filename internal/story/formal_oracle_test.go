package story

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const formalPolicyFixture = `# Generated authorization oracle: policy

## Decisions

| test id | stable id | verb | role | owner case | target | expectation | invariants |
|---|---|---|---|---|---|---|---|
| O-AUTHZ-01 | AUTHZ-a0788c | create | platform_admin | - | - | allow | rbac |
| O-AUTHZ-02 | AUTHZ-805492 | create | tenant_admin | - | - | allow | rbac |
`

// TestDesignRedGateCoversFormalOracleIDs: a story citing a Policy
// (formal) stable id needs a test carrying that id exactly like a machine
// row; the RED exit gate keys on both oracle kinds.
func TestDesignRedGateCoversFormalOracleIDs(t *testing.T) {
	root := managedProject(t)
	writeFile(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), formalPolicyFixture)
	fakeMachinery(t, 0)

	_, err := designRedGate(root, "S-1", "ACs: AUTHZ-a0788c and DEAL-eb0c40")
	if err == nil || !strings.Contains(err.Error(), "AUTHZ-a0788c") {
		t.Fatalf("an uncovered formal id must block approval naming it, got %v", err)
	}

	writeFile(t, filepath.Join(root, "test", "authz_oracle_test.exs"), "# P-authz-oracle rows\n# AUTHZ-a0788c\n# DEAL-eb0c40\n")
	note, err := designRedGate(root, "S-1", "ACs: AUTHZ-a0788c and DEAL-eb0c40")
	if err != nil {
		t.Fatalf("covered formal + machine ids must pass: %v", err)
	}
	if !strings.Contains(note, "2 oracle stable id(s) covered") {
		t.Fatalf("note = %q", note)
	}
}

// TestSyncOracleDiffsFormalOracles: a revised Policy table diffs by stable
// id like a machine oracle and maps onto the stories that cite the rows.
func TestSyncOracleDiffsFormalOracles(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	writeFile(t, filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), "design.machinery: on\n")
	writeFile(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeFile(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), formalPolicyFixture)
	run("add", ".")
	run("commit", "-q", "-m", "v1")

	// v2 flips the second row's expectation under the same stable id and adds a row.
	v2 := strings.Replace(formalPolicyFixture, "| O-AUTHZ-02 | AUTHZ-805492 | create | tenant_admin | - | - | allow | rbac |",
		"| O-AUTHZ-02 | AUTHZ-805492 | create | tenant_admin | - | - | deny | rbac |\n| O-AUTHZ-03 | AUTHZ-050542 | create | view_only | - | - | deny | rbac |", 1)
	writeFile(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), v2)

	vault := t.TempDir()
	writeFile(t, filepath.Join(vault, "issues", "S-1.md"), "---\nid: S-1\nstatus: open\n---\nAC: AUTHZ-805492 tenant_admin may create.\n")
	t.Setenv("ND_VAULT_DIR", vault)

	rep, err := SyncOracle(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SyncEntry{}
	for _, e := range rep.Entries {
		byID[e.ID] = e
	}
	if len(rep.Entries) != 2 {
		t.Fatalf("expected modified + added, got %+v", rep.Entries)
	}
	if e := byID["AUTHZ-805492"]; e.Change != "modified" || e.Oracle != "design/formal/Policy.oracle.md" || len(e.Stories) != 1 || e.Stories[0] != "S-1" {
		t.Fatalf("AUTHZ-805492: %+v", e)
	}
	if e := byID["AUTHZ-050542"]; e.Change != "added" || len(e.Stories) != 0 {
		t.Fatalf("AUTHZ-050542: %+v", e)
	}
	if rep.Uncovered != 1 {
		t.Fatalf("the added row has no covering story: %+v", rep)
	}
}
