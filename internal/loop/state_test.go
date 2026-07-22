package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewState_Defaults(t *testing.T) {
	s := NewState("all", "", 50)
	if !s.Active {
		t.Error("expected active=true")
	}
	if s.Mode != "all" {
		t.Errorf("expected mode=all, got %s", s.Mode)
	}
	if s.TargetEpic != "" {
		t.Errorf("expected empty target_epic, got %s", s.TargetEpic)
	}
	if s.Iteration != 0 {
		t.Errorf("expected iteration=0, got %d", s.Iteration)
	}
	if s.MaxIterations != 50 {
		t.Errorf("expected max_iterations=50, got %d", s.MaxIterations)
	}
	if s.ConsecutiveWaits != 0 {
		t.Errorf("expected consecutive_waits=0, got %d", s.ConsecutiveWaits)
	}
	if s.MaxConsecutiveWaits != 3 {
		t.Errorf("expected max_consecutive_waits=3, got %d", s.MaxConsecutiveWaits)
	}
	if s.WaitIterations != 0 {
		t.Errorf("expected wait_iterations=0, got %d", s.WaitIterations)
	}
	if s.StartedAt == "" {
		t.Error("expected non-empty started_at")
	}
}

func TestNewState_EpicMode(t *testing.T) {
	s := NewState("epic", "PROJ-a1b", 0)
	if s.Mode != "epic" {
		t.Errorf("expected mode=epic, got %s", s.Mode)
	}
	if s.TargetEpic != "PROJ-a1b" {
		t.Errorf("expected target_epic=PROJ-a1b, got %s", s.TargetEpic)
	}
	if s.MaxIterations != 0 {
		t.Errorf("expected max_iterations=0 (unlimited), got %d", s.MaxIterations)
	}
}

func TestStateJSON_RoundTrip(t *testing.T) {
	original := &State{
		Active:              true,
		Mode:                "epic",
		TargetEpic:          "TEST-abc",
		AutoRotate:          true,
		CompletedEpics:      []string{"TEST-prev"},
		Iteration:           5,
		MaxIterations:       50,
		ConsecutiveWaits:    2,
		MaxConsecutiveWaits: 3,
		WaitIterations:      7,
		StartedAt:           "2026-02-27T10:00:00Z",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var restored State
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	if restored.Active != original.Active {
		t.Errorf("active mismatch: %v vs %v", restored.Active, original.Active)
	}
	if restored.Mode != original.Mode {
		t.Errorf("mode mismatch: %s vs %s", restored.Mode, original.Mode)
	}
	if restored.TargetEpic != original.TargetEpic {
		t.Errorf("target_epic mismatch: %s vs %s", restored.TargetEpic, original.TargetEpic)
	}
	if restored.Iteration != original.Iteration {
		t.Errorf("iteration mismatch: %d vs %d", restored.Iteration, original.Iteration)
	}
	if restored.MaxIterations != original.MaxIterations {
		t.Errorf("max_iterations mismatch: %d vs %d", restored.MaxIterations, original.MaxIterations)
	}
	if restored.ConsecutiveWaits != original.ConsecutiveWaits {
		t.Errorf("consecutive_waits mismatch: %d vs %d", restored.ConsecutiveWaits, original.ConsecutiveWaits)
	}
	if restored.WaitIterations != original.WaitIterations {
		t.Errorf("wait_iterations mismatch: %d vs %d", restored.WaitIterations, original.WaitIterations)
	}
	if restored.AutoRotate != original.AutoRotate {
		t.Errorf("auto_rotate mismatch: %v vs %v", restored.AutoRotate, original.AutoRotate)
	}
	if len(restored.CompletedEpics) != len(original.CompletedEpics) {
		t.Errorf("completed_epics length mismatch: %d vs %d", len(restored.CompletedEpics), len(original.CompletedEpics))
	}
}

func TestWriteState_ReadState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	original := NewState("all", "", 25)
	original.Iteration = 3

	if err := WriteState(dir, original); err != nil {
		t.Fatalf("WriteState() error: %v", err)
	}

	restored, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}

	if restored.Mode != original.Mode {
		t.Errorf("mode mismatch: %s vs %s", restored.Mode, original.Mode)
	}
	if restored.Iteration != original.Iteration {
		t.Errorf("iteration mismatch: %d vs %d", restored.Iteration, original.Iteration)
	}
}

