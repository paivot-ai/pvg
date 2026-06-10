// Package dispatcher manages dispatcher mode state for the paivot-graph plugin.
//
// When dispatcher mode is active, the orchestrator (main Claude session) is
// restricted to coordination only -- it cannot write D&F artifacts directly.
// BLT agents (BA, Designer, Architect) are tracked so the guard can distinguish
// agent writes from orchestrator writes.
//
// State is persisted in .vault/.dispatcher-state.json (not .vault/knowledge/,
// to avoid creating the project knowledge directory as a side effect).
package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stateFile is the filename within .vault/ for dispatcher state.
// Lives in .vault/ (not .vault/knowledge/) because it is ephemeral runtime
// state, not knowledge. This avoids creating .vault/knowledge/ as a side
// effect in projects that don't use a project vault.
const stateFile = ".dispatcher-state.json"

// State represents the current dispatcher mode state.
type State struct {
	Enabled        bool              `json:"enabled"`
	Since          string            `json:"since"`
	ActiveAgents   map[string]string `json:"active_agents"`             // agent_id -> agent_type
	AgentWorktrees map[string]string `json:"agent_worktrees,omitempty"` // agent_id -> worktree path
}

// statePath returns the full path to the state file for a project root.
func statePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vault", stateFile)
}

// On enables dispatcher mode for the given project root.
// Also initializes the project vault structure if it doesn't exist --
// saying "use Paivot" is an explicit opt-in, so we prepare the soil.
func On(projectRoot string) error {
	initProjectVault(projectRoot)

	state := State{
		Enabled:        true,
		Since:          time.Now().UTC().Format(time.RFC3339),
		ActiveAgents:   make(map[string]string),
		AgentWorktrees: make(map[string]string),
	}
	return writeState(projectRoot, state)
}

// knowledgeDirs are the subdirectories created under .vault/knowledge/
// when Paivot is activated. Matches the layout that session_start scans.
var knowledgeDirs = []string{
	"conventions",
	"decisions",
	"patterns",
	"debug",
	"skills",
	"uat",
}

// initProjectVault ensures the .vault/ directory tree exists.
// Idempotent: creates only what's missing. Never overwrites existing files.
func initProjectVault(projectRoot string) {
	base := filepath.Join(projectRoot, ".vault")

	// .vault/knowledge/ and its subdirectories
	for _, sub := range knowledgeDirs {
		_ = os.MkdirAll(filepath.Join(base, "knowledge", sub), 0755)
	}

	// .vault/issues/ for nd
	_ = os.MkdirAll(filepath.Join(base, "issues"), 0755)

	// Default .settings.yaml if it doesn't exist
	settingsPath := filepath.Join(base, "knowledge", ".settings.yaml")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		defaultSettings := "# paivot-graph project vault settings\n# Managed by: pvg settings key=value\n\nstack_detection: false\n"
		_ = os.WriteFile(settingsPath, []byte(defaultSettings), 0644)
	}

	// Ensure runtime state files are gitignored.
	// nd issues and loop/dispatcher state are filesystem-based and must not
	// be committed -- git checkout/merge would overwrite nd state changes
	// made by PM-Acceptor or other agents between branch switches.
	ensureGitignore(projectRoot)
}

// gitignoreEntries are paths that must be excluded from git tracking.
// These are runtime state files that change independently of code branches.
var gitignoreEntries = []string{
	".vault/issues/",
	".vault/.nd.yaml",
	".vault/.piv-loop-state.json",
	".vault/.piv-loop-snapshot.json",
	".vault/.dispatcher-state.json",
	".vault/.vlt.lock",
	".vault/.guard/",
}

