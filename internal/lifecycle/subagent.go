package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

// subagentInput matches the JSON Claude Code sends to SubagentStart/SubagentStop hooks.
type subagentInput struct {
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// trackedAgentTypes are the agent types that dispatcher mode must track for
// structural enforcement.
var trackedAgentTypes = map[string]bool{
	"paivot-graph:business-analyst": true,
	"paivot-graph:designer":         true,
	"paivot-graph:architect":        true,
	"paivot-graph:sr-pm":            true,
	"paivot-graph:developer":        true,
	"paivot-graph:pm":               true,
}

// worktreeAgentTypes are agent types that typically work inside git worktrees.
// After these agents complete, the dispatcher's CWD may have drifted into the
// worktree. Emitting a reset instruction prevents the session-fatal CWD
// corruption that occurs when a worktree is removed while CWD points into it.
var worktreeAgentTypes = map[string]bool{
	"paivot-graph:developer": true,
	"paivot-graph:pm":        true,
}

// SubagentStart tracks a dispatcher-relevant subagent when it starts.
// Silent output -- does not block agent launch.
func SubagentStart() error {
	var input subagentInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return nil
	}

	if !trackedAgentTypes[input.AgentType] {
		return nil
	}

	cwd, _ := os.Getwd()
	if cwd == "" {
		return nil
	}

	_ = dispatcher.TrackAgent(cwd, input.AgentID, input.AgentType)
	return nil
}

// SubagentStop untracks a dispatcher-relevant subagent when it completes.
// It intentionally does NOT remove developer worktrees here: Claude Code may
// return control to the parent session with the subagent's CWD still active,
// and deleting that worktree inside the hook would strand the parent session in
// a non-existent directory before its next Bash command can run.
func SubagentStop() error {
	var input subagentInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return nil
	}

	if !trackedAgentTypes[input.AgentType] {
		return nil
	}

	cwd, _ := os.Getwd()
	if cwd == "" {
		return nil
	}

	_ = dispatcher.UntrackAgent(cwd, input.AgentID)

	if worktreeAgentTypes[input.AgentType] {
		emitCWDResetWarning(cwd, input.AgentType)
	}
	return nil
}

// emitCWDResetWarning checks if dispatcher mode is active and outputs a
// CWD reset instruction as a {"systemMessage": ...} JSON line.
//
// SubagentStop stdout is NOT injected into the model's context -- it is
// transcript-only, so the model never sees this output. The systemMessage
// form at least surfaces the warning to the user. The real protections
// against session-fatal CWD corruption are the piv-loop CWD reset protocol
// and the pvg worktree remove CWD guard.
func emitCWDResetWarning(cwd, agentType string) {
	// Only emit when Paivot dispatcher is active.
	state, _, err := dispatcher.ReadStateRoot(cwd)
	if err != nil || !state.Enabled {
		return
	}

	root := resolveGitRootQuiet()
	if root == "" {
		root = cwd
	}

	msg := fmt.Sprintf(
		"[CWD-RESET MANDATORY] %s agent completed. The dispatcher's very first Bash command must be: cd %s && pwd -- only after that reset should it remove the dev worktree. Skipping this can kill the session.",
		agentType, root)
	data, err := json.Marshal(map[string]string{"systemMessage": msg})
	if err != nil {
		return
	}
	fmt.Println(string(data))
}

// resolveGitRootQuiet returns the git repo root or empty string on failure.
func resolveGitRootQuiet() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
