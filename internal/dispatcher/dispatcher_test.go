package dispatcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOn_CreatesStateFile(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := On(dir); err != nil {
		t.Fatalf("On() error: %v", err)
	}

	state, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}
	if !state.Enabled {
		t.Error("expected enabled=true")
	}
	if state.Since == "" {
		t.Error("expected non-empty since timestamp")
	}
	if state.ActiveAgents == nil {
		t.Error("expected non-nil active_agents")
	}
}

func TestOff_RemovesStateFile(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := On(dir); err != nil {
		t.Fatal(err)
	}

	if err := Off(dir); err != nil {
		t.Fatalf("Off() error: %v", err)
	}

	_, err := ReadState(dir)
	if err == nil {
		t.Error("expected error reading state after Off()")
	}
}

func TestOff_NoopWhenNoStateFile(t *testing.T) {
	dir := t.TempDir()
	if err := Off(dir); err != nil {
		t.Fatalf("Off() with no state file should not error: %v", err)
	}
}

func TestReadState_ErrorOnMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadState(dir)
	if err == nil {
		t.Error("expected error for missing state file")
	}
}

func TestTrackAgent_AddsToActiveAgents(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := On(dir); err != nil {
		t.Fatal(err)
	}

	if err := TrackAgent(dir, "agent-123", "paivot-graph:designer"); err != nil {
		t.Fatalf("TrackAgent() error: %v", err)
	}

	state, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveAgents["agent-123"] != "paivot-graph:designer" {
		t.Errorf("expected agent-123=paivot-graph:designer, got %v", state.ActiveAgents)
	}
}

func TestTrackAgent_FromWorktreeUpdatesRootState(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "agent-123")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	if err := On(root); err != nil {
		t.Fatal(err)
	}

	if err := TrackAgent(worktree, "agent-123", "paivot-graph:designer"); err != nil {
		t.Fatalf("TrackAgent() from worktree error: %v", err)
	}

	state, err := ReadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.AgentWorktrees["agent-123"]; got != worktree {
		t.Fatalf("expected agent worktree %q, got %q", worktree, got)
	}
}

func TestTrackAgent_NoopWhenNotEnabled(t *testing.T) {
	dir := t.TempDir()
	// No state file -- TrackAgent should be a no-op
	if err := TrackAgent(dir, "agent-123", "paivot-graph:designer"); err != nil {
		t.Fatalf("TrackAgent() without state should not error: %v", err)
	}
}

func TestUntrackAgent_RemovesFromActiveAgents(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := On(dir); err != nil {
		t.Fatal(err)
	}
	if err := TrackAgent(dir, "agent-123", "paivot-graph:designer"); err != nil {
		t.Fatal(err)
	}

	if err := UntrackAgent(dir, "agent-123"); err != nil {
		t.Fatalf("UntrackAgent() error: %v", err)
	}

	state, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveAgents) != 0 {
		t.Errorf("expected empty active_agents, got %v", state.ActiveAgents)
	}
}

func TestHasActiveBLTAgent_TrueWhenAgentPresent(t *testing.T) {
	state := &State{
		Enabled:      true,
		ActiveAgents: map[string]string{"agent-1": "paivot-graph:architect"},
	}
	if !HasActiveBLTAgent(state) {
		t.Error("expected true when agent is present")
	}
}

func TestHasActiveBLTAgent_FalseWhenEmpty(t *testing.T) {
	state := &State{
		Enabled:      true,
		ActiveAgents: map[string]string{},
	}
	if HasActiveBLTAgent(state) {
		t.Error("expected false when no agents")
	}
}

func TestHasActiveAgentType(t *testing.T) {
	state := &State{
		Enabled: true,
		ActiveAgents: map[string]string{
			"a1": "paivot-graph:architect",
			"a2": "paivot-graph:designer",
		},
	}

	if !HasActiveAgentType(state, "paivot-graph:architect") {
		t.Error("expected architect agent to be detected")
	}
	if HasActiveAgentType(state, "paivot-graph:business-analyst") {
		t.Error("did not expect unrelated agent type to be detected")
	}
}

func TestHasActiveAgentTypeAtPath(t *testing.T) {
	state := &State{
		Enabled: true,
		ActiveAgents: map[string]string{
			"a1": "paivot-graph:architect",
		},
		AgentWorktrees: map[string]string{
			"a1": "/project/.claude/worktrees/agent-a1",
		},
	}

	if !HasActiveAgentTypeAtPath(state, "paivot-graph:architect", "/project/.claude/worktrees/agent-a1") {
		t.Error("expected exact worktree path match")
	}
	if !HasActiveAgentTypeAtPath(state, "paivot-graph:architect", "/project/.claude/worktrees/agent-a1/subdir") {
		t.Error("expected subdir under worktree to match")
	}
	if HasActiveAgentTypeAtPath(state, "paivot-graph:architect", "/project") {
		t.Error("did not expect orchestrator root to match agent worktree")
	}
}

func TestStateFileName(t *testing.T) {
	if StateFileName() != ".dispatcher-state.json" {
		t.Errorf("unexpected state file name: %s", StateFileName())
	}
}

