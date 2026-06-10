package lifecycle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/paivot-ai/vlt"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
)

func TestDetectProject_FallsBackToBasename(t *testing.T) {
	// Use a temporary directory (not a git repo)
	dir := t.TempDir()
	project := detectProject(dir)
	expected := filepath.Base(dir)
	if project != expected {
		t.Errorf("expected %q, got %q", expected, project)
	}
}

func TestExtractNoteSummary_ParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	note := filepath.Join(dir, "test.md")
	content := `---
type: decision
created: 2026-01-15
status: active
---

# My Decision

We decided to use Go instead of Rust.
`
	if err := os.WriteFile(note, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	date, firstLine := extractNoteSummary(note)
	if date != "2026-01-15" {
		t.Errorf("expected date 2026-01-15, got %q", date)
	}
	if firstLine != "We decided to use Go instead of Rust." {
		t.Errorf("expected first content line, got %q", firstLine)
	}
}

func TestExtractNoteSummary_HandlesNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	note := filepath.Join(dir, "bare.md")
	content := "Just a plain note with no frontmatter."
	if err := os.WriteFile(note, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	date, _ := extractNoteSummary(note)
	if date != "unknown" {
		t.Errorf("expected date unknown, got %q", date)
	}
}

func TestExtractNoteSummary_MissingFile(t *testing.T) {
	date, firstLine := extractNoteSummary("/nonexistent/path.md")
	if date != "unknown" {
		t.Errorf("expected unknown, got %q", date)
	}
	if firstLine != "(no summary)" {
		t.Errorf("expected (no summary), got %q", firstLine)
	}
}

func TestReadMaxNotesSetting_Default(t *testing.T) {
	dir := t.TempDir()
	n := readMaxNotesSetting(dir)
	if n != 10 {
		t.Errorf("expected default 10, got %d", n)
	}
}

func TestReadMaxNotesSetting_CustomValue(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsFile := filepath.Join(settingsDir, ".settings.yaml")
	if err := os.WriteFile(settingsFile, []byte("session_start_max_notes: 5\n"), 0644); err != nil {
		t.Fatal(err)
	}

	n := readMaxNotesSetting(dir)
	if n != 5 {
		t.Errorf("expected 5, got %d", n)
	}
}

func TestStaticOperatingMode_ContainsKeyContent(t *testing.T) {
	mode := staticOperatingMode()
	checks := []string{
		"CONCURRENCY LIMITS",
		"BEFORE STARTING",
		"WHILE WORKING",
		"BEFORE ENDING",
		"DISPATCHER MODE",
		"D&F ORCHESTRATION",
		"QUESTIONS_FOR_USER",
		"vlt vault=",
	}
	for _, check := range checks {
		if !strings.Contains(mode, check) {
			t.Errorf("static operating mode missing %q", check)
		}
	}
}

