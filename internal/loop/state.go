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
