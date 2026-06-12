package story

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type loggedCall struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

func TestTransitionDeliverUsesSharedNDFlow(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ND_VAULT_DIR", vault); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = helperExecCommand(t, logPath)

	msg, err := Transition(repo, "deliver", "PROJ-a1b2", TransitionOptions{})
	if err != nil {
		t.Fatalf("Transition() error: %v", err)
	}
	if !strings.Contains(msg, vault) {
		t.Fatalf("Transition() message = %q, want vault path", msg)
	}

	calls := readCalls(t, logPath)
	wantSubstrings := []string{
		"show PROJ-a1b2",
		"update PROJ-a1b2 --status=in_progress",
		"labels rm PROJ-a1b2 rejected",
		"labels rm PROJ-a1b2 accepted",
		"labels add PROJ-a1b2 delivered",
		"update PROJ-a1b2 --append-notes",
		"doctor --fix",
	}
	joined := flattenCalls(calls)
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected call containing %q, got:\n%s", want, joined)
		}
	}
}

func TestVerifyDeliveryPassesWithAuthoritativeContract(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", `---
title: Test
status: in_progress
labels: [delivered]
---

## Implementation Evidence
### CI/Test Results
Commands run:
Summary: shipped
SHA: abcdef1
### AC Verification

## nd_contract
status: delivered

### evidence
- tests passed

### proof
- [x] AC #1: done
`)

	report, err := VerifyDelivery(repo, "PROJ-a1b2")
	if err != nil {
		t.Fatalf("VerifyDelivery() error: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("VerifyDelivery() failed checks = %d\n%s", report.Failed, report.FormatText())
	}
}

func TestVerifyDeliveryFailsWhenContractNotAtEOF(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", `---
title: Test
status: in_progress
labels: [delivered]
---

## Implementation Evidence
### Test Results
Commands run:
Summary: shipped
SHA: abcdef1

## nd_contract
status: delivered

### evidence
- tests passed

### proof
- [x] AC #1: done

Trailing note
`)

	report, err := VerifyDelivery(repo, "PROJ-a1b2")
	if err != nil {
		t.Fatalf("VerifyDelivery() error: %v", err)
	}
	if report.Failed == 0 {
		t.Fatalf("expected failure when contract is not at EOF")
	}
	if !strings.Contains(report.FormatText(), "nd_contract:eof") {
		t.Fatalf("expected nd_contract:eof failure, got:\n%s", report.FormatText())
	}
}

func TestValidateAuthoritativeContractEOF(t *testing.T) {
	const contractBlock = `## nd_contract
status: delivered

### evidence
- tests passed

### proof
- [x] AC #1: done
`
	const ndHistory = `## History
- 2026-06-12: delivered
`
	const ndLinks = `## Links
- parent: PROJ-epic
`
	const ndComments = `## Comments
- looks good
`

	cases := []struct {
		name      string
		trailing  string
		wantEOF   bool
		wantState string
	}{
		{
			name:      "literal last block",
			trailing:  "",
			wantEOF:   true,
			wantState: "delivered",
		},
		{
			name:      "nd managed sections follow",
			trailing:  "\n" + ndHistory + "\n" + ndLinks + "\n" + ndComments,
			wantEOF:   true,
			wantState: "delivered",
		},
		{
			name:      "only comments follow",
			trailing:  "\n" + ndComments,
			wantEOF:   true,
			wantState: "delivered",
		},
		{
			name:      "links then history reordered",
			trailing:  "\n" + ndLinks + "\n" + ndHistory,
			wantEOF:   true,
			wantState: "delivered",
		},
		{
			name:      "rogue section follows",
			trailing:  "\n## Rogue\n- appended after contract\n",
			wantEOF:   false,
			wantState: "delivered",
		},
		{
			name:      "second notes section follows",
			trailing:  "\n## Notes\n- appended after contract\n",
			wantEOF:   false,
			wantState: "delivered",
		},
		{
			name:      "managed sections then rogue section",
			trailing:  "\n" + ndHistory + "\n## Rogue\n- still bad\n",
			wantEOF:   false,
			wantState: "delivered",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "## Implementation Evidence\nSummary: shipped\n\n" + contractBlock + tc.trailing
			status, atEOF := validateAuthoritativeContract(content)
			if status != tc.wantState {
				t.Fatalf("status = %q, want %q", status, tc.wantState)
			}
			if atEOF != tc.wantEOF {
				t.Fatalf("atEOF = %v, want %v\ncontent:\n%s", atEOF, tc.wantEOF, content)
			}
		})
	}
}

func TestVerifyDeliveryPassesWithNDManagedTrailingSections(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", `---
title: Test
status: in_progress
labels: [delivered]
---

## Implementation Evidence
### CI/Test Results
Commands run:
Summary: shipped
SHA: abcdef1
### AC Verification

## nd_contract
status: delivered

### evidence
- tests passed

### proof
- [x] AC #1: done

## History
- 2026-06-12: delivered via pvg story deliver

## Links
- parent: PROJ-epic

## Comments
- ready for review
`)

	report, err := VerifyDelivery(repo, "PROJ-a1b2")
	if err != nil {
		t.Fatalf("VerifyDelivery() error: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("VerifyDelivery() failed checks = %d\n%s", report.Failed, report.FormatText())
	}
}

func TestTransitionAcceptWarnsWhenNextStoryCannotStart(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ND_VAULT_DIR", vault); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = helperExecCommand(t, logPath)

	t.Setenv("STORY_HELPER_FAIL_MATCH", " update NOPE --status=in_progress")
	t.Setenv("STORY_HELPER_FAIL_OUTPUT", "next story cannot start")

	msg, err := Transition(repo, "accept", "PROJ-a1b2", TransitionOptions{NextStory: "NOPE"})
	if err != nil {
		t.Fatalf("Transition() error: %v", err)
	}
	if !strings.Contains(msg, "warning: accepted story closed successfully, but could not start next story NOPE") {
		t.Fatalf("Transition() message = %q, want warning about next story", msg)
	}

	joined := flattenCalls(readCalls(t, logPath))
	wantSubstrings := []string{
		"show PROJ-a1b2",
		"show NOPE",
		"close PROJ-a1b2 --reason=Accepted via pvg story accept",
		"labels add PROJ-a1b2 accepted",
		"update PROJ-a1b2 --append-notes",
		"update NOPE --status=in_progress",
		"doctor --fix",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected call containing %q, got:\n%s", want, joined)
		}
	}
}

func TestTransitionAcceptFailsBeforeMutationWhenNextStoryMissing(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ND_VAULT_DIR", vault); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = helperExecCommand(t, logPath)

	t.Setenv("STORY_HELPER_FAIL_MATCH", " show NOPE")
	t.Setenv("STORY_HELPER_FAIL_OUTPUT", "story not found")

	_, err := Transition(repo, "accept", "PROJ-a1b2", TransitionOptions{NextStory: "NOPE"})
	if err == nil {
		t.Fatal("expected next story validation error")
	}
	if !strings.Contains(err.Error(), "next story not found: NOPE") {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := flattenCalls(readCalls(t, logPath))
	if strings.Contains(joined, "close PROJ-a1b2") {
		t.Fatalf("close should not run when next story validation fails:\n%s", joined)
	}
}

func TestMergeDefaultsToMain(t *testing.T) {
	repo, remote := initGitRepo(t)
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", `---
title: Test
status: closed
parent: PROJ-epic
labels: [delivered, accepted]
---
Body
`)

	runGitCommand(t, repo, "checkout", "-b", "story/PROJ-a1b2")
	if err := os.WriteFile(filepath.Join(repo, "story.txt"), []byte("story work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repo, "add", "story.txt")
	runGitCommand(t, repo, "commit", "-m", "story work")
	runGitCommand(t, repo, "push", "-u", "origin", "story/PROJ-a1b2")

	msg, err := Merge(repo, "PROJ-a1b2", "")
	if err != nil {
		t.Fatalf("Merge() error: %v", err)
	}
	if !strings.Contains(msg, "main") {
		t.Fatalf("Merge() message = %q, want main branch", msg)
	}

	out := runGitOutput(t, repo, "branch", "--list", "story/PROJ-a1b2")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected local story branch deleted, got %q", out)
	}

	remoteRefs := runGitOutput(t, repo, "ls-remote", "--heads", remote, "story/PROJ-a1b2")
	if strings.TrimSpace(remoteRefs) != "" {
		t.Fatalf("expected remote story branch deleted, got %q", remoteRefs)
	}
}

func TestMergeRejectsUnacceptedStory(t *testing.T) {
	repo, _ := initGitRepo(t)
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", `---
title: Test
status: in_progress
parent: PROJ-epic
labels: [delivered]
---
Body
`)

	if _, err := Merge(repo, "PROJ-a1b2", "epic/PROJ-epic"); err == nil {
		t.Fatal("expected merge-ready validation error")
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_STORY_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}

	call := loggedCall{Name: args[0], Args: args[1:]}
	logPath := os.Getenv("STORY_HELPER_LOG")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(call); err != nil {
		panic(err)
	}

	joined := " " + call.Name + " " + strings.Join(call.Args, " ")
	if failMatch := os.Getenv("STORY_HELPER_FAIL_MATCH"); failMatch != "" && strings.Contains(joined, failMatch) {
		if msg := os.Getenv("STORY_HELPER_FAIL_OUTPUT"); msg != "" {
			_, _ = os.Stderr.WriteString(msg)
		}
		os.Exit(1)
	}

	if call.Name == "nd" && strings.Contains(strings.Join(call.Args, " "), " show ") {
		_, _ = os.Stdout.WriteString(`{"id":"PROJ-a1b2"}`)
	}
	os.Exit(0)
}

func helperExecCommand(t *testing.T, logPath string) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_STORY_HELPER_PROCESS=1",
			"STORY_HELPER_LOG="+logPath,
		)
		return cmd
	}
}

func readCalls(t *testing.T, path string) []loggedCall {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var calls []loggedCall
	for _, line := range lines {
		var call loggedCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("unmarshal log line: %v", err)
		}
		calls = append(calls, call)
	}
	return calls
}

func flattenCalls(calls []loggedCall) string {
	var parts []string
	for _, call := range calls {
		parts = append(parts, call.Name+" "+strings.Join(call.Args, " "))
	}
	return strings.Join(parts, "\n")
}

func setupIssueEnv(t *testing.T, vault string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(vault, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ND_VAULT_DIR", vault); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("ND_VAULT_DIR") })
}

func writeIssue(t *testing.T, vault, storyID, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(vault, "issues", storyID+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initGitRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitCommand(t, repo, "init", "-b", "main")
	runGitCommand(t, repo, "config", "user.name", "Test User")
	runGitCommand(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repo, "add", "README.md")
	runGitCommand(t, repo, "commit", "-m", "init")
	runGitCommand(t, repo, "init", "--bare", remote)
	runGitCommand(t, repo, "remote", "add", "origin", remote)
	runGitCommand(t, repo, "push", "-u", "origin", "main")
	return repo, remote
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
