package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("WD9YMI_AC_EDITOR") == "1" {
		if err := runFormalACEditor(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFormalACEditor() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("formal AC editor requires an issue path")
	}
	path := os.Args[len(os.Args)-1]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const heading = "## Acceptance Criteria\n"
	start := strings.Index(string(data), heading)
	if start < 0 {
		return fmt.Errorf("formal AC heading missing from %s", path)
	}
	contentStart := start + len(heading)
	end := strings.Index(string(data)[contentStart:], "\n## Design\n")
	if end < 0 {
		return fmt.Errorf("Design heading missing after formal AC in %s", path)
	}
	end += contentStart
	replacement := "## Acceptance Criteria\n- [ ] The command fails closed and names the story, section, and repair.\n\n"
	updated := string(data)[:start] + replacement + string(data)[end+1:]
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), info.Mode().Perm())
}

func TestFormalAcceptanceCriteriaIntegration(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	vault := filepath.Join(root, "nd-vault")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("nd", "init", "--prefix", "PROJ", "--vault", vault).CombinedOutput(); err != nil {
		t.Fatalf("nd init: %v\n%s", err, out)
	}

	binary := filepath.Join(root, "pvg")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build pvg: %v\n%s", err, out)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "ND_VAULT_DIR="+vault)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("pvg %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}
	fail := func(want string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "ND_VAULT_DIR="+vault)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		if err == nil {
			t.Fatalf("pvg %s unexpectedly passed:\n%s", strings.Join(args, " "), out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("pvg %s error does not contain %q:\n%s", strings.Join(args, " "), want, out.String())
		}
		return out.String()
	}

	issue := createBlankACIssue(t, run, "Formal AC transition")

	// A detailed Description and delivery-note table cannot replace the formal
	// contract at the delivery gate.
	deliverEvidence(t, run, issue)
	deliverOutput := fail("formal acceptance criteria section", "story", "deliver", issue)
	if !strings.Contains(deliverOutput, issue) || !strings.Contains(deliverOutput, "## Acceptance Criteria") || !strings.Contains(deliverOutput, "repair:") {
		t.Fatalf("delivery diagnostic was incomplete:\n%s", deliverOutput)
	}

	verifyOutput := fail("formal:acceptance_criteria", "story", "verify-delivery", issue)
	if !strings.Contains(verifyOutput, "[OK]   proof:ac_items") {
		t.Fatalf("surrogate delivery-note AC items should not substitute for the formal section:\n%s", verifyOutput)
	}

	acceptOutput := fail("formal acceptance criteria section", "story", "accept", issue, "--reason", "should not close")
	if !strings.Contains(acceptOutput, issue) || !strings.Contains(acceptOutput, "repair:") {
		t.Fatalf("acceptance diagnostic was incomplete:\n%s", acceptOutput)
	}
	assertIssueShape(t, run, issue, "in_progress", []string{"delivered"})

	// Nd owns the formal section as a manual-edit section. Use `nd edit` so nd
	// performs the write and updates its content hash; the test never writes
	// the issue markdown directly.
	editor := exec.Command("nd", "--vault", vault, "edit", issue)
	editor.Dir = project
	editor.Env = append(os.Environ(),
		"ND_VAULT_DIR="+vault,
		"EDITOR="+os.Args[0],
		"WD9YMI_AC_EDITOR=1",
	)
	if out, err := editor.CombinedOutput(); err != nil {
		t.Fatalf("nd edit: %v\n%s", err, out)
	}
	run("nd", "labels", "rm", issue, "delivered")

	lintOutput := run("lint", "--backlog")
	if !strings.Contains(lintOutput, "PASSED: 0 error(s), 0 review finding(s)") {
		t.Fatalf("repaired backlog lint output = %q", lintOutput)
	}
	run("story", "deliver", issue)
	verifyOutput = run("story", "verify-delivery", issue)
	if !strings.Contains(verifyOutput, "Passed: 10, Failed: 0") {
		t.Fatalf("repaired delivery verification output = %q", verifyOutput)
	}
	run("story", "accept", issue, "--reason", "Accepted by integration test")
	assertIssueShape(t, run, issue, "closed", []string{"accepted"})

	// Construct an already accepted legacy record without changing its body,
	// then prove verification reports it without mutating content or hash.
	historical := createBlankACIssue(t, run, "Historical formal AC")
	deliverEvidence(t, run, historical)
	run("issues", "close", historical, "--reason=Historical accepted fixture")
	run("nd", "labels", "add", historical, "accepted")
	before := showIssue(t, run, historical)
	legacyVerify := fail("legacy accepted record; not rewritten", "story", "verify-delivery", historical)
	if !strings.Contains(legacyVerify, "formal:acceptance_criteria") {
		t.Fatalf("legacy verification output = %q", legacyVerify)
	}
	legacyLint := run("lint", "--backlog")
	if !strings.Contains(legacyLint, "PASSED: 0 error(s), 0 review finding(s)") {
		t.Fatalf("legacy backlog lint output = %q", legacyLint)
	}
	after := showIssue(t, run, historical)
	if after.Body != before.Body || after.Extras["content_hash"] != before.Extras["content_hash"] {
		t.Fatalf("historical record changed:\nbefore=%+v\nafter=%+v", before, after)
	}
}

type integrationIssue struct {
	ID     string
	Status string
	Labels []string
	Body   string
	Extras map[string]interface{}
}

func createBlankACIssue(t *testing.T, run func(...string) string, title string) string {
	t.Helper()
	out := run(
		"issues", "create", title,
		"--type", "bug",
		"--body", "Detailed surrogate requirements. MANDATORY SKILLS: pvg. The command returns proof.",
		"--json",
	)
	var issue integrationIssue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("decode created issue %q: %v", out, err)
	}
	if issue.ID == "" || !strings.Contains(issue.Body, "## Acceptance Criteria") {
		t.Fatalf("created issue is not a blank-AC fixture: %+v", issue)
	}
	return issue.ID
}

func deliverEvidence(t *testing.T, run func(...string) string, id string) {
	t.Helper()
	run("nd", "update", id, "--status=in_progress")
	run("nd", "labels", "add", id, "delivered")
	run("nd", "update", id, "--append-notes", `## Implementation Evidence
### CI/Test Results
Commands run:
- integration surrogate command
Summary: all evidence checks except formal AC pass
SHA: abcdef1
### AC Verification
| AC # | Requirement | Status |
|---|---|---|
| 1 | Description-derived requirement | PASS |

## nd_contract
status: delivered

### evidence
- integration delivery evidence

### proof
- [x] AC #1: Description-derived requirement`)
}

func showIssue(t *testing.T, run func(...string) string, id string) integrationIssue {
	t.Helper()
	var issue integrationIssue
	if err := json.Unmarshal([]byte(run("issues", "show", id, "--json")), &issue); err != nil {
		t.Fatalf("decode issue %s: %v", id, err)
	}
	return issue
}

func assertIssueShape(t *testing.T, run func(...string) string, id, status string, labels []string) {
	t.Helper()
	issue := showIssue(t, run, id)
	if issue.Status != status {
		t.Fatalf("issue %s status = %q, want %q", id, issue.Status, status)
	}
	for _, label := range labels {
		found := false
		for _, got := range issue.Labels {
			if got == label {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("issue %s labels = %v, want %q", id, issue.Labels, label)
		}
	}
}