func TestFormatVaultSearchOutput_NoResults(t *testing.T) {
	got := formatVaultSearchOutput(nil, nil)
	want := "(none found -- this is a new project to the vault)"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestFormatVaultSearchOutput_DegradedError(t *testing.T) {
	got := formatVaultSearchOutput(nil, errors.New("vault lock denied"))
	if !strings.Contains(got, "vault search unavailable -- degraded mode") {
		t.Errorf("expected degraded mode message, got %q", got)
	}
	if !strings.Contains(got, "vault lock denied") {
		t.Errorf("expected original error in message, got %q", got)
	}
}

func TestFormatVaultSearchOutput_WithResults(t *testing.T) {
	results := []vlt.SearchResult{
		{Title: "paivot-graph", RelPath: "projects/paivot-graph.md"},
		{Title: "Testing Philosophy", RelPath: "patterns/testing-philosophy.md"},
	}
	got := formatVaultSearchOutput(results, nil)
	if !strings.Contains(got, "paivot-graph (projects/paivot-graph.md)") {
		t.Errorf("expected first result in output, got %q", got)
	}
	if !strings.Contains(got, "Testing Philosophy (patterns/testing-philosophy.md)") {
		t.Errorf("expected second result in output, got %q", got)
	}
}

func TestFormatOperatingModeOutput_DegradedError(t *testing.T) {
	got := formatOperatingModeOutput("", errors.New("permission denied"))
	if !strings.Contains(got, "Operating mode unavailable from vault -- using built-in fallback") {
		t.Errorf("expected fallback message, got %q", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Errorf("expected original error in output, got %q", got)
	}
	if !strings.Contains(got, "CONCURRENCY LIMITS") {
		t.Errorf("expected built-in operating mode content, got %q", got)
	}
}

func TestFormatOperatingModeOutput_WithContent(t *testing.T) {
	got := formatOperatingModeOutput("Use dispatcher mode.", nil)
	if got != "[VAULT] Operating mode for this session (from vault):\n\nUse dispatcher mode.\n" {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestStaticDispatcherReminder_ContainsKeyContent(t *testing.T) {
	text := staticDispatcherReminder()
	checks := []string{
		"DISPATCHER MODE",
		"SURVIVES COMPACTION",
		"COORDINATOR",
		"NEVER write BUSINESS.md",
		"NEVER skip agents",
		"compaction boundaries",
	}
	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Errorf("static dispatcher reminder missing %q", check)
		}
	}
}

func TestOutputDispatcherReminder_NoopWhenInactive(t *testing.T) {
	// In a temp dir with no state file, outputDispatcherReminder should be a no-op.
	// We can't easily capture stdout here, but at minimum verify no panic.
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	outputDispatcherReminder() // should not panic
}

func TestStaticPreCompact_ContainsKeyContent(t *testing.T) {
	text := staticPreCompact()
	checks := []string{
		"DECISIONS",
		"PATTERNS",
		"DEBUG INSIGHTS",
		"PROJECT UPDATES",
		"vlt vault=",
	}
	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Errorf("static pre-compact missing %q", check)
		}
	}
}

func TestStaticStopChecklist_ContainsKeyContent(t *testing.T) {
	text := staticStopChecklist()
	checks := []string{
		"DECISIONS",
		"PATTERNS",
		"DEBUG INSIGHTS",
		"PROJECT INDEX NOTE",
	}
	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Errorf("static stop checklist missing %q", check)
		}
	}
}

func TestFormatSessionEntry_NoLinks(t *testing.T) {
	entry := formatSessionEntry("2026-02-25", nil)
	if !strings.Contains(entry, "Session log (2026-02-25)") {
		t.Error("missing date in session entry")
	}
	if !strings.Contains(entry, "Session ended normally") {
		t.Error("missing status line")
	}
	if strings.Contains(entry, "Notes created") {
		t.Error("should not have Notes created line when no links")
	}
}

func TestFormatSessionEntry_WithLinks(t *testing.T) {
	links := []string{"pvg Go CLI architecture", "Three-Tier Knowledge Model"}
	entry := formatSessionEntry("2026-02-25", links)
	if !strings.Contains(entry, "[[pvg Go CLI architecture]]") {
		t.Error("missing first wikilink")
	}
	if !strings.Contains(entry, "[[Three-Tier Knowledge Model]]") {
		t.Error("missing second wikilink")
	}
	if !strings.Contains(entry, "Notes created: [[pvg Go CLI architecture]], [[Three-Tier Knowledge Model]]") {
		t.Errorf("unexpected format: %s", entry)
	}
}

func TestFormatSessionEntry_SingleLink(t *testing.T) {
	links := []string{"Some Decision"}
	entry := formatSessionEntry("2026-02-25", links)
	if !strings.Contains(entry, "- Notes created: [[Some Decision]]\n") {
		t.Errorf("unexpected format for single link: %s", entry)
	}
	// No comma for single link
	if strings.Contains(entry, ", ") {
		t.Error("should not have comma with single link")
	}
}

func TestCollectLocalLinks_NoVaultDir(t *testing.T) {
	dir := t.TempDir()
	links := collectLocalLinks(dir, "2026-02-25")
	// No .vault/knowledge/ exists -- should return empty
	if len(links) != 0 {
		t.Errorf("expected empty links, got %v", links)
	}
}

