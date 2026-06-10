package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- Config parsing ---

func TestParseWorkflowConfig_Disabled(t *testing.T) {
	wc := ParseWorkflowConfig(map[string]string{})
	if wc.Enabled {
		t.Error("expected disabled when key missing")
	}
	wc = ParseWorkflowConfig(map[string]string{"workflow.fsm": "false"})
	if wc.Enabled {
		t.Error("expected disabled for 'false'")
	}
}

func TestParseWorkflowConfig_Enabled(t *testing.T) {
	wc := ParseWorkflowConfig(map[string]string{
		"workflow.fsm":      "true",
		"workflow.sequence": "open,in_progress,closed",
	})
	if !wc.Enabled {
		t.Error("expected enabled")
	}
	if len(wc.Sequence) != 3 {
		t.Errorf("expected 3 statuses, got %d", len(wc.Sequence))
	}
}

func TestParseWorkflowConfig_ExitRules(t *testing.T) {
	wc := ParseWorkflowConfig(map[string]string{
		"workflow.fsm":        "true",
		"workflow.exit_rules": "blocked:open,in_progress;rejected:in_progress",
	})
	if len(wc.ExitRules) != 2 {
		t.Fatalf("expected 2 exit rules, got %d", len(wc.ExitRules))
	}
	if targets := wc.ExitRules["blocked"]; len(targets) != 2 {
		t.Errorf("expected 2 targets for blocked, got %d", len(targets))
	}
	if targets := wc.ExitRules["rejected"]; len(targets) != 1 || targets[0] != "in_progress" {
		t.Errorf("unexpected targets for rejected: %v", targets)
	}
}

func TestParseWorkflowConfig_DefaultSequenceFallback(t *testing.T) {
	wc := ParseWorkflowConfig(map[string]string{
		"workflow.fsm":      "true",
		"workflow.sequence": "",
	})
	if len(wc.Sequence) != 3 {
		t.Errorf("expected default sequence length 3, got %d", len(wc.Sequence))
	}
	if wc.Sequence[0] != "open" || wc.Sequence[2] != "closed" {
		t.Errorf("expected default open..closed sequence, got %v", wc.Sequence)
	}
}

func TestParseWorkflowConfig_MalformedExitRules(t *testing.T) {
	wc := ParseWorkflowConfig(map[string]string{
		"workflow.fsm":        "true",
		"workflow.exit_rules": "malformed;also-bad;:;good:open",
	})
	// Only "good:open" should parse
	if len(wc.ExitRules) != 1 {
		t.Errorf("expected 1 exit rule, got %d: %v", len(wc.ExitRules), wc.ExitRules)
	}
}

// --- Transition validation ---

func defaultWC() WorkflowConfig {
	return ParseWorkflowConfig(map[string]string{
		"workflow.fsm":        "true",
		"workflow.sequence":   "open,in_progress,closed",
		"workflow.exit_rules": "blocked:open,in_progress;deferred:open,in_progress",
	})
}

func TestValidateTransition_ForwardOneStep(t *testing.T) {
	wc := defaultWC()
	tests := []struct {
		from, to string
	}{
		{"open", "in_progress"},
		{"in_progress", "closed"},
	}
	for _, tt := range tests {
		r := ValidateTransition(wc, "TEST-a1b2", tt.from, tt.to)
		if !r.Allowed {
			t.Errorf("%s -> %s: expected allowed, got blocked: %s", tt.from, tt.to, r.Reason)
		}
	}
}

func TestValidateTransition_ForwardSkipBlocked(t *testing.T) {
	wc := defaultWC()
	tests := []struct {
		from, to string
	}{
		{"open", "closed"},
	}
	for _, tt := range tests {
		r := ValidateTransition(wc, "TEST-a1b2", tt.from, tt.to)
		if r.Allowed {
			t.Errorf("%s -> %s: expected blocked, got allowed", tt.from, tt.to)
		}
	}
}

func TestValidateTransition_BackwardAny(t *testing.T) {
	wc := defaultWC()
	tests := []struct {
		from, to string
	}{
		{"closed", "open"},
		{"in_progress", "open"},
	}
	for _, tt := range tests {
		r := ValidateTransition(wc, "TEST-a1b2", tt.from, tt.to)
		if !r.Allowed {
			t.Errorf("%s -> %s: expected allowed (backward), got blocked: %s", tt.from, tt.to, r.Reason)
		}
	}
}