// ensureGitignore appends missing entries to the project's .gitignore.
// Idempotent: skips entries that already exist.
func ensureGitignore(projectRoot string) {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")

	existing, _ := os.ReadFile(gitignorePath)
	content := string(existing)

	var toAdd []string
	for _, entry := range gitignoreEntries {
		if !containsLine(content, entry) {
			toAdd = append(toAdd, entry)
		}
	}

	if len(toAdd) == 0 {
		return
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	// Add a blank line separator if the file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}

	_, _ = f.WriteString("\n# Paivot runtime state (managed by pvg)\n")
	for _, entry := range toAdd {
		_, _ = f.WriteString(entry + "\n")
	}
}

// Off disables dispatcher mode by removing the state file.
func Off(projectRoot string) error {
	path := statePath(projectRoot)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Status prints the current dispatcher mode state to stdout.
func Status(projectRoot string) {
	state, err := ReadState(projectRoot)
	if err != nil {
		fmt.Println("Dispatcher mode: off (no state file)")
		return
	}
	if !state.Enabled {
		fmt.Println("Dispatcher mode: off")
		return
	}

	fmt.Printf("Dispatcher mode: on (since %s)\n", state.Since)
	if len(state.ActiveAgents) == 0 {
		fmt.Println("Active BLT agents: none")
	} else {
		fmt.Println("Active BLT agents:")
		for id, agentType := range state.ActiveAgents {
			fmt.Printf("  %s (%s)\n", id, agentType)
		}
	}
}

// ReadState reads the dispatcher state from disk.
// Returns an error if the file does not exist or cannot be parsed.
func ReadState(projectRoot string) (*State, error) {
	path, _, err := findStateFile(projectRoot)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse dispatcher state: %w", err)
	}
	if state.ActiveAgents == nil {
		state.ActiveAgents = make(map[string]string)
	}
	if state.AgentWorktrees == nil {
		state.AgentWorktrees = make(map[string]string)
	}
	return &state, nil
}

// ReadStateRoot reads dispatcher state and returns the project root that owns it.
// This lets subagent worktrees discover the orchestrator's shared dispatcher state.
func ReadStateRoot(projectRoot string) (*State, string, error) {
	path, root, err := findStateFile(projectRoot)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, "", fmt.Errorf("parse dispatcher state: %w", err)
	}
	if state.ActiveAgents == nil {
		state.ActiveAgents = make(map[string]string)
	}
	if state.AgentWorktrees == nil {
		state.AgentWorktrees = make(map[string]string)
	}
	return &state, root, nil
}

// TrackAgent adds a BLT agent to the active agents map.
func TrackAgent(projectRoot, agentID, agentType string) error {
	state, root, err := ReadStateRoot(projectRoot)
	if err != nil {
		// If no state file, dispatcher mode is off -- nothing to track
		return nil
	}
	if !state.Enabled {
		return nil
	}

	if state.ActiveAgents == nil {
		state.ActiveAgents = make(map[string]string)
	}
	if state.AgentWorktrees == nil {
		state.AgentWorktrees = make(map[string]string)
	}
	state.ActiveAgents[agentID] = agentType
	state.AgentWorktrees[agentID] = filepath.Clean(projectRoot)
	return writeState(root, *state)
}

// UntrackAgent removes a BLT agent from the active agents map.
func UntrackAgent(projectRoot, agentID string) error {
	state, root, err := ReadStateRoot(projectRoot)
	if err != nil {
		return nil
	}
	if !state.Enabled {
		return nil
	}

	delete(state.ActiveAgents, agentID)
	delete(state.AgentWorktrees, agentID)
	return writeState(root, *state)
}

// HasActiveBLTAgent returns true if any BLT agent is currently tracked.
func HasActiveBLTAgent(state *State) bool {
	return len(state.ActiveAgents) > 0
}

// HasActiveAgentType returns true if a specific BLT agent type is currently tracked.
func HasActiveAgentType(state *State, agentType string) bool {
	if state == nil {
		return false
	}
	for _, activeType := range state.ActiveAgents {
		if activeType == agentType {
			return true
		}
	}
	return false
}

// HasActiveAgentTypeAtPath returns true if the given agent type is active and
// its tracked worktree matches the caller path (or a child directory).
func HasActiveAgentTypeAtPath(state *State, agentType, cwd string) bool {
	if state == nil {
		return false
	}

	cleanCWD := filepath.Clean(cwd)
	for id, activeType := range state.ActiveAgents {
		if activeType != agentType {
			continue
		}
		worktree := filepath.Clean(state.AgentWorktrees[id])
		if worktree == "." || worktree == "" {
			continue
		}
		if cleanCWD == worktree || strings.HasPrefix(cleanCWD, worktree+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// StateFileName returns the state file basename (for guard exemption checks).
func StateFileName() string {
	return stateFile
}

func writeState(projectRoot string, state State) error {
	path := statePath(projectRoot)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dispatcher state: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

func findStateFile(start string) (path, root string, err error) {
	dir := filepath.Clean(start)
	for {
		candidate := statePath(dir)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", os.ErrNotExist
}

// containsLine checks if content has a line matching entry (trimmed).
func containsLine(content, entry string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}
