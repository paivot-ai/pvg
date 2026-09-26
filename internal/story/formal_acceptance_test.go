package story

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/lint"
)

const blankFormalACIssue = `---
title: Test
status: in_progress
labels: [delivered]
---

## Description
Detailed surrogate requirements and MANDATORY SKILLS: pvg; the command returns proof.

## Acceptance Criteria


## Implementation Evidence
### CI/Test Results
Commands run:
- surrogate command
Summary: shipped
SHA: abcdef1
### AC Verification
| AC # | Requirement | Status |
|---|---|---|
| 1 | Description-derived requirement | PASS |

## nd_contract
status: delivered

### evidence
- tests passed

### proof
- [x] AC #1: Description-derived requirement
`

func TestVerifyDeliveryRejectsBlankFormalAC(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", blankFormalACIssue)

	report, err := VerifyDelivery(repo, "PROJ-a1b2")
	if err != nil {
		t.Fatalf("VerifyDelivery() error: %v", err)
	}
	if report.Failed == 0 {
		t.Fatalf("VerifyDelivery() unexpectedly passed:\n%s", report.FormatText())
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "formal:acceptance_criteria" {
			found = true
			if check.Passed {
				t.Fatalf("formal AC check unexpectedly passed: %+v", check)
			}
			if !strings.Contains(check.Message, "PROJ-a1b2") ||
				!strings.Contains(check.Message, lint.FormalAcceptanceSection) ||
				!strings.Contains(check.Message, "repair") {
				t.Fatalf("formal AC diagnostic is incomplete: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("formal AC check missing:\n%s", report.FormatText())
	}
}

func TestTransitionDeliverRejectsBlankFormalACBeforeMutation(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", blankFormalACIssue)

	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = helperExecCommand(t, logPath)

	_, err := Transition(repo, "deliver", "PROJ-a1b2", TransitionOptions{})
	assertFormalACError(t, err)
	joined := flattenCalls(readCalls(t, logPath))
	for _, mutation := range []string{"--status=in_progress", "labels add", "--append-notes"} {
		if strings.Contains(joined, mutation) {
			t.Fatalf("blank formal AC must fail before mutation; saw %q:\n%s", mutation, joined)
		}
	}
}

func TestTransitionAcceptRejectsBlankFormalACBeforeClose(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	writeIssue(t, vault, "PROJ-a1b2", blankFormalACIssue)

	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = helperExecCommand(t, logPath)

	_, err := Transition(repo, "accept", "PROJ-a1b2", TransitionOptions{})
	assertFormalACError(t, err)
	joined := flattenCalls(readCalls(t, logPath))
	for _, mutation := range []string{"close PROJ-a1b2", "labels add PROJ-a1b2 accepted", "--append-notes"} {
		if strings.Contains(joined, mutation) {
			t.Fatalf("blank formal AC must fail before close; saw %q:\n%s", mutation, joined)
		}
	}
}

func TestVerifyDeliveryReportsAcceptedLegacyRecordWithoutRewrite(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(t.TempDir(), "nd-vault")
	setupIssueEnv(t, vault)
	body := strings.Replace(blankFormalACIssue, "labels: [delivered]", "labels: [accepted]", 1)
	body = strings.Replace(body, "status: in_progress", "status: closed", 1)
	writeIssue(t, vault, "PROJ-a1b2", body)

	report, err := VerifyDelivery(repo, "PROJ-a1b2")
	if err != nil {
		t.Fatalf("VerifyDelivery() error: %v", err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "formal:acceptance_criteria" {
			found = true
			if check.Passed || !strings.Contains(check.Message, "legacy accepted record") {
				t.Fatalf("legacy diagnostic = %+v, want fail-closed legacy note", check)
			}
		}
	}
	if !found {
		t.Fatalf("formal AC check missing:\n%s", report.FormatText())
	}

	got, readErr := os.ReadFile(filepath.Join(vault, "issues", "PROJ-a1b2.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != body {
		t.Fatalf("VerifyDelivery rewrote historical record:\n got %q\nwant %q", string(got), body)
	}
}

func assertFormalACError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected formal acceptance criteria error")
	}
	var typed *lint.FormalAcceptanceError
	if !errors.As(err, &typed) {
		t.Fatalf("Transition() error = %T (%v), want *lint.FormalAcceptanceError", err, err)
	}
	if typed.StoryID != "PROJ-a1b2" || typed.Section != lint.FormalAcceptanceSection || typed.Code != lint.FormalACBlank || typed.Repair == "" {
		t.Fatalf("typed diagnostic = %+v", typed)
	}
}