func TestValidateTransition_SameStatusNoOp(t *testing.T) {
	wc := defaultWC()
	r := ValidateTransition(wc, "TEST-a1b2", "open", "open")
	if !r.Allowed {
		t.Errorf("same status should be no-op, got blocked: %s", r.Reason)
	}
}

func TestValidateTransition_ExitRules(t *testing.T) {
	wc := defaultWC()

	// blocked -> open: allowed by exit rule
	r := ValidateTransition(wc, "TEST-a1b2", "blocked", "open")
	if !r.Allowed {
		t.Errorf("blocked -> open: expected allowed by exit rule, got blocked: %s", r.Reason)
	}
	// blocked -> in_progress: allowed by exit rule
	r = ValidateTransition(wc, "TEST-a1b2", "blocked", "in_progress")
	if !r.Allowed {
		t.Errorf("blocked -> in_progress: expected allowed by exit rule, got blocked: %s", r.Reason)
	}
	// blocked -> closed: NOT in exit rule
	r = ValidateTransition(wc, "TEST-a1b2", "blocked", "closed")
	if r.Allowed {
		t.Error("blocked -> closed: expected blocked by exit rule, got allowed")
	}
	// deferred -> in_progress: allowed by exit rule
	r = ValidateTransition(wc, "TEST-a1b2", "deferred", "in_progress")
	if !r.Allowed {
		t.Errorf("deferred -> in_progress: expected allowed by exit rule, got blocked: %s", r.Reason)
	}
	// deferred -> closed: NOT in exit rule
	r = ValidateTransition(wc, "TEST-a1b2", "deferred", "closed")
	if r.Allowed {
		t.Error("deferred -> closed: expected blocked by exit rule, got allowed")
	}
}

func TestValidateTransition_OffSequence(t *testing.T) {
	wc := defaultWC()
	// "custom_status" is not in the sequence -- should be unrestricted
	r := ValidateTransition(wc, "TEST-a1b2", "open", "custom_status")
	if !r.Allowed {
		t.Errorf("off-sequence target should be allowed, got blocked: %s", r.Reason)
	}
	r = ValidateTransition(wc, "TEST-a1b2", "custom_status", "closed")
	if !r.Allowed {
		t.Errorf("off-sequence source should be allowed, got blocked: %s", r.Reason)
	}
}

// --- nd command parsing ---

