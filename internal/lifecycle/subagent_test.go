package lifecycle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

func TestTrackedAgentTypes_ContainsExpected(t *testing.T) {
	expected := []string{
		"paivot-graph:business-analyst",
		"paivot-graph:designer",
		"paivot-graph:architect",
		"paivot-graph:sr-pm",
		"paivot-graph:developer",
		"paivot-graph:pm",
	}
	for _, agentType := range expected {
		if !trackedAgentTypes[agentType] {
			t.Errorf("expected %q in trackedAgentTypes", agentType)
		}
	}
}

func TestTrackedAgentTypes_RejectsUntracked(t *testing.T) {
	untracked := []string{
		"paivot-graph:anchor",
		"paivot-graph:retro",
		"general-purpose",
	}
	for _, agentType := range untracked {
		if trackedAgentTypes[agentType] {
			t.Errorf("did not expect %q in trackedAgentTypes", agentType)
		}
	}
}

func TestSubagentStop_LeavesDeveloperWorktreeInPlaceAndWarns(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "dev-PRA-1234")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.On(root); err != nil {
		t.Fatalf("dispatcher.On: %v", err)
	}
	if err := dispatcher.TrackAgent(worktree, "agent-123", "paivot-graph:developer"); err != nil {
		t.Fatalf("dispatcher.TrackAgent: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatalf("os.Chdir(%s): %v", worktree, err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	payload, err := json.Marshal(subagentInput{
		AgentID:   "agent-123",
		AgentType: "paivot-graph:developer",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin: %v", err)
	}
	if _, err := stdinW.Write(payload); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("stdin close: %v", err)
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout: %v", err)
	}

	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = stdinR
	os.Stdout = stdoutW
	defer func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
	}()

	if err := SubagentStop(); err != nil {
		t.Fatalf("SubagentStop: %v", err)
	}

	if err := stdoutW.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(stdoutR); err != nil {
		t.Fatalf("stdout read: %v", err)
	}

	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("expected worktree to remain after SubagentStop, got %v", err)
	}

	state, err := dispatcher.ReadState(root)
	if err != nil {
		t.Fatalf("dispatcher.ReadState: %v", err)
	}
	if len(state.ActiveAgents) != 0 {
		t.Fatalf("expected active agents to be cleared, got %#v", state.ActiveAgents)
	}
	if len(state.AgentWorktrees) != 0 {
		t.Fatalf("expected tracked worktrees to be cleared, got %#v", state.AgentWorktrees)
	}

	// SubagentStop stdout is transcript-only (never reaches the model), so
	// the warning must be a single-line {"systemMessage": ...} JSON payload
	// that at least surfaces to the user.
	output := strings.TrimSpace(out.String())
	var warning struct {
		SystemMessage string `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(output), &warning); err != nil {
		t.Fatalf("expected JSON systemMessage payload, got %q: %v", output, err)
	}
	if !strings.Contains(warning.SystemMessage, "[CWD-RESET MANDATORY] paivot-graph:developer agent completed.") {
		t.Fatalf("expected reset warning, got %q", warning.SystemMessage)
	}
	if !strings.Contains(warning.SystemMessage, "remove the dev worktree") {
		t.Fatalf("expected cleanup ordering guidance, got %q", warning.SystemMessage)
	}
	if strings.Count(output, "\n") != 0 {
		t.Fatalf("expected single-line JSON output, got %q", output)
	}
}
