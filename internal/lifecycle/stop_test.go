package lifecycle

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/loop"
)

func TestBuildContinuationPrompt_WaitLike_NoDeliveries(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 3,
		Reason:       "Waiting for in-progress work to complete",
	}
	wc := &loop.WorkCounts{InProgress: 4}

	prompt := BuildContinuationPrompt(state, decision, "unlimited", wc)

	if !strings.Contains(prompt, "Wait for completions") {
		t.Error("expected wait instruction for wait-like scenario")
	}
	if !strings.Contains(prompt, "Do NOT produce explanatory output") {
		t.Error("expected silence instruction when no deliveries")
	}
	if strings.Contains(prompt, "Continue:") {
		t.Error("should not include full continuation prompt for wait-like")
	}
}

func TestBuildContinuationPrompt_WaitLike_WithDeliveries(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 5,
		Reason:       "Waiting for in-progress work to complete",
	}
	wc := &loop.WorkCounts{InProgress: 2, Delivered: 3}

	prompt := BuildContinuationPrompt(state, decision, "50", wc)

	if !strings.Contains(prompt, "3 delivered stories await PM review") {
		t.Error("expected delivery info when deliveries exist")
	}
	if strings.Contains(prompt, "Do NOT produce explanatory output") {
		t.Error("should not silence when there are deliveries to act on")
	}
}

func TestBuildContinuationPrompt_Actionable_ReadyOnly(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 2,
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 3}

	prompt := BuildContinuationPrompt(state, decision, "50", wc)

	if !strings.Contains(prompt, "Continue.") {
		t.Error("expected continuation prompt for actionable work")
	}
	if !strings.Contains(prompt, "3 ready") {
		t.Error("expected ready count")
	}
}

func TestBuildContinuationPrompt_Actionable_WithDeliveries(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 4,
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 2, Delivered: 1, InProgress: 1}

	prompt := BuildContinuationPrompt(state, decision, "50", wc)

	if !strings.Contains(prompt, "1 delivered") {
		t.Error("expected delivered count")
	}
	if !strings.Contains(prompt, "2 ready") {
		t.Error("expected ready count")
	}
}

func TestBuildContinuationPrompt_StackDependentConcurrency(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 5, // refresh iteration: full protocol reminder included
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 3}

	prompt := BuildContinuationPrompt(state, decision, "50", wc)

	want := "Concurrency: within current epic only, stack-dependent limits (heavy stacks: 2 dev / 1 PM / 3 total; light stacks: 4 dev / 2 PM / 6 total). Dispatcher-only."
	if !strings.Contains(prompt, want) {
		t.Errorf("expected stack-dependent concurrency line, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "Max 2 dev") {
		t.Error("hardcoded heavy-stack-only limits must not appear in the prompt")
	}
}

func TestBuildContinuationPrompt_EpicScope(t *testing.T) {
	state := &loop.State{Mode: "epic", TargetEpic: "PROJ-a1b", AutoRotate: true}
	decision := &loop.StopDecision{
		NewIteration: 1,
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 1}

	prompt := BuildContinuationPrompt(state, decision, "10", wc)

	if !strings.Contains(prompt, "Current epic: PROJ-a1b") {
		t.Error("expected current epic context in prompt")
	}
	if !strings.Contains(prompt, "auto-rotate=true") {
		t.Error("expected auto-rotate indicator in prompt")
	}
	if !strings.Contains(prompt, "scoped to the current epic") {
		t.Error("expected containment instruction in prompt")
	}
	if !strings.Contains(prompt, "pvg loop next --json") {
		t.Error("expected pvg loop next instruction in prompt")
	}
}

func TestBuildContinuationPrompt_Header(t *testing.T) {
	state := &loop.State{Mode: "all"}
	decision := &loop.StopDecision{
		NewIteration: 7,
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 1, Delivered: 2, InProgress: 3, Blocked: 4, Other: 5}

	prompt := BuildContinuationPrompt(state, decision, "20", wc)

	if !strings.Contains(prompt, "[LOOP] Iteration 7/20") {
		t.Error("expected header with iteration info")
	}
	if !strings.Contains(prompt, "Ready: 1") {
		t.Error("expected Ready count in header")
	}
	if !strings.Contains(prompt, "Delivered: 2") {
		t.Error("expected Delivered count in header")
	}
	if !strings.Contains(prompt, "In-progress: 3") {
		t.Error("expected In-progress count in header")
	}
	if !strings.Contains(prompt, "Blocked: 4") {
		t.Error("expected Blocked count in header")
	}
	if !strings.Contains(prompt, "Other: 5") {
		t.Error("expected Other count in header")
	}
}

