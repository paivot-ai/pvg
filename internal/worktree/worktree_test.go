package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveProjectRoot_ClaudeWorktreesConvention(t *testing.T) {
	// Create a temp dir that mimics the Paivot worktree layout:
	//   /tmp/xxx/.git/              (marks project root)
	//   /tmp/xxx/.claude/worktrees/dev-STORY-a1b/  (worktree dir)
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(root, ".claude", "worktrees", "dev-STORY-a1b")
	if err := os.MkdirAll(wtPath, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveProjectRoot(wtPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Errorf("ResolveProjectRoot(%q) = %q, want %q", wtPath, got, root)
	}
}

func TestResolveProjectRoot_NoConvention_NoGit(t *testing.T) {
	// A random path with no .claude/worktrees/ and no git -- should fail.
	dir := t.TempDir()
	_, err := ResolveProjectRoot(dir)
	if err == nil {
		t.Error("expected error for path with no convention and no git, got nil")
	}
}

func TestResolveProjectRoot_NestedConvention(t *testing.T) {
	// Ensure it picks the right root even with nested .claude dirs.
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Deeply nested worktree path
	wtPath := filepath.Join(root, ".claude", "worktrees", "dev-PRA-nqys")
	if err := os.MkdirAll(wtPath, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveProjectRoot(wtPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Errorf("got %q, want %q", got, root)
	}
}

func TestSafeRemove_NonexistentWorktree(t *testing.T) {
	// SafeRemove on a path that doesn't exist and has no .claude convention.
	result := SafeRemove("/nonexistent/path/to/worktree")
	if result.Removed {
		t.Error("expected Removed=false for nonexistent path")
	}
	if result.Error == "" {
		t.Error("expected non-empty Error for nonexistent path")
	}
}

func TestSafeRemove_StaleWorktree(t *testing.T) {
	// If the worktree directory is gone but metadata remains, SafeRemove
	// should fall back to prune. We test the ResolveProjectRoot part here
	// since full git worktree integration requires a real git repo.
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Worktree path follows convention but directory does NOT exist.
	wtPath := filepath.Join(root, ".claude", "worktrees", "dev-GONE")

	resolved, err := ResolveProjectRoot(wtPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != root {
		t.Errorf("got %q, want %q", resolved, root)
	}
}

func TestRemoveResult_FormatText(t *testing.T) {
	r := RemoveResult{
		Removed:      true,
		WorktreePath: "/repo/.claude/worktrees/dev-X",
		ProjectRoot:  "/repo",
		Pruned:       true,
	}
	text := r.FormatText()
	if text == "" {
		t.Error("expected non-empty text")
	}
	if !contains(text, "Removed") {
		t.Errorf("expected 'Removed' in text, got %q", text)
	}
	if !contains(text, "[pruned]") {
		t.Errorf("expected '[pruned]' in text, got %q", text)
	}
}

func TestRemoveResult_FormatText_Error(t *testing.T) {
	r := RemoveResult{
		Error: "something went wrong",
	}
	text := r.FormatText()
	if !contains(text, "FAIL") {
		t.Errorf("expected 'FAIL' in error text, got %q", text)
	}
}

func TestRemoveResult_FormatJSON(t *testing.T) {
	r := RemoveResult{
		Removed:      true,
		WorktreePath: "/repo/.claude/worktrees/dev-X",
		ProjectRoot:  "/repo",
	}
	j := r.FormatJSON()
	if j == "" {
		t.Error("expected non-empty JSON")
	}
	if !contains(j, `"removed": true`) {
		t.Errorf("expected removed:true in JSON, got %s", j)
	}
}

// Integration test: only runs when a real git repo is available.
func TestSafeRemove_Integration(t *testing.T) {
	// Create a real git repo with a worktree.
	root := t.TempDir()

	// Restore original execCommand after test
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	// Init git repo
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}

	// Need at least one commit for worktrees to work.
	cmd = exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git commit failed: %s", out)
	}

	// Create a branch for the worktree
	cmd = exec.Command("git", "-C", root, "branch", "story/TEST-123")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}

	// Create the worktree directory structure (Paivot convention)
	wtDir := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(wtDir, "dev-TEST-123")
	cmd = exec.Command("git", "-C", root, "worktree", "add", wtPath, "story/TEST-123")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// Verify the worktree exists
	if !isDir(wtPath) {
		t.Fatalf("worktree dir does not exist at %s", wtPath)
	}

	// SafeRemove should work even if we cd somewhere else.
	result := SafeRemove(wtPath)
	if result.Error != "" {
		t.Fatalf("SafeRemove error: %s", result.Error)
	}
	if !result.Removed {
		t.Error("expected Removed=true")
	}
	if result.ProjectRoot != root {
		t.Errorf("ProjectRoot = %q, want %q", result.ProjectRoot, root)
	}

	// Verify the worktree is gone
	if isDir(wtPath) {
		t.Error("worktree dir still exists after removal")
	}
}