func TestCollectLocalLinks_FindsLocalNotes(t *testing.T) {
	dir := t.TempDir()
	knowledgeDir := filepath.Join(dir, ".vault", "knowledge", "decisions")
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	note := filepath.Join(knowledgeDir, "Use Go for CLI.md")
	if err := os.WriteFile(note, []byte("# Use Go for CLI\n"), 0644); err != nil {
		t.Fatal(err)
	}

	links := collectLocalLinks(dir, today)
	// The note was just created, so its mtime is today
	found := false
	for _, l := range links {
		if l == "Use Go for CLI" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Use Go for CLI' in links, got %v", links)
	}
}

func TestBuildSessionSearchQuery_UsesBracketFilters(t *testing.T) {
	got := buildSessionSearchQuery("my project", "2026-03-12")
	want := "[project:my project] [created:2026-03-12]"
	if got != want {
		t.Fatalf("buildSessionSearchQuery() = %q, want %q", got, want)
	}
}

// --- detectStack tests ---

func TestDetectStack_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	stacks := detectStack(dir)
	if len(stacks) != 0 {
		t.Errorf("expected empty stacks, got %v", stacks)
	}
}

func TestDetectStack_GoProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "go" {
		t.Errorf("expected [go], got %v", stacks)
	}
}

func TestDetectStack_RustProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "rust" {
		t.Errorf("expected [rust], got %v", stacks)
	}
}

func TestDetectStack_NodeProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "node" {
		t.Errorf("expected [node], got %v", stacks)
	}
}

func TestDetectStack_TypeScriptProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"devDependencies":{"typescript":"^5.0"}}`), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 2 || stacks[0] != "node" || stacks[1] != "typescript" {
		t.Errorf("expected [node, typescript], got %v", stacks)
	}
}

func TestDetectStack_PythonPyproject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "python" {
		t.Errorf("expected [python], got %v", stacks)
	}
}

func TestDetectStack_PythonRequirements(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "python" {
		t.Errorf("expected [python], got %v", stacks)
	}
}

func TestDetectStack_ElixirProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "mix.exs"), []byte("defmodule Test do\nend\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "elixir" {
		t.Errorf("expected [elixir], got %v", stacks)
	}
}

func TestDetectStack_CSharpProject_Csproj(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "MyApp.csproj"), []byte("<Project />\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "csharp" {
		t.Errorf("expected [csharp], got %v", stacks)
	}
}

func TestDetectStack_CSharpProject_Sln(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "MyApp.sln"), []byte("Solution\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "csharp" {
		t.Errorf("expected [csharp], got %v", stacks)
	}
}

func TestDetectStack_MonorepoMultipleStacks(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"devDependencies":{"typescript":"^5"}}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 4 {
		t.Errorf("expected 4 stacks (go, node, typescript, python), got %v", stacks)
	}
}

func TestDetectStack_JavaProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project />\n"), 0644)
	stacks := detectStack(dir)
	if len(stacks) != 1 || stacks[0] != "java" {
		t.Errorf("expected [java], got %v", stacks)
	}
}

// --- readStackDetectionSetting tests ---

func TestReadStackDetectionSetting_Default(t *testing.T) {
	dir := t.TempDir()
	if readStackDetectionSetting(dir) {
		t.Error("expected false when no settings file exists")
	}
}

func TestReadStackDetectionSetting_Enabled(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsFile := filepath.Join(settingsDir, ".settings.yaml")
	if err := os.WriteFile(settingsFile, []byte("stack_detection: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !readStackDetectionSetting(dir) {
		t.Error("expected true when stack_detection is set to true")
	}
}

func TestOutputProjectKnowledge_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	knowledgeDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Should not panic on empty directories
	// Capture stdout would require redirecting os.Stdout, which is overkill.
	// Just verify it doesn't panic.
	outputProjectKnowledge(knowledgeDir, dir)
}

func TestOutputProjectKnowledge_WithNotes(t *testing.T) {
	dir := t.TempDir()
	decisionsDir := filepath.Join(dir, ".vault", "knowledge", "decisions")
	if err := os.MkdirAll(decisionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	note := `---