func TestWriteState_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	// Don't pre-create .vault/
	state := NewState("all", "", 10)

	if err := WriteState(dir, state); err != nil {
		t.Fatalf("WriteState() should create directories: %v", err)
	}

	if _, err := os.Stat(StatePath(dir)); err != nil {
		t.Fatalf("state file should exist: %v", err)
	}
}

func TestRemoveState_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("all", "", 10)
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if err := RemoveState(dir); err != nil {
		t.Fatalf("RemoveState() error: %v", err)
	}

	if _, err := os.Stat(StatePath(dir)); !os.IsNotExist(err) {
		t.Error("expected state file to be removed")
	}
}

func TestRemoveState_NoopWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveState(dir); err != nil {
		t.Fatalf("RemoveState() with no state file should not error: %v", err)
	}
}

func TestIsActive_TrueWhenActive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("all", "", 10)
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if !IsActive(dir) {
		t.Error("expected IsActive=true")
	}
}

func TestIsActive_FalseWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	if IsActive(dir) {
		t.Error("expected IsActive=false when no state file")
	}
}

func TestIsActive_FalseWhenInactive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("all", "", 10)
	state.Active = false
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if IsActive(dir) {
		t.Error("expected IsActive=false when state.Active=false")
	}
}

func TestStatePath(t *testing.T) {
	got := StatePath("/project")
	want := filepath.Join("/project", ".vault", ".piv-loop-state.json")
	if got != want {
		t.Errorf("StatePath() = %s, want %s", got, want)
	}
}

func TestStateFileName_Value(t *testing.T) {
	if StateFileName() != ".piv-loop-state.json" {
		t.Errorf("unexpected state file name: %s", StateFileName())
	}
}

func TestReadStateRoot_FindsAncestorState(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "agent-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	state := NewState("all", "", 10)
	if err := WriteState(root, state); err != nil {
		t.Fatal(err)
	}

	restored, foundRoot, err := ReadStateRoot(worktree)
	if err != nil {
		t.Fatalf("ReadStateRoot() error: %v", err)
	}
	if foundRoot != root {
		t.Fatalf("expected root %s, got %s", root, foundRoot)
	}
	if !restored.Active {
		t.Fatal("expected restored state to be active")
	}
}

func TestIsActiveFrom_TrueWhenAncestorStateExists(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".claude", "worktrees", "agent-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	state := NewState("all", "", 10)
	if err := WriteState(root, state); err != nil {
		t.Fatal(err)
	}

	if !IsActiveFrom(worktree) {
		t.Fatal("expected nested worktree to detect ancestor loop state")
	}
}

func TestRotate_TransitionsEpic(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("epic", "EPIC-old", 0)
	state.AutoRotate = true
	state.ConsecutiveWaits = 2
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if err := Rotate(dir, "EPIC-new"); err != nil {
		t.Fatalf("Rotate() error: %v", err)
	}

	restored, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}

	if restored.TargetEpic != "EPIC-new" {
		t.Errorf("expected target_epic=EPIC-new, got %s", restored.TargetEpic)
	}
	if len(restored.CompletedEpics) != 1 || restored.CompletedEpics[0] != "EPIC-old" {
		t.Errorf("expected completed_epics=[EPIC-old], got %v", restored.CompletedEpics)
	}
	if restored.ConsecutiveWaits != 0 {
		t.Errorf("expected consecutive_waits reset to 0, got %d", restored.ConsecutiveWaits)
	}
	if !restored.Active {
		t.Error("expected loop to remain active after rotation")
	}
}

func TestRotate_AccumulatesCompletedEpics(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("epic", "EPIC-1", 0)
	state.CompletedEpics = []string{"EPIC-0"}
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if err := Rotate(dir, "EPIC-2"); err != nil {
		t.Fatalf("Rotate() error: %v", err)
	}

	restored, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}

	if len(restored.CompletedEpics) != 2 {
		t.Fatalf("expected 2 completed epics, got %d", len(restored.CompletedEpics))
	}
	if restored.CompletedEpics[0] != "EPIC-0" || restored.CompletedEpics[1] != "EPIC-1" {
		t.Errorf("expected completed_epics=[EPIC-0, EPIC-1], got %v", restored.CompletedEpics)
	}
	if restored.TargetEpic != "EPIC-2" {
		t.Errorf("expected target_epic=EPIC-2, got %s", restored.TargetEpic)
	}
}