// TestSafeRemove_RefusesCwdInside verifies the CWD safety guard:
// if the caller's CWD is inside the worktree, removal is refused.
func TestSafeRemove_RefusesCwdInside(t *testing.T) {
	root := t.TempDir()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	// Init git repo
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}

	// Need at least one commit for worktrees to work.
	cmd = exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git commit failed: %s", out)
	}

	// Create a branch for the worktree
	cmd = exec.Command("git", "-C", root, "branch", "story/CWD-GUARD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}

	// Create the worktree directory structure (Paivot convention)
	wtDir := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(wtDir, "dev-CWD-GUARD")
	cmd = exec.Command("git", "-C", root, "worktree", "add", wtPath, "story/CWD-GUARD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// Verify the worktree exists
	if !isDir(wtPath) {
		t.Fatalf("worktree dir does not exist at %s", wtPath)
	}

	// Save original CWD and restore it after the test.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origCwd) })

	// Move into the worktree directory
	if err := os.Chdir(wtPath); err != nil {
		t.Fatalf("os.Chdir(%s): %v", wtPath, err)
	}

	// SafeRemove should refuse since CWD is inside the worktree.
	result := SafeRemove(wtPath)
	if result.Removed {
		t.Error("expected Removed=false when CWD is inside worktree")
	}
	if !contains(result.Error, "REFUSED") {
		t.Errorf("expected error to contain 'REFUSED', got %q", result.Error)
	}

	// Verify the worktree directory was NOT deleted.
	if !isDir(wtPath) {
		t.Error("worktree dir was deleted despite CWD being inside it")
	}
}

// TestSafeRemove_AllowsRemovalFromOutside confirms the CWD guard does not
// block removal when CWD is outside the worktree.
func TestSafeRemove_AllowsRemovalFromOutside(t *testing.T) {
	root := t.TempDir()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	// Init git repo
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}

	// Need at least one commit for worktrees to work.
	cmd = exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git commit failed: %s", out)
	}

	// Create a branch for the worktree
	cmd = exec.Command("git", "-C", root, "branch", "story/OUTSIDE")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}

	// Create the worktree directory structure (Paivot convention)
	wtDir := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(wtDir, "dev-OUTSIDE")
	cmd = exec.Command("git", "-C", root, "worktree", "add", wtPath, "story/OUTSIDE")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	if !isDir(wtPath) {
		t.Fatalf("worktree dir does not exist at %s", wtPath)
	}

	// Confirm CWD is outside the worktree before calling SafeRemove.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	wtAbs, _ := filepath.Abs(wtPath)
	cwdClean := filepath.Clean(cwd)
	wtClean := filepath.Clean(wtAbs)
	if cwdClean == wtClean || contains(cwdClean, wtClean+string(filepath.Separator)) {
		t.Fatalf("CWD %q is unexpectedly inside worktree %q", cwdClean, wtClean)
	}

	// SafeRemove should succeed since CWD is outside.
	result := SafeRemove(wtPath)
	if result.Error != "" {
		t.Fatalf("SafeRemove error: %s", result.Error)
	}
	if !result.Removed {
		t.Error("expected Removed=true when CWD is outside worktree")
	}
	if result.ProjectRoot != root {
		t.Errorf("ProjectRoot = %q, want %q", result.ProjectRoot, root)
	}

	// Verify the worktree is gone
	if isDir(wtPath) {
		t.Error("worktree dir still exists after removal")
	}
}

