package rtm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// optIn enables the design substrate for a fixture project: design.machinery
// defaults to off, so tests exercising oracle-derived requirements must make
// the user's explicit choice first.
func optIn(t *testing.T, root, value string) {
	t.Helper()
	writeF(t, filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), "design.machinery: "+value+"\n")
}

// TestOracleIDsBecomeRequirements: on a machinery-managed project with the
// substrate explicitly enabled, every oracle stable id is a deterministic
// requirement checked by exact token match, beside the keyword-matched D&F
// requirements.
func TestOracleIDsBecomeRequirements(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	optIn(t, root, "auto")
	writeF(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeF(t, filepath.Join(root, "design", "machines", "Order.oracle.md"), `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-A-01 | ORD-aaaaaa | Open | on:pay | guardPaid | Paid | recordPayment |
| T-A-02 | ORD-bbbbbb | Open | on:cancel | - | Cancelled | recordCancel |
`)
	writeF(t, filepath.Join(vault, "issues", "S-1.md"),
		"---\nid: S-1\nstatus: open\n---\nAC keyed on ORD-aaaaaa.\n")

	res, err := CheckCoverage(root, vault)
	if err != nil {
		t.Fatal(err)
	}
	oracle := map[string]Requirement{}
	for _, r := range res.Requirements {
		if r.Tag == "[ORACLE]" {
			oracle[r.Text] = r
		}
	}
	if len(oracle) != 2 {
		t.Fatalf("expected 2 oracle requirements, got %+v", res.Requirements)
	}
	if r := oracle["ORD-aaaaaa"]; !r.Covered || r.CoveredBy != "S-1" {
		t.Fatalf("ORD-aaaaaa should be covered by S-1: %+v", r)
	}
	if r := oracle["ORD-bbbbbb"]; r.Covered {
		t.Fatalf("ORD-bbbbbb has no story and must be uncovered: %+v", r)
	}
	if res.Passed {
		t.Fatal("an uncovered oracle id fails the RTM")
	}
	if !strings.Contains(oracle["ORD-aaaaaa"].Source, "Order.oracle.md") {
		t.Fatalf("oracle requirement must name its file: %+v", oracle["ORD-aaaaaa"])
	}
}

// TestExactTokenNotSubstring: an id inside a longer identifier is no cover.
func TestExactTokenNotSubstring(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	optIn(t, root, "on")
	writeF(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeF(t, filepath.Join(root, "design", "machines", "O.oracle.md"), `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-1 | ORD-aaaaaa | A | on:x | - | B | act |
`)
	writeF(t, filepath.Join(vault, "issues", "S-1.md"),
		"---\nid: S-1\nstatus: open\n---\nMentions xORD-aaaaaa only inside a token.\n")
	res, err := CheckCoverage(root, vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Requirements {
		if r.Tag == "[ORACLE]" && r.Covered {
			t.Fatalf("substring must not cover: %+v", r)
		}
	}
}

// TestOracleIDsIgnoredByDefault: design.machinery defaults to off, so
// machinery artifacts alone must not contribute oracle requirements.
func TestOracleIDsIgnoredByDefault(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	// No settings file: the default (off) governs despite the artifacts.
	writeF(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeF(t, filepath.Join(root, "design", "machines", "O.oracle.md"), `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-1 | ORD-aaaaaa | A | on:x | - | B | act |
`)
	res, err := CheckCoverage(root, vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Requirements {
		if r.Tag == "[ORACLE]" {
			t.Fatalf("artifact presence alone must not enable the substrate: %+v", r)
		}
	}
	if !res.Passed {
		t.Fatalf("no requirements means the RTM passes: %+v", res)
	}

	// Explicit off behaves identically.
	optIn(t, root, "off")
	res, err = CheckCoverage(root, vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Requirements {
		if r.Tag == "[ORACLE]" {
			t.Fatalf("explicit off must exclude oracle requirements: %+v", r)
		}
	}
}