func TestCheckLoop_PreservesStateWhenNDQueryFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := loop.NewState("all", "", 50)
	state.Iteration = 7
	state.ConsecutiveWaits = 2
	state.WaitIterations = 2
	if err := loop.WriteState(dir, state); err != nil {
		t.Fatalf("WriteState() error: %v", err)
	}

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	ndPath := filepath.Join(binDir, "nd")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(ndPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := checkLoop(dir); err != nil {
		t.Fatalf("checkLoop() error: %v", err)
	}

	preserved, err := loop.ReadState(dir)
	if err != nil {
		t.Fatalf("expected loop state to remain after nd query failure: %v", err)
	}
	if preserved.Iteration != state.Iteration {
		t.Errorf("expected iteration %d preserved, got %d", state.Iteration, preserved.Iteration)
	}
	if preserved.ConsecutiveWaits != state.ConsecutiveWaits {
		t.Errorf("expected consecutive waits %d preserved, got %d", state.ConsecutiveWaits, preserved.ConsecutiveWaits)
	}
	if preserved.WaitIterations != state.WaitIterations {
		t.Errorf("expected wait iterations %d preserved, got %d", state.WaitIterations, preserved.WaitIterations)
	}
}

func TestCheckLoop_BlockEmitsFullPromptAsReason(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	state := loop.NewState("all", "", 50)
	if err := loop.WriteState(dir, state); err != nil {
		t.Fatalf("WriteState() error: %v", err)
	}

	// Stub nd: one ready story, nothing else -- forces a block decision.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
  *"ready --json"*) printf '[{"ID":"PROJ-s1","Title":"Story","Status":"open"}]' ;;
  *) printf '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "nd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := captureStdoutDuring(t, func() {
		if err := checkLoop(dir); err != nil {
			t.Errorf("checkLoop() error: %v", err)
		}
	})

	var continuation map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &continuation); err != nil {
		t.Fatalf("expected JSON continuation on stdout, got %q: %v", out, err)
	}
	if continuation["decision"] != "block" {
		t.Fatalf("expected decision=block, got %v", continuation["decision"])
	}
	// Claude Code's Stop hook contract only recognizes top-level decision and
	// reason: the full continuation prompt must BE the reason.
	reason, _ := continuation["reason"].(string)
	if !strings.Contains(reason, "[LOOP] Iteration") {
		t.Errorf("expected full prompt header in reason, got %q", reason)
	}
	if !strings.Contains(reason, "pvg loop next --json") {
		t.Errorf("expected continuation instructions in reason, got %q", reason)
	}
	if _, hasOptions := continuation["options"]; hasOptions {
		t.Error("continuation must not contain an options key (not part of the Stop hook contract)")
	}
}

func captureStdoutDuring(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}

func TestCheckLoop_UsesAncestorLoopStateFromNestedWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "agent-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	state := loop.NewState("all", "", 50)
	if err := loop.WriteState(root, state); err != nil {
		t.Fatalf("WriteState() error: %v", err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ndPath := filepath.Join(binDir, "nd")
	script := "#!/bin/sh\nprintf '[]\\n'\n"
	if err := os.WriteFile(ndPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := checkLoop(worktree); err != nil {
		t.Fatalf("checkLoop() error: %v", err)
	}

	if _, err := os.Stat(loop.StatePath(root)); !os.IsNotExist(err) {
		t.Fatal("expected ancestor loop state to be removed when backlog is empty")
	}
}

// Off-refresh iterations stay compact: the long protocol reminders appear
// only on iteration 1 and every 5th, not in every blocked-stop reason.
func TestBuildContinuationPrompt_CompactBetweenRefreshes(t *testing.T) {
	state := &loop.State{Mode: "epic", TargetEpic: "PROJ-e1"}
	decision := &loop.StopDecision{
		NewIteration: 22,
		Reason:       "Actionable work remains",
	}
	wc := &loop.WorkCounts{Ready: 40, Delivered: 3}

	prompt := BuildContinuationPrompt(state, decision, "50", wc)

	if strings.Contains(prompt, "Concurrency:") || strings.Contains(prompt, "scoped to the current epic") {
		t.Errorf("iteration 22 must be compact, got:\n%s", prompt)
	}
	for _, want := range []string{"Iteration 22/50", "Continue.", "3 delivered", "40 ready"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("compact prompt missing %q:\n%s", want, prompt)
		}
	}

	decision.NewIteration = 25 // multiple of 5: refresh
	prompt = BuildContinuationPrompt(state, decision, "50", wc)
	if !strings.Contains(prompt, "Concurrency:") {
		t.Errorf("iteration 25 must include the protocol refresh:\n%s", prompt)
	}
}