// TestSafeRemove_RefusesCwdInsideRelativePath verifies the CWD safety guard
// works correctly when SafeRemove is called with a RELATIVE path from the
// project root, while CWD has drifted into the worktree itself. This is the
// exact failure mode that caused session-fatal CWD corruption: filepath.Abs()
// resolves the relative path using the drifted CWD, producing a double-nested
// wrong path that fails to match, silently allowing the removal.
func TestSafeRemove_RefusesCwdInsideRelativePath(t *testing.T) {
	root := t.TempDir()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	// Init git repo
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}

	// Need at least one commit for worktrees to work.
	cmd = exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git commit failed: %s", out)
	}

	// Create a branch for the worktree
	cmd = exec.Command("git", "-C", root, "branch", "story/REL-PATH")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}

	// Create the worktree directory structure (Paivot convention)
	wtDir := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(wtDir, "dev-REL-PATH")
	cmd = exec.Command("git", "-C", root, "worktree", "add", wtPath, "story/REL-PATH")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// Verify the worktree exists
	if !isDir(wtPath) {
		t.Fatalf("worktree dir does not exist at %s", wtPath)
	}

	// Save original CWD and restore it after the test.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origCwd) })

	// Move into the worktree directory (simulates CWD drift)
	if err := os.Chdir(wtPath); err != nil {
		t.Fatalf("os.Chdir(%s): %v", wtPath, err)
	}

	// Compute the RELATIVE path from project root -- this is what the
	// dispatcher passes to SafeRemove in the real failure case.
	relPath, err := filepath.Rel(root, wtPath)
	if err != nil {
		t.Fatalf("filepath.Rel(%s, %s): %v", root, wtPath, err)
	}

	// SafeRemove with relative path should refuse since CWD is inside the worktree.
	result := SafeRemove(relPath)
	if result.Removed {
		t.Error("expected Removed=false when CWD is inside worktree (relative path)")
	}
	if !contains(result.Error, "REFUSED") {
		t.Errorf("expected error to contain 'REFUSED', got %q", result.Error)
	}

	// Verify the worktree directory was NOT deleted.
	if !isDir(wtPath) {
		t.Error("worktree dir was deleted despite CWD being inside it (relative path)")
	}
}

// TestSafeRemove_PermissionDeniedActionableError verifies that when git fails
// to unlink root-owned container artifacts, SafeRemove returns an actionable
// error naming the contract and remediation rather than the bare git error.
func TestSafeRemove_PermissionDeniedActionableError(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}
	wtPath := filepath.Join(root, ".claude", "worktrees", "dev-ROOTOWNED")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	// Mock the `git worktree remove` by re-exec'ing this test binary as a
	// helper that emits a permission-denied error and exits non-zero. No
	// shell is involved, so there is no command-injection surface.
	execCommand = func(name string, args ...string) *exec.Cmd {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "worktree" && args[i+1] == "remove" {
				c := exec.Command(os.Args[0], "-test.run=TestHelperGitPermDenied")
				c.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
				return c
			}
		}
		return exec.Command("git", "--version")
	}

	result := SafeRemove(wtPath)
	if result.Removed {
		t.Fatalf("expected Removed=false on permission failure, got: %+v", result)
	}
	for _, want := range []string{"Permission denied", "root-owned", "chown", "worktree prune"} {
		if !contains(result.Error, want) {
			t.Errorf("error missing %q; got: %s", want, result.Error)
		}
	}
}