type: decision
created: 2026-02-25
---

# Use Go for CLI

We chose Go for the pvg CLI because it compiles to a single binary.
`
	if err := os.WriteFile(filepath.Join(decisionsDir, "Use Go for CLI.md"), []byte(note), 0644); err != nil {
		t.Fatal(err)
	}

	// Should not panic with actual notes
	knowledgeDir := filepath.Join(dir, ".vault", "knowledge")
	outputProjectKnowledge(knowledgeDir, dir)
}

func TestOutputProjectKnowledge_IncludesUATSubfolder(t *testing.T) {
	dir := t.TempDir()
	uatDir := filepath.Join(dir, ".vault", "knowledge", "uat")
	if err := os.MkdirAll(uatDir, 0755); err != nil {
		t.Fatal(err)
	}
	note := "---\ntype: convention\ncreated: 2026-06-01\n---\n\nUAT script for checkout flow.\n"
	if err := os.WriteFile(filepath.Join(uatDir, "Checkout UAT.md"), []byte(note), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdoutDuring(t, func() {
		outputProjectKnowledge(filepath.Join(dir, ".vault", "knowledge"), dir)
	})

	if !strings.Contains(out, "uat/Checkout UAT") {
		t.Errorf("expected uat note in project knowledge output, got %q", out)
	}
}

func TestCleanupStaleLoop_PreservesByDefault(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write an active loop state file
	stateContent := `{"active":true,"mode":"all","iteration":5,"max_iterations":50,"consecutive_waits":0,"max_consecutive_waits":3,"wait_iterations":0,"started_at":"2026-03-06T18:00:00Z"}`
	statePath := filepath.Join(vaultDir, ".piv-loop-state.json")
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatal(err)
	}

	// No settings file = persist enabled (the default); state must survive
	// so background agent completions can resume the loop in a new session.
	cleanupStaleLoop(dir)

	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("expected loop state file to be preserved by default (persist=true)")
	}
}

func TestCleanupStaleLoop_RemovesWhenPersistExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	knowledgeDir := filepath.Join(vaultDir, "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		t.Fatal(err)
	}

	stateContent := `{"active":true,"mode":"all","iteration":5,"max_iterations":50,"consecutive_waits":0,"max_consecutive_waits":3,"wait_iterations":0,"started_at":"2026-03-06T18:00:00Z"}`
	statePath := filepath.Join(vaultDir, ".piv-loop-state.json")
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatal(err)
	}

	settingsContent := "loop.persist_across_sessions: false\n"
	if err := os.WriteFile(filepath.Join(knowledgeDir, ".settings.yaml"), []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	cleanupStaleLoop(dir)

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("expected loop state file to be removed when persist=false")
	}
}

func TestCleanupStaleLoop_PreservesWhenPersistExplicitlyEnabled(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, ".vault")
	knowledgeDir := filepath.Join(vaultDir, "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write an active loop state file
	stateContent := `{"active":true,"mode":"all","iteration":5,"max_iterations":50,"consecutive_waits":0,"max_consecutive_waits":3,"wait_iterations":0,"started_at":"2026-03-06T18:00:00Z"}`
	statePath := filepath.Join(vaultDir, ".piv-loop-state.json")
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Explicitly enable persist
	settingsContent := "loop.persist_across_sessions: true\n"
	if err := os.WriteFile(filepath.Join(knowledgeDir, ".settings.yaml"), []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	cleanupStaleLoop(dir)

	// State file should still exist
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("expected loop state file to be preserved when persist=true")
	}
}

func TestCleanupStaleLoop_NoopWhenNoState(t *testing.T) {
	dir := t.TempDir()
	// No state file at all -- should not panic
	cleanupStaleLoop(dir)
}

func TestShouldCleanupStaleLoop_SourceGating(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"startup", true},
		{"clear", true},
		{"", true}, // missing source: treat as fresh session
		{"compact", false},
		{"resume", false},
	}
	for _, tt := range tests {
		if got := shouldCleanupStaleLoop(tt.source); got != tt.want {
			t.Errorf("shouldCleanupStaleLoop(%q) = %v, want %v", tt.source, got, tt.want)
		}
	}
}

func TestHookInput_ParsesSource(t *testing.T) {
	var input hookInput
	if err := json.Unmarshal([]byte(`{"cwd":"/tmp/project","source":"compact"}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.CWD != "/tmp/project" {
		t.Errorf("CWD = %q", input.CWD)
	}
	if input.Source != "compact" {
		t.Errorf("Source = %q, want compact", input.Source)
	}
}

