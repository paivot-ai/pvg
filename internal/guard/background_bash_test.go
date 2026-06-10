package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Regression: developers on a saturated host backgrounded the in-container
// compile, ended their turn, and were disposed with uncommitted work.
func TestCheckBackgroundBash_BlocksInDispatcherMode(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]interface{}{"enabled": true, "since": "2026-01-01T00:00:00Z"}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, ".dispatcher-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	blocked := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "mix test", RunInBackground: true}}
	if r := CheckBackgroundBash(root, blocked); r.Allowed {
		t.Fatal("expected backgrounded Bash to be blocked in dispatcher mode")
	}

	sync := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "mix test"}}
	if r := CheckBackgroundBash(root, sync); !r.Allowed {
		t.Fatalf("synchronous Bash must pass, got: %s", r.Reason)
	}
}

func TestCheckBackgroundBash_AllowsWhenDispatcherOff(t *testing.T) {
	root := t.TempDir()
	input := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: "sleep 5", RunInBackground: true}}
	if r := CheckBackgroundBash(root, input); !r.Allowed {
		t.Fatalf("expected allow without dispatcher mode, got: %s", r.Reason)
	}
}