// TestSafeRemove_RefusesOutsideOwnedBase verifies the ownership guard: a
// worktree OUTSIDE Paivot's owned base (e.g. .codex-worktrees/) is refused, and
// the refusal happens BEFORE any `git worktree remove` is ever invoked. This is
// the mechanism-layer defense against deleting worktrees Paivot does not own.
func TestSafeRemove_RefusesOutsideOwnedBase(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Skipf("git init failed (git not available?): %s", out)
	}

	// A real foreign worktree directory outside .claude/worktrees. It must be a
	// real dir so ResolveProjectRoot can use git resolution (Strategy 2) and
	// succeed in finding the project root.
	foreignWT := filepath.Join(root, ".codex-worktrees", "feature-x")
	if err := os.MkdirAll(foreignWT, 0o755); err != nil {
		t.Fatal(err)
	}

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	// Mock execCommand: allow the git resolution call(s) that ResolveProjectRoot
	// needs, but FAIL the test loudly if `git worktree remove` is ever reached --
	// the ownership refusal must short-circuit before any removal.
	var removeCalled bool
	execCommand = func(name string, args ...string) *exec.Cmd {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "worktree" && args[i+1] == "remove" {
				removeCalled = true
			}
		}
		return exec.Command(name, args...)
	}

	result := SafeRemove(foreignWT)

	if removeCalled {
		t.Fatal("git worktree remove was invoked on a foreign worktree (ownership guard failed to short-circuit)")
	}
	if result.Removed {
		t.Errorf("expected Removed=false for a worktree outside the owned base")
	}
	if !contains(result.Error, "REFUSED") {
		t.Errorf("expected error to contain 'REFUSED', got %q", result.Error)
	}
	if !contains(result.Error, "managed worktree base") {
		t.Errorf("expected error to name the managed base, got %q", result.Error)
	}
	// The foreign worktree directory must NOT have been touched.
	if !isDir(foreignWT) {
		t.Error("foreign worktree directory was removed despite the refusal")
	}
}

// TestSafeRemoveForce_BypassesOwnershipGuard verifies the emergency escape
// hatch: SafeRemoveForce removes a worktree outside the owned base (the CWD
// guard still applies, but ownership does not).
func TestSafeRemoveForce_BypassesOwnershipGuard(t *testing.T) {
	root := t.TempDir()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
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

	// Create a worktree OUTSIDE .claude/worktrees on a branch.
	if out, err := exec.Command("git", "-C", root, "branch", "feature/force").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	foreignWT := filepath.Join(root, ".codex-worktrees", "force-target")
	if err := os.MkdirAll(filepath.Dir(foreignWT), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "add", foreignWT, "feature/force").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// Safe-by-default refuses it.
	if r := SafeRemove(foreignWT); r.Removed || !contains(r.Error, "REFUSED") {
		t.Fatalf("SafeRemove should refuse foreign worktree; got %+v", r)
	}

	// Force removes it.
	result := SafeRemoveForce(foreignWT)
	if result.Error != "" {
		t.Fatalf("SafeRemoveForce error: %s", result.Error)
	}
	if !result.Removed {
		t.Error("expected Removed=true with SafeRemoveForce")
	}
	if isDir(foreignWT) {
		t.Error("foreign worktree still exists after SafeRemoveForce")
	}
}

// TestSafeRemove_AllowsCustomBaseFromSetting verifies the worktree.base setting
// overrides the default owned base, so a non-default base (e.g. the codex
// variant's .codex-worktrees) is treated as owned.
func TestSafeRemove_AllowsCustomBaseFromSetting(t *testing.T) {
	root := t.TempDir()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
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

	// Write a settings file pointing the owned base at .codex-worktrees.
	settingsDir := filepath.Join(root, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, ".settings.yaml"),
		[]byte("worktree.base: .codex-worktrees\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command("git", "-C", root, "branch", "feature/owned").CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %s", out)
	}
	wt := filepath.Join(root, ".codex-worktrees", "dev-owned")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "add", wt, "feature/owned").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %s", out)
	}

	// With the setting, .codex-worktrees is OWNED -> SafeRemove succeeds.
	result := SafeRemove(wt)
	if result.Error != "" {
		t.Fatalf("SafeRemove should succeed for the configured base; got error: %s", result.Error)
	}
	if !result.Removed {
		t.Error("expected Removed=true for a worktree under the configured base")
	}
}

// TestHelperGitPermDenied is not a real test: when run as a helper subprocess
// (GO_WANT_HELPER_PROCESS=1) it mimics git refusing to unlink root-owned files.
func TestHelperGitPermDenied(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Stderr.WriteString("error: failed to delete: Permission denied\n")
	os.Exit(1)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
