package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckHardTDDOracle_FormalOracleIDs: a story citing a Policy or
// Isolation decision row (design/formal/*.oracle.md) derives locked RED
// tests exactly like a transition row, so the hard-tdd label is required.
func TestCheckHardTDDOracle_FormalOracleIDs(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "design", "formal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "design", "domain.modelith.yaml"), []byte("model: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := "# Generated authorization oracle: policy\n\n## Decisions\n\n" +
		"| test id | stable id | verb | role | owner case | target | expectation | invariants |\n" +
		"|---|---|---|---|---|---|---|---|\n" +
		"| O-AUTHZ-01 | AUTHZ-a0788c | create | platform_admin | - | - | allow | rbac |\n"
	if err := os.WriteFile(filepath.Join(projectRoot, "design", "formal", "Policy.oracle.md"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}

	b := buildBacklog(t, map[string]string{
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\nConformance row AUTHZ-a0788c.\n",
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: task\nlabels: [hard-tdd]\n---\nConformance row AUTHZ-a0788c.\n",
	})
	findings := checkHardTDDOracle(b, scope{}, projectRoot, map[string]string{"design.machinery": "on"})
	if len(findings) != 1 || findings[0].IssueID != "PROJ-s1" || !strings.Contains(findings[0].Message, "AUTHZ-a0788c") {
		t.Fatalf("expected one error for PROJ-s1 naming the formal id, got %+v", findings)
	}
}