func TestRotate_FailsWhenInactive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vault"), 0755); err != nil {
		t.Fatal(err)
	}

	state := NewState("epic", "EPIC-old", 0)
	state.Active = false
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	if err := Rotate(dir, "EPIC-new"); err == nil {
		t.Error("expected Rotate() to fail when loop is inactive")
	}
}

func TestRotate_FailsWhenNoState(t *testing.T) {
	dir := t.TempDir()
	if err := Rotate(dir, "EPIC-new"); err == nil {
		t.Error("expected Rotate() to fail when no state file exists")
	}
}

func TestValidAgentRole(t *testing.T) {
	valid := []string{AgentRoleDeveloper, AgentRolePM}
	for _, role := range valid {
		if !ValidAgentRole(role) {
			t.Errorf("ValidAgentRole(%q) = false, want true", role)
		}
	}
	invalid := []string{"", "pm_acceptor", "Developer", "PM", "anchor"}
	for _, role := range invalid {
		if ValidAgentRole(role) {
			t.Errorf("ValidAgentRole(%q) = true, want false", role)
		}
	}
}

func TestNewState_HasNoAgentHandles(t *testing.T) {
	s := NewState("epic", "PROJ-e", 50)
	if s.AgentHandles != nil {
		t.Errorf("fresh state must have no agent handles, got %v", s.AgentHandles)
	}
	if s.SessionID != "" {
		t.Errorf("fresh state must have empty session id, got %q", s.SessionID)
	}
}

func TestSetAgentHandle_RecordsAndResetsResumes(t *testing.T) {
	s := NewState("epic", "PROJ-e", 50)

	s.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-abc")
	h, ok := s.AgentHandleFor("PROJ-s1", AgentRoleDeveloper)
	if !ok || h.Handle != "agent-abc" || h.Resumes != 0 {
		t.Fatalf("expected handle agent-abc with 0 resumes, got %+v (ok=%v)", h, ok)
	}

	// Consume a resume, then overwrite: the counter must reset to 0.
	s.incrementAgentResume("PROJ-s1", AgentRoleDeveloper)
	s.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-new")
	h, _ = s.AgentHandleFor("PROJ-s1", AgentRoleDeveloper)
	if h.Handle != "agent-new" || h.Resumes != 0 {
		t.Fatalf("expected overwrite to reset resumes, got %+v", h)
	}

	// Roles are independent for the same story.
	s.SetAgentHandle("PROJ-s1", AgentRolePM, "agent-pm")
	if h, _ := s.AgentHandleFor("PROJ-s1", AgentRolePM); h.Handle != "agent-pm" {
		t.Fatalf("expected independent pm handle, got %+v", h)
	}
	if h, _ := s.AgentHandleFor("PROJ-s1", AgentRoleDeveloper); h.Handle != "agent-new" {
		t.Fatalf("pm set must not disturb developer handle, got %+v", h)
	}
}

func TestIncrementAgentResume_NoopWhenAbsent(t *testing.T) {
	s := NewState("epic", "PROJ-e", 50)
	s.incrementAgentResume("PROJ-none", AgentRoleDeveloper) // must not panic
	if s.AgentHandles != nil {
		t.Errorf("increment on absent entry must not create one, got %v", s.AgentHandles)
	}
}

