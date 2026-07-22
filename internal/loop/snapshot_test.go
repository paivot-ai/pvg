package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseWorktreePorcelain_Empty(t *testing.T) {
	result := parseWorktreePorcelain("")
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d worktrees", len(result))
	}
}

func TestParseWorktreePorcelain_MainOnly(t *testing.T) {
	input := `worktree /Users/dev/project
HEAD abc1234567890
branch refs/heads/main
`
	result := parseWorktreePorcelain(input)
	if len(result) != 0 {
		t.Errorf("expected no worktrees (main skipped), got %d", len(result))
	}
}

func TestParseWorktreePorcelain_TwoWorktrees(t *testing.T) {
	input := `worktree /Users/dev/project
HEAD abc1234567890
branch refs/heads/main

worktree /Users/dev/project/.claude/worktrees/agent-abc
HEAD def4567890123
branch refs/heads/worktree-agent-abc
`
	result := parseWorktreePorcelain(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(result))
	}
	if result[0].Path != "/Users/dev/project/.claude/worktrees/agent-abc" {
		t.Errorf("unexpected path: %s", result[0].Path)
	}
	if result[0].Branch != "worktree-agent-abc" {
		t.Errorf("unexpected branch: %s", result[0].Branch)
	}
}

func TestParseWorktreePorcelain_ThreeWorktrees(t *testing.T) {
	input := `worktree /project
HEAD aaa
branch refs/heads/main

worktree /project/.claude/worktrees/agent-1
HEAD bbb
branch refs/heads/worktree-agent-1

worktree /project/.claude/worktrees/agent-2
HEAD ccc
branch refs/heads/worktree-agent-2
`
	result := parseWorktreePorcelain(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(result))
	}
	if result[0].Path != "/project/.claude/worktrees/agent-1" {
		t.Errorf("unexpected path[0]: %s", result[0].Path)
	}
	if result[1].Path != "/project/.claude/worktrees/agent-2" {
		t.Errorf("unexpected path[1]: %s", result[1].Path)
	}
}

func TestParseWorktreePorcelain_DetachedHead(t *testing.T) {
	input := `worktree /project
HEAD aaa
branch refs/heads/main

worktree /project/.claude/worktrees/detached
HEAD bbb
detached
`
	result := parseWorktreePorcelain(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(result))
	}
	if result[0].Path != "/project/.claude/worktrees/detached" {
		t.Errorf("unexpected path: %s", result[0].Path)
	}
	if result[0].Branch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %s", result[0].Branch)
	}
}

func TestParseWorktreePorcelain_NoTrailingNewline(t *testing.T) {
	input := `worktree /project
HEAD aaa
branch refs/heads/main

worktree /project/.claude/worktrees/agent-x
HEAD bbb
branch refs/heads/worktree-agent-x`
	result := parseWorktreePorcelain(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(result))
	}
	if result[0].Branch != "worktree-agent-x" {
		t.Errorf("unexpected branch: %s", result[0].Branch)
	}
}

func TestWriteSnapshot_ReadSnapshot_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	original := &Snapshot{
		TakenAt: "2026-03-02T10:00:00Z",
		Stories: []SnapshotEntry{
			{
				StoryID:      "PROJ-a1b",
				NDStatus:     "in_progress",
				NDLabels:     []string{"delivered"},
				AgentType:    "pm-acceptor",
				WorktreePath: "/tmp/wt1",
				BranchName:   "worktree-agent-a1b",
			},
			{
				StoryID:  "PROJ-c3d",
				NDStatus: "in_progress",
			},
		},
	}

	if err := WriteSnapshot(dir, original); err != nil {
		t.Fatalf("WriteSnapshot() error: %v", err)
	}

	restored, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatalf("ReadSnapshot() error: %v", err)
	}

	if restored.TakenAt != original.TakenAt {
		t.Errorf("TakenAt mismatch: %s vs %s", restored.TakenAt, original.TakenAt)
	}
	if len(restored.Stories) != len(original.Stories) {
		t.Fatalf("Stories length mismatch: %d vs %d", len(restored.Stories), len(original.Stories))
	}
	if restored.Stories[0].StoryID != "PROJ-a1b" {
		t.Errorf("StoryID mismatch: %s", restored.Stories[0].StoryID)
	}
	if restored.Stories[0].AgentType != "pm-acceptor" {
		t.Errorf("AgentType mismatch: %s", restored.Stories[0].AgentType)
	}
	if len(restored.Stories[0].NDLabels) != 1 || restored.Stories[0].NDLabels[0] != "delivered" {
		t.Errorf("NDLabels mismatch: %v", restored.Stories[0].NDLabels)
	}
}

func TestWriteSnapshot_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	// Don't pre-create .vault/
	snap := &Snapshot{TakenAt: "2026-03-02T10:00:00Z"}

	if err := WriteSnapshot(dir, snap); err != nil {
		t.Fatalf("WriteSnapshot() should create directories: %v", err)
	}

	if _, err := os.Stat(SnapshotPath(dir)); err != nil {
		t.Fatalf("snapshot file should exist: %v", err)
	}
}

func TestReadSnapshot_ErrorWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSnapshot(dir)
	if err == nil {
		t.Error("expected error when snapshot file does not exist")
	}
}

func TestRemoveSnapshot_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// First remove: file doesn't exist -- should be no-op
	if err := RemoveSnapshot(dir); err != nil {
		t.Fatalf("RemoveSnapshot() on missing file should not error: %v", err)
	}

	// Write then remove
	snap := &Snapshot{TakenAt: "2026-03-02T10:00:00Z"}
	if err := WriteSnapshot(dir, snap); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSnapshot(dir); err != nil {
		t.Fatalf("RemoveSnapshot() error: %v", err)
	}
	if _, err := os.Stat(SnapshotPath(dir)); !os.IsNotExist(err) {
		t.Error("expected snapshot file to be removed")
	}

	// Second remove: file already gone -- should be no-op
	if err := RemoveSnapshot(dir); err != nil {
		t.Fatalf("RemoveSnapshot() second call should not error: %v", err)
	}
}

func TestSnapshotPath(t *testing.T) {
	got := SnapshotPath("/project")
	want := filepath.Join("/project", ".vault", ".piv-loop-snapshot.json")
	if got != want {
		t.Errorf("SnapshotPath() = %s, want %s", got, want)
	}
}

func TestSnapshotFileName_Value(t *testing.T) {
	if SnapshotFileName() != ".piv-loop-snapshot.json" {
		t.Errorf("unexpected snapshot file name: %s", SnapshotFileName())
	}
}

// EpicBranchExists must see local AND remote-tracking epic refs: a session
// that fetched an unmerged epic branch without checking it out locally still
// owes the completion gate.
func TestEpicBranchExists_LocalAndRemoteRefs(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")

	if EpicBranchExists(root, "PROJ-e1") {
		t.Fatal("expected false with no epic refs at all")
	}

	run("branch", "epic/PROJ-e1")
	if !EpicBranchExists(root, "PROJ-e1") {
		t.Fatal("expected true for local epic branch")
	}

	// Remote-only: delete the local ref, plant a remote-tracking one.
	run("branch", "-D", "epic/PROJ-e1")
	run("update-ref", "refs/remotes/origin/epic/PROJ-e1", "HEAD")
	if !EpicBranchExists(root, "PROJ-e1") {
		t.Fatal("expected true for remote-tracking epic ref (origin/epic/PROJ-e1)")
	}

	if EpicBranchExists(root, "") {
		t.Fatal("expected false for empty epic id")
	}
}
