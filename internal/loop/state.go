// Package loop manages the piv-loop execution loop state.
//
// When a piv-loop is active, the stop hook intercepts session exit and
// evaluates whether to continue (emit continuation JSON) or allow exit.
// State is persisted in .vault/.piv-loop-state.json.
package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateFile = ".piv-loop-state.json"

// State represents the persistent loop execution state.
type State struct {
	Active              bool     `json:"active"`
	Mode                string   `json:"mode"`                  // "all" or "epic"
	TargetEpic          string   `json:"target_epic,omitempty"` // epic ID when mode=epic
	AutoRotate          bool     `json:"auto_rotate"`           // true: rotate to next epic on completion
	CompletedEpics      []string `json:"completed_epics,omitempty"`
	Iteration           int      `json:"iteration"`
	MaxIterations       int      `json:"max_iterations"` // 0 = unlimited
	ConsecutiveWaits    int      `json:"consecutive_waits"`
	MaxConsecutiveWaits int      `json:"max_consecutive_waits"`
	WaitIterations      int      `json:"wait_iterations"`
	StartedAt           string   `json:"started_at"`
	// LastStopSignature is the work-state signature recorded at the previous
	// stop evaluation (ComputeWorkSignature). EvaluateStop resets the
	// consecutive-wait counter when the current signature differs.
	LastStopSignature string `json:"last_stop_signature,omitempty"`
	// WaitStorySet / WaitStoryStreak track stalled-claim detection for
	// `pvg loop next`: the comma-joined sorted in_progress story-id set seen
	// at the last wait decision, and how many consecutive wait evaluations
	// observed that identical set (see DetectStall).
	WaitStorySet    string `json:"wait_story_set,omitempty"`
	WaitStoryStreak int    `json:"wait_story_streak,omitempty"`
	// DispatcherEnabledBySetup records that `pvg loop setup` was what enabled
	// dispatcher mode for this loop. `pvg loop cancel` then restores the
	// pre-loop posture by disabling it; a dispatcher the user enabled
	// independently is left on.
	DispatcherEnabledBySetup bool `json:"dispatcher_enabled_by_setup,omitempty"`
	// SessionID is the Claude Code session id stamped by the session-start
	// hook. Subagent resume is same-session only, so a different id arriving
	// on a fresh session boundary (source startup/clear) invalidates every
	// recorded agent handle below.
	SessionID string `json:"session_id,omitempty"`
	// AgentHandles records the semi-persistent story-agent handles the
	// dispatcher reported back via `pvg loop agent set`, keyed by story ID
	// then role (AgentRoleDeveloper | AgentRolePM). `pvg loop next` surfaces
	// them as resume hints on rework and re-review actions.
	AgentHandles map[string]map[string]AgentHandle `json:"agent_handles,omitempty"`
}

// Story-agent handle roles. The pm_review action role "pm_acceptor" maps to
// AgentRolePM; developer_rework/developer_new map to AgentRoleDeveloper.
const (
	AgentRoleDeveloper = "developer"
	AgentRolePM        = "pm"
)

// ValidAgentRole reports whether role is a recordable story-agent role.
func ValidAgentRole(role string) bool {
	return role == AgentRoleDeveloper || role == AgentRolePM
}

// AgentHandle records one semi-persistent subagent for a story+role: the
// opaque resume handle the dispatcher reported back, and how many resumes
// have been consumed against it. Handles are same-session only.
type AgentHandle struct {
	Handle  string `json:"handle"`
	Resumes int    `json:"resumes"`
}

// SetAgentHandle records (or overwrites) the handle for a story+role and
// resets its resume counter: a fresh handle has consumed no resumes.
func (s *State) SetAgentHandle(storyID, role, handle string) {
	if s.AgentHandles == nil {
		s.AgentHandles = make(map[string]map[string]AgentHandle)
	}
	if s.AgentHandles[storyID] == nil {
		s.AgentHandles[storyID] = make(map[string]AgentHandle)
	}
	s.AgentHandles[storyID][role] = AgentHandle{Handle: handle}
}

// AgentHandleFor returns the recorded handle for a story+role.
func (s *State) AgentHandleFor(storyID, role string) (AgentHandle, bool) {
	h, ok := s.AgentHandles[storyID][role]
	return h, ok
}

