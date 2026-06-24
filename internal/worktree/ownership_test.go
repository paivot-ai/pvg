package worktree

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newMarkedRepo builds a real git repo on `main` with one commit and returns the
// repo root. Skips the test if git is unavailable.
func newMarkedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", root).CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}
	cmd := exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git commit failed: %s", out)
	}
	return root
}

// TestIsPaivotOwned_TrueAfterAdd verifies a worktree created via Add carries the
// ownership marker.
func TestIsPaivotOwned_TrueAfterAdd(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "story/OWN").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "dev-OWN")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if res := Add(root, wt, "story/OWN", "OWN"); res.Error != "" {
		t.Fatalf("Add failed: %s", res.Error)
	}
	if !IsPaivotOwned(wt) {
		t.Error("expected IsPaivotOwned=true after Add")
	}
}

// TestIsPaivotOwned_TrueAfterWriteOwnershipMarker verifies WriteOwnershipMarker
// marks an already-created worktree.
func TestIsPaivotOwned_TrueAfterWriteOwnershipMarker(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "story/MARK").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "dev-MARK")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	// Raw create: starts UNMARKED.
	if out, err := exec.Command("git", "-C", root, "worktree", "add", wt, "story/MARK").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}
	if IsPaivotOwned(wt) {
		t.Fatal("worktree should be unmarked before WriteOwnershipMarker")
	}
	if err := WriteOwnershipMarker(wt, "MARK"); err != nil {
		t.Fatalf("WriteOwnershipMarker: %v", err)
	}
	if !IsPaivotOwned(wt) {
		t.Error("expected IsPaivotOwned=true after WriteOwnershipMarker")
	}
}

// TestIsPaivotOwned_FalseForRawWorktreeAdd verifies a worktree created with raw
// `git worktree add` (no marker) is NOT owned -- even inside .claude/worktrees/.
func TestIsPaivotOwned_FalseForRawWorktreeAdd(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "feature/raw").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	// INSIDE .claude/worktrees/ to prove path is not what decides ownership.
	wt := filepath.Join(root, ".claude", "worktrees", "raw-session")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "add", wt, "feature/raw").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}
	if IsPaivotOwned(wt) {
		t.Error("expected IsPaivotOwned=false for a raw `git worktree add` worktree")
	}
}

// TestIsPaivotOwned_FalseForMissingDir verifies IsPaivotOwned fails CLOSED when
// the worktree directory does not exist (rev-parse fails -> false).
func TestIsPaivotOwned_FalseForMissingDir(t *testing.T) {
	if IsPaivotOwned("/nonexistent/path/to/worktree") {
		t.Error("expected IsPaivotOwned=false for a nonexistent path")
	}
}

// TestSafeRemove_RefusesUnmarkedInsideClaudeBase is the DECISIVE mechanism-layer
// regression: a worktree INSIDE .claude/worktrees/ that carries NO marker (a
// concurrent non-Paivot session created it) is REFUSED by SafeRemove. Path is
// not proof of ownership; the marker is.
func TestSafeRemove_RefusesUnmarkedInsideClaudeBase(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "feature/intruder").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "intruder")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	// Raw create inside the Paivot base -> UNMARKED.
	if out, err := exec.Command("git", "-C", root, "worktree", "add", wt, "feature/intruder").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	result := SafeRemove(wt)
	if result.Removed {
		t.Error("expected Removed=false for an unmarked worktree inside .claude/worktrees/")
	}
	if !contains(result.Error, "REFUSED") || !contains(result.Error, "ownership marker") {
		t.Errorf("expected an ownership-marker refusal, got %q", result.Error)
	}
	// The directory must survive.
	if !isDir(wt) {
		t.Error("unmarked worktree inside .claude/worktrees/ was deleted despite the refusal")
	}
}

// TestSafeRemoveForce_RemovesUnmarkedInsideClaudeBase verifies the escape hatch
// still works for an unmarked worktree inside the base.
func TestSafeRemoveForce_RemovesUnmarkedInsideClaudeBase(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "feature/forceit").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "force-unmarked")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "add", wt, "feature/forceit").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// Safe-by-default refuses it (no marker).
	if r := SafeRemove(wt); r.Removed || !contains(r.Error, "REFUSED") {
		t.Fatalf("SafeRemove should refuse the unmarked worktree; got %+v", r)
	}
	// Force removes it.
	result := SafeRemoveForce(wt)
	if result.Error != "" {
		t.Fatalf("SafeRemoveForce error: %s", result.Error)
	}
	if !result.Removed {
		t.Error("expected Removed=true with SafeRemoveForce")
	}
	if isDir(wt) {
		t.Error("unmarked worktree still exists after SafeRemoveForce")
	}
}

// TestAdd_WritesParseableMarker verifies the marker stamped by Add is valid JSON
// with the expected owner and story id, and lives in the worktree's git admin
// dir.
func TestAdd_WritesParseableMarker(t *testing.T) {
	root := newMarkedRepo(t)
	if out, err := exec.Command("git", "-C", root, "branch", "story/PARSE").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "dev-PARSE")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	res := Add(root, wt, "story/PARSE", "PARSE")
	if res.Error != "" {
		t.Fatalf("Add failed: %s", res.Error)
	}
	if !res.Added {
		t.Fatal("expected Added=true")
	}
	if res.MarkerPath == "" {
		t.Fatal("expected a non-empty MarkerPath in the result")
	}

	data, err := os.ReadFile(res.MarkerPath)
	if err != nil {
		t.Fatalf("read marker %q: %v", res.MarkerPath, err)
	}
	var marker OwnershipMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("marker is not parseable JSON: %v (content: %s)", err, data)
	}
	if marker.Owner != "paivot" {
		t.Errorf("marker owner = %q, want %q", marker.Owner, "paivot")
	}
	if marker.StoryID != "PARSE" {
		t.Errorf("marker story_id = %q, want %q", marker.StoryID, "PARSE")
	}
	if marker.CreatedAt == "" {
		t.Error("marker created_at is empty")
	}
	// The marker must live under the worktree's git admin dir.
	adminDir, err := resolveAdminDir(wt)
	if err != nil {
		t.Fatalf("resolveAdminDir: %v", err)
	}
	if filepath.Dir(res.MarkerPath) != adminDir {
		t.Errorf("marker dir = %q, want admin dir %q", filepath.Dir(res.MarkerPath), adminDir)
	}
}

// TestAdd_FailsWithoutMarkerWhenGitAddFails verifies that when `git worktree
// add` fails (e.g. nonexistent branch), Add returns the error and writes no
// marker.
func TestAdd_FailsWithoutMarkerWhenGitAddFails(t *testing.T) {
	root := newMarkedRepo(t)
	wt := filepath.Join(root, ".claude", "worktrees", "dev-NOPE")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	// Branch does not exist -> git worktree add fails.
	res := Add(root, wt, "story/DOES-NOT-EXIST", "NOPE")
	if res.Added {
		t.Error("expected Added=false when git worktree add fails")
	}
	if res.Error == "" {
		t.Error("expected a non-empty error when git worktree add fails")
	}
	if isDir(wt) {
		t.Error("worktree dir should not exist after a failed Add")
	}
}