func TestClearAgentHandles_RoleStoryAndIdempotency(t *testing.T) {
	s := NewState("epic", "PROJ-e", 50)
	s.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-dev")
	s.SetAgentHandle("PROJ-s1", AgentRolePM, "agent-pm")
	s.SetAgentHandle("PROJ-s2", AgentRoleDeveloper, "agent-other")

	// Clear a single role: the other role survives.
	s.ClearAgentHandles("PROJ-s1", AgentRolePM)
	if _, ok := s.AgentHandleFor("PROJ-s1", AgentRolePM); ok {
		t.Fatal("expected pm handle cleared")
	}
	if _, ok := s.AgentHandleFor("PROJ-s1", AgentRoleDeveloper); !ok {
		t.Fatal("expected developer handle preserved")
	}

	// Clear the whole story (role omitted): both roles gone.
	s.ClearAgentHandles("PROJ-s1", "")
	if _, ok := s.AgentHandleFor("PROJ-s1", AgentRoleDeveloper); ok {
		t.Fatal("expected all handles for PROJ-s1 cleared")
	}
	if _, ok := s.AgentHandleFor("PROJ-s2", AgentRoleDeveloper); !ok {
		t.Fatal("expected PROJ-s2 handle untouched")
	}

	// Idempotent: clearing absent entries must not panic or error.
	s.ClearAgentHandles("PROJ-s1", "")
	s.ClearAgentHandles("PROJ-s1", AgentRoleDeveloper)
	s.ClearAgentHandles("PROJ-missing", AgentRolePM)

	// Clearing the last story drops the map back to nil.
	s.ClearAgentHandles("PROJ-s2", AgentRoleDeveloper)
	if s.AgentHandles != nil {
		t.Errorf("expected nil map after clearing everything, got %v", s.AgentHandles)
	}
}

func TestClearAllAgentHandles_DropsEverything(t *testing.T) {
	s := NewState("epic", "PROJ-e", 50)
	s.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-dev")
	s.SetAgentHandle("PROJ-s2", AgentRolePM, "agent-pm")

	s.ClearAllAgentHandles()
	if s.AgentHandles != nil {
		t.Errorf("expected nil map after ClearAllAgentHandles, got %v", s.AgentHandles)
	}
}

func TestAgentHandles_StatePersistRoundTrip(t *testing.T) {
	dir := t.TempDir()

	original := NewState("epic", "PROJ-e", 50)
	original.SessionID = "sess-1"
	original.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-dev")
	original.SetAgentHandle("PROJ-s1", AgentRolePM, "agent-pm")
	original.incrementAgentResume("PROJ-s1", AgentRolePM)

	if err := WriteState(dir, original); err != nil {
		t.Fatalf("WriteState() error: %v", err)
	}
	restored, err := ReadState(dir)
	if err != nil {
		t.Fatalf("ReadState() error: %v", err)
	}

	if restored.SessionID != "sess-1" {
		t.Errorf("session_id mismatch: %q", restored.SessionID)
	}
	dev, ok := restored.AgentHandleFor("PROJ-s1", AgentRoleDeveloper)
	if !ok || dev.Handle != "agent-dev" || dev.Resumes != 0 {
		t.Errorf("developer handle mismatch: %+v (ok=%v)", dev, ok)
	}
	pm, ok := restored.AgentHandleFor("PROJ-s1", AgentRolePM)
	if !ok || pm.Handle != "agent-pm" || pm.Resumes != 1 {
		t.Errorf("pm handle mismatch: %+v (ok=%v)", pm, ok)
	}
}

func TestClearStoryAgentHandles_PersistsAndTolerant(t *testing.T) {
	dir := t.TempDir()

	// No state at all: silent no-op.
	ClearStoryAgentHandles(dir, "PROJ-s1")

	state := NewState("epic", "PROJ-e", 50)
	state.SetAgentHandle("PROJ-s1", AgentRoleDeveloper, "agent-dev")
	state.SetAgentHandle("PROJ-s2", AgentRolePM, "agent-pm")
	if err := WriteState(dir, state); err != nil {
		t.Fatal(err)
	}

	// Absent story: state on disk stays untouched.
	ClearStoryAgentHandles(dir, "PROJ-none")
	restored, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.AgentHandleFor("PROJ-s1", AgentRoleDeveloper); !ok {
		t.Fatal("clearing an absent story must not disturb recorded handles")
	}

	// Present story: cleared and persisted, other stories untouched.
	ClearStoryAgentHandles(dir, "PROJ-s1")
	restored, err = ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.AgentHandleFor("PROJ-s1", AgentRoleDeveloper); ok {
		t.Fatal("expected PROJ-s1 handles cleared on disk")
	}
	if _, ok := restored.AgentHandleFor("PROJ-s2", AgentRolePM); !ok {
		t.Fatal("expected PROJ-s2 handles preserved")
	}
}