func TestOn_CreatesDirectoryIfNeeded(t *testing.T) {
	dir := t.TempDir()
	// Don't pre-create .vault/
	if err := On(dir); err != nil {
		t.Fatalf("On() should create directories: %v", err)
	}

	state, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error after On(): %v", err)
	}
	if !state.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestKnowledgeDirs_ContainsExpectedSubfolders(t *testing.T) {
	// "uat" must be present: the retro agent writes UAT scripts to
	// .vault/knowledge/uat/.
	expected := []string{"conventions", "decisions", "patterns", "debug", "skills", "uat"}
	for _, want := range expected {
		found := false
		for _, dir := range knowledgeDirs {
			if dir == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("knowledgeDirs missing %q", want)
		}
	}
}

func TestOn_InitializesProjectVault(t *testing.T) {
	dir := t.TempDir()
	// Start from nothing -- On() should build the full vault tree
	if err := On(dir); err != nil {
		t.Fatalf("On() error: %v", err)
	}

	// Check knowledge subdirectories
	for _, sub := range knowledgeDirs {
		path := filepath.Join(dir, ".vault", "knowledge", sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected .vault/knowledge/%s/ to exist: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("expected .vault/knowledge/%s/ to be a directory", sub)
		}
	}

	// Check .vault/issues/
	issuesDir := filepath.Join(dir, ".vault", "issues")
	info, err := os.Stat(issuesDir)
	if err != nil {
		t.Errorf("expected .vault/issues/ to exist: %v", err)
	} else if !info.IsDir() {
		t.Error("expected .vault/issues/ to be a directory")
	}

	// Check default .settings.yaml
	settingsPath := filepath.Join(dir, ".vault", "knowledge", ".settings.yaml")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("expected .settings.yaml to exist: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty .settings.yaml")
	}

	// .nd-shared.yaml should NOT be auto-created -- shared worktree mode is opt-in.
	sharedPath := filepath.Join(dir, ".vault", ".nd-shared.yaml")
	if _, err := os.Stat(sharedPath); err == nil {
		t.Error(".nd-shared.yaml should not be auto-created; shared mode is opt-in")
	}
}

func TestOn_DoesNotOverwriteExistingSettings(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	custom := "# custom settings\nstack_detection: true\nworkflow.fsm: true\n"
	settingsPath := filepath.Join(settingsDir, ".settings.yaml")
	if err := os.WriteFile(settingsPath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}

	if err := On(dir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Errorf("settings were overwritten: got %q, want %q", string(data), custom)
	}
}

func TestEnsureGitignore_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	ensureGitignore(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore to be created: %v", err)
	}

	content := string(data)
	for _, entry := range gitignoreEntries {
		if !containsLine(content, entry) {
			t.Errorf("expected .gitignore to contain %q", entry)
		}
	}
}

func TestEnsureGitignore_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	existing := "*.pyc\n__pycache__/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	ensureGitignore(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Existing entries preserved
	if !containsLine(content, "*.pyc") {
		t.Error("lost existing .gitignore entry")
	}
	// New entries added
	for _, entry := range gitignoreEntries {
		if !containsLine(content, entry) {
			t.Errorf("expected .gitignore to contain %q", entry)
		}
	}
}

func TestEnsureGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ensureGitignore(dir)
	data1, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))

	ensureGitignore(dir)
	data2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))

	if string(data1) != string(data2) {
		t.Error("ensureGitignore is not idempotent -- content changed on second call")
	}
}

func TestEnsureGitignore_SkipsExistingEntries(t *testing.T) {
	dir := t.TempDir()
	existing := ".vault/issues/\n.vault/.nd.yaml\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	ensureGitignore(dir)

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	content := string(data)

	// Count occurrences of ".vault/issues/"
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".vault/issues/" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of .vault/issues/, got %d", count)
	}
}

func TestContainsLine(t *testing.T) {
	content := "foo\nbar\n  baz  \n"
	if !containsLine(content, "foo") {
		t.Error("should find 'foo'")
	}
	if !containsLine(content, "baz") {
		t.Error("should find 'baz' (trimmed)")
	}
	if containsLine(content, "qux") {
		t.Error("should not find 'qux'")
	}
}

func TestOn_CreatesGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := On(dir); err != nil {
		t.Fatalf("On() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore after On(): %v", err)
	}

	content := string(data)
	if !containsLine(content, ".vault/issues/") {
		t.Error("On() should gitignore .vault/issues/")
	}
}

func TestStateJSON_RoundTrip(t *testing.T) {
	original := State{
		Enabled: true,
		Since:   "2026-02-26T10:00:00Z",
		ActiveAgents: map[string]string{
			"a1": "paivot-graph:business-analyst",
			"a2": "paivot-graph:designer",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var restored State
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	if restored.Enabled != original.Enabled {
		t.Errorf("enabled mismatch: %v vs %v", restored.Enabled, original.Enabled)
	}
	if len(restored.ActiveAgents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(restored.ActiveAgents))
	}
}