func TestParseNdStatusChange_UpdateEquals(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd update PROJ-a3f8 --status=in_progress")
	if !found || status != "in_progress" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_UpdateSpace(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd update PROJ-a3f8 --status delivered")
	if !found || status != "delivered" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_Close(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd close PROJ-a3f8")
	if !found || status != "closed" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_CloseMultiple(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd close PROJ-a1b2 PROJ-c3d4 PROJ-e5f6")
	if !found || status != "closed" || len(ids) != 3 {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_NonStatusUpdate(t *testing.T) {
	_, _, found := parseNdStatusChange("nd update PROJ-a3f8 --title=new-title")
	if found {
		t.Error("non-status update should not be detected")
	}
}

func TestParseNdStatusChange_NonNdCommand(t *testing.T) {
	_, _, found := parseNdStatusChange("git status")
	if found {
		t.Error("non-nd command should not be detected")
	}
}

func TestParseNdStatusChange_FullPath(t *testing.T) {
	ids, status, found := parseNdStatusChange("/usr/local/bin/nd update PROJ-a3f8 --status=closed")
	if !found || status != "closed" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_WithVaultFlag(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd --vault .nd update PROJ-a3f8 --status=delivered")
	if !found || status != "delivered" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_WithBooleanGlobalFlag(t *testing.T) {
	ids, status, found := parseNdStatusChange("nd --json update PROJ-a3f8 --status=closed")
	if !found || status != "closed" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_ChainedCommand(t *testing.T) {
	ids, status, found := parseNdStatusChange("echo hello && nd update PROJ-a3f8 --status=in_progress")
	if !found || status != "in_progress" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

func TestParseNdStatusChange_SemicolonChain(t *testing.T) {
	ids, status, found := parseNdStatusChange("echo hello; nd close PROJ-a3f8")
	if !found || status != "closed" || len(ids) != 1 || ids[0] != "PROJ-a3f8" {
		t.Errorf("unexpected: ids=%v status=%q found=%v", ids, status, found)
	}
}

// --- pvg-wrapped and pvg issues forms ---

func TestParseNdStatusChange_PvgWrappedForms(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantIDs    []string
		wantStatus string
		wantFound  bool
	}{
		{"pvg nd update equals", "pvg nd update PROJ-a3f8 --status=in_progress", []string{"PROJ-a3f8"}, "in_progress", true},
		{"pvg nd update space", "pvg nd update PROJ-a3f8 --status closed", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg nd close", "pvg nd close PROJ-a3f8", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg nd close multiple", "pvg nd close PROJ-a1b2 PROJ-c3d4", []string{"PROJ-a1b2", "PROJ-c3d4"}, "closed", true},
		{"pvg path prefix", "/usr/local/bin/pvg nd close PROJ-a3f8", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg nd chained", "echo hi && pvg nd update PROJ-a3f8 --status=open", []string{"PROJ-a3f8"}, "open", true},
		{"pvg issues update equals", "pvg issues update PROJ-a3f8 --status=closed", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg issues update space", "pvg issues update PROJ-a3f8 --status open", []string{"PROJ-a3f8"}, "open", true},
		{"pvg issues close", "pvg issues close PROJ-a3f8", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg issues close with reason", `pvg issues close PROJ-a3f8 --reason "done"`, []string{"PROJ-a3f8"}, "closed", true},
		{"pvg issues reopen", "pvg issues reopen PROJ-a3f8", []string{"PROJ-a3f8"}, "open", true},
		{"pvg issues chained", "git pull; pvg issues close PROJ-a3f8", []string{"PROJ-a3f8"}, "closed", true},
		{"pvg issues list not a mutation", "pvg issues list --status open", nil, "", false},
		{"pvg issues show not a mutation", "pvg issues show PROJ-a3f8", nil, "", false},
		{"mid-token pvg not matched", "echo pvg issues close PROJ-a3f8 is a command", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, status, found := parseNdStatusChange(tt.command)
			if found != tt.wantFound {
				t.Fatalf("found=%v want %v (ids=%v status=%q)", found, tt.wantFound, ids, status)
			}
			if !tt.wantFound {
				return
			}
			if status != tt.wantStatus {
				t.Errorf("status=%q want %q", status, tt.wantStatus)
			}
			if len(ids) != len(tt.wantIDs) {
				t.Fatalf("ids=%v want %v", ids, tt.wantIDs)
			}
			for i := range ids {
				if ids[i] != tt.wantIDs[i] {
					t.Errorf("ids[%d]=%q want %q", i, ids[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestParseNdContractLabelAdd_PvgForms(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantID     string
		wantLabels []string
		wantFound  bool
	}{
		{"pvg nd update add-label", "pvg nd update PROJ-a3f8 --add-label=delivered", "PROJ-a3f8", []string{"delivered"}, true},
		{"pvg nd labels add", "pvg nd labels add PROJ-a3f8 delivered", "PROJ-a3f8", []string{"delivered"}, true},
		{"pvg issues update add-label equals", "pvg issues update PROJ-a3f8 --add-label=accepted", "PROJ-a3f8", []string{"accepted"}, true},
		{"pvg issues update add-label space", "pvg issues update PROJ-a3f8 --add-label rejected", "PROJ-a3f8", []string{"rejected"}, true},
		{"pvg issues update no label", "pvg issues update PROJ-a3f8 --title=x", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, labels, found := parseNdContractLabelAdd(tt.command)
			if found != tt.wantFound {
				t.Fatalf("found=%v want %v (id=%q labels=%v)", found, tt.wantFound, id, labels)
			}
			if !tt.wantFound {
				return
			}
			if id != tt.wantID {
				t.Errorf("id=%q want %q", id, tt.wantID)
			}
			if len(labels) != len(tt.wantLabels) {
				t.Fatalf("labels=%v want %v", labels, tt.wantLabels)
			}
			for i := range labels {
				if labels[i] != tt.wantLabels[i] {
					t.Errorf("labels[%d]=%q want %q", i, labels[i], tt.wantLabels[i])
				}
			}
		})
	}
}

func TestParseNdContractLabelAdd_Update(t *testing.T) {
	id, labels, found := parseNdContractLabelAdd("nd update PROJ-a3f8 --add-label=delivered")
	if !found || id != "PROJ-a3f8" || len(labels) != 1 || labels[0] != "delivered" {
		t.Errorf("unexpected: id=%q labels=%v found=%v", id, labels, found)
	}
}

func TestParseNdContractLabelAdd_LabelsAdd(t *testing.T) {
	id, labels, found := parseNdContractLabelAdd("nd labels add PROJ-a3f8 delivered rejected")
	if !found || id != "PROJ-a3f8" || len(labels) != 2 || labels[0] != "delivered" || labels[1] != "rejected" {
		t.Errorf("unexpected: id=%q labels=%v found=%v", id, labels, found)
	}
}

func TestParseNdContractLabelAdd_WithBooleanGlobalFlag(t *testing.T) {
	id, labels, found := parseNdContractLabelAdd("nd --json labels add PROJ-a3f8 accepted")
	if !found || id != "PROJ-a3f8" || len(labels) != 1 || labels[0] != "accepted" {
		t.Errorf("unexpected: id=%q labels=%v found=%v", id, labels, found)
	}
}

func TestParseNdDeferCommand(t *testing.T) {
	id, found := parseNdDeferCommand("nd defer PROJ-a3f8 --until 2026-03-20")
	if !found || id != "PROJ-a3f8" {
		t.Errorf("unexpected: id=%q found=%v", id, found)
	}
}

func TestParseNdDeferCommand_WithBooleanGlobalFlag(t *testing.T) {
	id, found := parseNdDeferCommand("nd --json defer PROJ-a3f8 --until 2026-03-20")
	if !found || id != "PROJ-a3f8" {
		t.Errorf("unexpected: id=%q found=%v", id, found)
	}
}

func TestParseNdUndeferCommand(t *testing.T) {
	id, found := parseNdUndeferCommand("nd undefer PROJ-a3f8")
	if !found || id != "PROJ-a3f8" {
		t.Errorf("unexpected: id=%q found=%v", id, found)
	}
}

func TestParseNdUndeferCommand_WithBooleanGlobalFlag(t *testing.T) {
	id, found := parseNdUndeferCommand("nd --json undefer PROJ-a3f8")
	if !found || id != "PROJ-a3f8" {
		t.Errorf("unexpected: id=%q found=%v", id, found)
	}
}

// --- Issue status reading ---

func TestReadIssueStatus_ValidFile(t *testing.T) {
	dir := t.TempDir()
	issuesDir := filepath.Join(dir, ".vault", "issues")
	if err := os.MkdirAll(issuesDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: Test\nstatus: in_progress\n---\nBody text"
	if err := os.WriteFile(filepath.Join(issuesDir, "PROJ-a1b2.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	status := ReadIssueStatus(dir, "PROJ-a1b2")
	if status != "in_progress" {
		t.Errorf("expected 'in_progress', got %q", status)
	}
}

func TestReadIssueStatus_MissingFile(t *testing.T) {
	dir := t.TempDir()
	status := ReadIssueStatus(dir, "PROJ-noexist")
	if status != "" {
		t.Errorf("expected empty for missing file, got %q", status)
	}
}

func TestReadIssueStatus_MissingDirectory(t *testing.T) {
	status := ReadIssueStatus("/nonexistent/project", "PROJ-a1b2")
	if status != "" {
		t.Errorf("expected empty for missing dir, got %q", status)
	}
}

func TestReadIssueStatus_MalformedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	issuesDir := filepath.Join(dir, ".vault", "issues")
	if err := os.MkdirAll(issuesDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "no frontmatter here\njust text"
	if err := os.WriteFile(filepath.Join(issuesDir, "PROJ-bad.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	status := ReadIssueStatus(dir, "PROJ-bad")
	if status != "" {
		t.Errorf("expected empty for malformed frontmatter, got %q", status)
	}
}

func TestReadIssueStatus_NoStatusField(t *testing.T) {
	dir := t.TempDir()
	issuesDir := filepath.Join(dir, ".vault", "issues")
	if err := os.MkdirAll(issuesDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: Test\npriority: high\n---\nBody"
	if err := os.WriteFile(filepath.Join(issuesDir, "PROJ-nostatus.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	status := ReadIssueStatus(dir, "PROJ-nostatus")
	if status != "" {
		t.Errorf("expected empty for missing status field, got %q", status)
	}
}

// --- Integration: CheckFSM end-to-end ---

func setupFSMProject(t *testing.T, fsmEnabled bool, issueID, issueStatus string) string {
	t.Helper()
	dir := t.TempDir()
	sharedVault := filepath.Join(dir, ".git", "paivot", "nd-vault")

	// Create settings
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	// Explicit shared vault config
	sharedCfg := "# nd shared-worktree state\nmode: git_common_dir\npath: paivot/nd-vault\n"
	if err := os.WriteFile(filepath.Join(dir, ".vault", ".nd-shared.yaml"), []byte(sharedCfg), 0644); err != nil {
		t.Fatal(err)
	}
	enabled := "false"
	if fsmEnabled {
		enabled = "true"
	}
	settingsContent := fmt.Sprintf(
		"workflow.fsm: %s\nworkflow.sequence: open,in_progress,closed\nworkflow.exit_rules: blocked:open,in_progress;deferred:open,in_progress\n",
		enabled)
	if err := os.WriteFile(filepath.Join(settingsDir, ".settings.yaml"), []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create issue if provided
	if issueID != "" && issueStatus != "" {
		if err := os.MkdirAll(sharedVault, 0755); err != nil {
			t.Fatal(err)
		}
		issuesDir := filepath.Join(sharedVault, "issues")
		if err := os.MkdirAll(issuesDir, 0755); err != nil {
			t.Fatal(err)
		}
		issueContent := fmt.Sprintf("---\ntitle: Test Issue\nstatus: %s\n---\nBody", issueStatus)
		if err := os.WriteFile(filepath.Join(issuesDir, issueID+".md"), []byte(issueContent), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestCheckFSM_Disabled(t *testing.T) {
	dir := setupFSMProject(t, false, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "nd update PROJ-a1b2 --status=closed")
	if !r.Allowed {
		t.Errorf("expected allowed when FSM disabled, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_AllowedTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "nd update PROJ-a1b2 --status=in_progress")
	if !r.Allowed {
		t.Errorf("expected allowed for open -> in_progress, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_BlockedTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "nd update PROJ-a1b2 --status=closed")
	if r.Allowed {
		t.Error("expected blocked for open -> closed, got allowed")
	}
	if r.Reason == "" {
		t.Error("expected error message with reason")
	}
}

func TestCheckFSM_FailOpenMissingFile(t *testing.T) {
	dir := setupFSMProject(t, true, "", "") // no issue file
	r := CheckFSM(dir, "nd update PROJ-noexist --status=closed")
	if !r.Allowed {
		t.Errorf("expected fail-open for missing issue file, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_FailOpenMissingSettings(t *testing.T) {
	dir := t.TempDir() // no settings at all
	r := CheckFSM(dir, "nd update PROJ-a1b2 --status=closed")
	if !r.Allowed {
		t.Errorf("expected fail-open for missing settings, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_ValidatesDeferTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "blocked")
	r := CheckFSM(dir, "nd defer PROJ-a1b2")
	if r.Allowed {
		t.Fatal("expected blocked -> deferred to be blocked by exit rules")
	}
}

func TestCheckFSM_UsesDeferredResumeTarget(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "paivot", "nd-vault", "issues"), 0755); err != nil {
		t.Fatal(err)
	}
	sharedCfg := "# nd shared-worktree state\nmode: git_common_dir\npath: paivot/nd-vault\n"
	if err := os.WriteFile(filepath.Join(dir, ".vault", ".nd-shared.yaml"), []byte(sharedCfg), 0644); err != nil {
		t.Fatal(err)
	}
	settingsContent := "workflow.fsm: true\nworkflow.sequence: open,in_progress,closed\nworkflow.exit_rules: deferred:in_progress\n"
	if err := os.WriteFile(filepath.Join(settingsDir, ".settings.yaml"), []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}
	issueContent := "---\ntitle: Test Issue\nstatus: deferred\n---\nBody"
	if err := os.WriteFile(filepath.Join(dir, ".git", "paivot", "nd-vault", "issues", "PROJ-a1b2.md"), []byte(issueContent), 0644); err != nil {
		t.Fatal(err)
	}

	r := CheckFSM(dir, "nd undefer PROJ-a1b2")
	if !r.Allowed {
		t.Fatalf("expected undefer to resume to in_progress per exit rules, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_NonNdCommand(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "git status")
	if !r.Allowed {
		t.Errorf("expected allowed for non-nd command, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_CloseBlocked(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "nd close PROJ-a1b2")
	if r.Allowed {
		t.Error("expected blocked for open -> closed via nd close, got allowed")
	}
}

func TestCheckFSM_CloseAllowedFromInProgress(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckFSM(dir, "nd close PROJ-a1b2")
	if !r.Allowed {
		t.Errorf("expected allowed for in_progress -> closed, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_CloseAllowedFromInProgressWithBooleanGlobalFlag(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckFSM(dir, "nd --json close PROJ-a1b2")
	if !r.Allowed {
		t.Errorf("expected allowed for in_progress -> closed with --json, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_EmptyProjectRoot(t *testing.T) {
	r := CheckFSM("", "nd update PROJ-a1b2 --status=closed")
	if !r.Allowed {
		t.Errorf("expected allowed for empty project root, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_BlocksPvgNdWrappedTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "pvg nd update PROJ-a1b2 --status=closed")
	if r.Allowed {
		t.Error("expected blocked for open -> closed via pvg nd update, got allowed")
	}
}

func TestCheckFSM_BlocksPvgIssuesUpdateTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "pvg issues update PROJ-a1b2 --status=closed")
	if r.Allowed {
		t.Error("expected blocked for open -> closed via pvg issues update, got allowed")
	}
}

func TestCheckFSM_BlocksPvgIssuesCloseTransition(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckFSM(dir, "pvg issues close PROJ-a1b2")
	if r.Allowed {
		t.Error("expected blocked for open -> closed via pvg issues close, got allowed")
	}
}

func TestCheckFSM_AllowsPvgIssuesCloseFromInProgress(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckFSM(dir, "pvg issues close PROJ-a1b2")
	if !r.Allowed {
		t.Errorf("expected allowed for in_progress -> closed via pvg issues close, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_AllowsPvgIssuesReopen(t *testing.T) {
	// reopen -> open is a backward transition, always allowed.
	dir := setupFSMProject(t, true, "PROJ-a1b2", "closed")
	r := CheckFSM(dir, "pvg issues reopen PROJ-a1b2")
	if !r.Allowed {
		t.Errorf("expected allowed for closed -> open via pvg issues reopen, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_BackwardAllowed(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckFSM(dir, "nd update PROJ-a1b2 --status=open")
	if !r.Allowed {
		t.Errorf("expected allowed for backward transition, got blocked: %s", r.Reason)
	}
}

// --- Label contracts (CheckLabelContract): active whenever the repo is
// Paivot-managed, independent of workflow.fsm ---

func TestCheckLabelContract_BlocksDeliveredLabelWhenNotInProgress(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "open")
	r := CheckLabelContract(dir, "nd labels add PROJ-a1b2 delivered")
	if r.Allowed {
		t.Error("expected delivered label blocked while issue is open")
	}
}

func TestCheckLabelContract_AllowsDeliveredLabelFromInProgress(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckLabelContract(dir, "nd update PROJ-a1b2 --add-label delivered")
	if !r.Allowed {
		t.Errorf("expected delivered label allowed from in_progress, got blocked: %s", r.Reason)
	}
}

func TestCheckLabelContract_BlocksAcceptedLabelBeforeClose(t *testing.T) {
	dir := setupFSMProject(t, true, "PROJ-a1b2", "in_progress")
	r := CheckLabelContract(dir, "nd labels add PROJ-a1b2 accepted")
	if r.Allowed {
		t.Error("expected accepted label blocked while issue is still in_progress")
	}
}

func TestCheckLabelContract_EnforcedEvenWhenFSMDisabled(t *testing.T) {
	// workflow.fsm gates only the status-transition checks; the contract
	// labels are enforced for any Paivot-managed repo (settings file present).
	dir := setupFSMProject(t, false, "PROJ-a1b2", "open")
	r := CheckLabelContract(dir, "nd labels add PROJ-a1b2 delivered")
	if r.Allowed {
		t.Error("expected delivered label blocked while open even with FSM disabled")
	}
}

func TestCheckLabelContract_AllowsWhenNotPaivotManaged(t *testing.T) {
	dir := t.TempDir() // no .vault/knowledge/.settings.yaml anywhere
	r := CheckLabelContract(dir, "nd labels add PROJ-a1b2 delivered")
	if !r.Allowed {
		t.Errorf("expected allowed in non-Paivot repo, got blocked: %s", r.Reason)
	}
}

func TestCheckLabelContract_BlocksPvgIssuesAddLabelForm(t *testing.T) {
	dir := setupFSMProject(t, false, "PROJ-a1b2", "open")
	r := CheckLabelContract(dir, "pvg issues update PROJ-a1b2 --add-label delivered")
	if r.Allowed {
		t.Error("expected pvg issues --add-label delivered blocked while issue is open")
	}
}

func TestCheckLabelContract_BlocksPvgNdLabelsAddForm(t *testing.T) {
	dir := setupFSMProject(t, false, "PROJ-a1b2", "open")
	r := CheckLabelContract(dir, "pvg nd labels add PROJ-a1b2 delivered")
	if r.Allowed {
		t.Error("expected pvg nd labels add delivered blocked while issue is open")
	}
}

func TestCheckLabelContract_CombinedUpdateValidatesAgainstNewStatus(t *testing.T) {
	// PM reject command: one command moves the story to open AND adds the
	// rejected label. The label must be validated against the NEW status
	// (open), not the pre-update status (in_progress).
	dir := setupFSMProject(t, false, "PROJ-a1b2", "in_progress")
	r := CheckLabelContract(dir, "pvg issues update PROJ-a1b2 --status=open --remove-label delivered --add-label rejected")
	if !r.Allowed {
		t.Errorf("expected combined status+label update allowed (rejected validated against new status open), got blocked: %s", r.Reason)
	}
}

func TestCheckLabelContract_CombinedUpdateBlocksWrongNewStatus(t *testing.T) {
	// delivered requires in_progress; the same command moves it to closed.
	dir := setupFSMProject(t, false, "PROJ-a1b2", "in_progress")
	r := CheckLabelContract(dir, "nd update PROJ-a1b2 --status=closed --add-label delivered")
	if r.Allowed {
		t.Error("expected delivered label blocked when the same command sets status closed")
	}
}

func TestGuardCheck_WiresLabelContractForBash(t *testing.T) {
	// End-to-end: guard.Check must route Bash commands through
	// CheckLabelContract even when workflow.fsm is disabled.
	dir := setupFSMProject(t, false, "PROJ-a1b2", "open")
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: "pvg issues update PROJ-a1b2 --add-label delivered"},
	}
	r := Check("", dir, input)
	if r.Allowed {
		t.Error("expected guard.Check to block delivered label on open issue via label contract")
	}
}

func TestCheckLabelContract_CombinedUpdateAllowsAcceptedWithClose(t *testing.T) {
	// accepted requires closed; the same command closes the story.
	dir := setupFSMProject(t, false, "PROJ-a1b2", "in_progress")
	r := CheckLabelContract(dir, "pvg issues update PROJ-a1b2 --status=closed --add-label accepted")
	if !r.Allowed {
		t.Errorf("expected accepted+close combined update allowed, got blocked: %s", r.Reason)
	}
}

func TestCheckFSM_UsesSharedVaultOverLocalBranchState(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, ".settings.yaml"), []byte("workflow.fsm: true\nworkflow.sequence: open,in_progress,closed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	localIssuesDir := filepath.Join(dir, ".vault", "issues")
	if err := os.MkdirAll(localIssuesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localIssuesDir, "PROJ-a1b2.md"), []byte("---\nstatus: open\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	override := filepath.Join(t.TempDir(), "shared-vault")
	if err := os.MkdirAll(filepath.Join(override, "issues"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "issues", "PROJ-a1b2.md"), []byte("---\nstatus: in_progress\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	r := CheckFSM(dir, "nd close PROJ-a1b2")
	if !r.Allowed {
		t.Fatalf("expected shared vault in_progress status to allow close, got blocked: %s", r.Reason)
	}
}