func TestSessionStartAfterCompact_EmitsDispatcherAndLoopContext(t *testing.T) {
	dir := t.TempDir()
	if err := dispatcher.On(dir); err != nil {
		t.Fatalf("dispatcher.On: %v", err)
	}

	state := loop.NewState("epic", "PROJ-epic", 50)
	state.Iteration = 4
	if err := loop.WriteState(dir, state); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	out := captureStdoutDuring(t, func() {
		if err := sessionStartAfterCompact(dir); err != nil {
			t.Errorf("sessionStartAfterCompact: %v", err)
		}
	})

	checks := []string{
		"DISPATCHER MODE -- SURVIVES COMPACTION",
		"LOOP RECOVERY -- RUN IMMEDIATELY AFTER COMPACTION",
		"[LOOP] Active loop: epic PROJ-epic, iteration 4.",
		"pvg loop recover",
		"pvg loop next --json",
		"CONCURRENCY LIMITS", // operating-mode fallback
	}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("compact-mode output missing %q", check)
		}
	}

	// Loop state must survive compaction.
	if _, err := os.Stat(loop.StatePath(dir)); err != nil {
		t.Errorf("expected loop state to survive compaction handling: %v", err)
	}
}

func TestSessionStartAfterCompact_NoDispatcherNoLoop(t *testing.T) {
	dir := t.TempDir()

	out := captureStdoutDuring(t, func() {
		if err := sessionStartAfterCompact(dir); err != nil {
			t.Errorf("sessionStartAfterCompact: %v", err)
		}
	})

	if strings.Contains(out, "DISPATCHER MODE -- SURVIVES COMPACTION") {
		t.Error("dispatcher reminder must not appear when dispatcher mode is off")
	}
	if strings.Contains(out, "[LOOP] Active loop:") {
		t.Error("loop status must not appear when no loop is active")
	}
	if !strings.Contains(out, "CONCURRENCY LIMITS") {
		t.Error("operating-mode fallback must always be emitted on compact")
	}
}

func TestSessionStart_CompactSourcePreservesLoopState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := loop.NewState("all", "", 50)
	if err := loop.WriteState(dir, state); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	// Persist explicitly disabled: even so, compact must NOT clean up.
	knowledgeDir := filepath.Join(dir, ".vault", "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledgeDir, ".settings.yaml"), []byte("loop.persist_across_sessions: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"cwd":` + strconv.Quote(dir) + `,"source":"compact"}`)
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = stdinW.Close()
	origStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = origStdin }()

	_ = captureStdoutDuring(t, func() {
		if err := SessionStart(); err != nil {
			t.Errorf("SessionStart: %v", err)
		}
	})

	if _, err := os.Stat(loop.StatePath(dir)); err != nil {
		t.Errorf("expected active loop state to survive compaction, got %v", err)
	}
}

func TestCleanupStaleLoop_RemovesAncestorStateFromNestedWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "agent-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	state := `{"active":true,"mode":"all","iteration":5,"max_iterations":50,"consecutive_waits":0,"max_consecutive_waits":3,"wait_iterations":0,"started_at":"2026-03-06T18:00:00Z"}`
	statePath := filepath.Join(root, ".vault", ".piv-loop-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	// Persist explicitly disabled at the project root that owns the state.
	knowledgeDir := filepath.Join(root, ".vault", "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledgeDir, ".settings.yaml"), []byte("loop.persist_across_sessions: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupStaleLoop(worktree)

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("expected ancestor loop state file to be removed when persist=false")
	}
}