// ClearAgentHandles removes the recorded handle for a story+role, or every
// role for the story when role is empty. Clearing absent entries is a no-op
// by design: the dispatcher clears unconditionally (e.g. on story accept).
func (s *State) ClearAgentHandles(storyID, role string) {
	roles, ok := s.AgentHandles[storyID]
	if !ok {
		return
	}
	if role == "" {
		delete(s.AgentHandles, storyID)
	} else {
		delete(roles, role)
		if len(roles) == 0 {
			delete(s.AgentHandles, storyID)
		}
	}
	if len(s.AgentHandles) == 0 {
		s.AgentHandles = nil
	}
}

// ClearAllAgentHandles drops every recorded handle. Used at session
// boundaries: a new session id means every handle is stale.
func (s *State) ClearAllAgentHandles() {
	s.AgentHandles = nil
}

// incrementAgentResume consumes one resume for a recorded story+role handle.
// No-op when nothing is recorded.
func (s *State) incrementAgentResume(storyID, role string) {
	if h, ok := s.AgentHandles[storyID][role]; ok {
		h.Resumes++
		s.AgentHandles[storyID][role] = h
	}
}

// ClearStoryAgentHandles removes every recorded agent handle for one story
// from the active loop state and persists it. Best-effort by design: no loop
// state, an inactive loop, or an absent entry are all silent no-ops.
func ClearStoryAgentHandles(projectRoot, storyID string) {
	state, root, err := ReadStateRoot(projectRoot)
	if err != nil || !state.Active {
		return
	}
	if _, ok := state.AgentHandles[storyID]; !ok {
		return
	}
	state.ClearAgentHandles(storyID, "")
	_ = WriteState(root, state)
}

// NewState creates a new loop state with sensible defaults.
func NewState(mode, epic string, maxIter int) *State {
	return &State{
		Active:              true,
		Mode:                mode,
		TargetEpic:          epic,
		Iteration:           0,
		MaxIterations:       maxIter,
		ConsecutiveWaits:    0,
		MaxConsecutiveWaits: 3,
		WaitIterations:      0,
		StartedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

// StatePath returns the full path to the loop state file.
func StatePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vault", stateFile)
}

// StateFileName returns the state file basename (for guard exemption checks).
func StateFileName() string {
	return stateFile
}

// ReadState reads the loop state from disk.
func ReadState(projectRoot string) (*State, error) {
	path := StatePath(projectRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse loop state: %w", err)
	}
	return &state, nil
}

// ReadStateRoot reads loop state from the nearest ancestor project root and
// returns both the state and the root directory that owns it.
func ReadStateRoot(start string) (*State, string, error) {
	path, root, err := findStateFile(start)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, "", fmt.Errorf("parse loop state: %w", err)
	}
	return &state, root, nil
}

// WriteState persists the loop state to disk.
func WriteState(projectRoot string, state *State) error {
	path := StatePath(projectRoot)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal loop state: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// RemoveState deletes the loop state file. No-op if it doesn't exist.
func RemoveState(projectRoot string) error {
	path := StatePath(projectRoot)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsActive checks whether a loop is currently active for the project.
func IsActive(projectRoot string) bool {
	state, err := ReadState(projectRoot)
	if err != nil {
		return false
	}
	return state.Active
}

// Rotate transitions the loop from the current epic to the next one.
// It appends the current epic to CompletedEpics, sets TargetEpic to nextEpic,
// and resets the wait counters for the new epic.
func Rotate(projectRoot, nextEpic string) error {
	state, err := ReadState(projectRoot)
	if err != nil {
		return fmt.Errorf("read loop state: %w", err)
	}
	if !state.Active {
		return fmt.Errorf("no active loop to rotate")
	}
	if state.TargetEpic != "" {
		state.CompletedEpics = append(state.CompletedEpics, state.TargetEpic)
	}
	state.TargetEpic = nextEpic
	state.ConsecutiveWaits = 0
	return WriteState(projectRoot, state)
}

// IsActiveFrom checks for an active loop state in the caller directory or any
// ancestor directory. This lets nested worktrees reuse the orchestrator state.
func IsActiveFrom(start string) bool {
	state, _, err := ReadStateRoot(start)
	if err != nil {
		return false
	}
	return state.Active
}

func findStateFile(start string) (path, root string, err error) {
	dir := filepath.Clean(start)
	for {
		candidate := StatePath(dir)
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
