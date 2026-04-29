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
// mandatory CWD reset instruction. This is the last line of defense against
// session-fatal CWD corruption caused by Claude Code leaking the subagent's
// CWD to the parent session before the dispatcher removes the worktree.
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

	fmt.Printf("[CWD-RESET MANDATORY] %s agent completed.\n", agentType)
	fmt.Printf("Your VERY FIRST Bash command MUST be:\n")
	fmt.Printf("  cd %s && pwd\n", root)
	fmt.Printf("Only after that reset should the dispatcher remove the dev worktree.\n")
	fmt.Printf("If you skip this, your session may die. This is not optional.\n")
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
