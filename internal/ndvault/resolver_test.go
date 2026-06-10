package ndvault

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_PrefersSharedVaultForConfiguredWorktree(t *testing.T) {
	projectRoot, sharedVault := setupSharedWorktree(t)

	localVault := filepath.Join(projectRoot, ".vault")
	if err := os.MkdirAll(localVault, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(projectRoot)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Resolve() = %q, want %q", got, sharedVault)
	}
}

func TestResolve_FallsBackToNearestLocalVault(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "repo")
	nested := filepath.Join(projectRoot, "pkg", "service")

	if err := os.MkdirAll(filepath.Join(projectRoot, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(nested)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	want := filepath.Join(projectRoot, ".vault")
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	if got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolve_UsesEnvironmentOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Unsetenv("ND_VAULT_DIR")
	}()

	got, err := Resolve("/does/not/matter")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != override {
		t.Fatalf("Resolve() = %q, want %q", got, override)
	}
}

func TestResolve_PrefersSharedVaultFromNestedWorktree(t *testing.T) {
	projectRoot, sharedVault := setupSharedWorktree(t)

	worktreeRoot := filepath.Join(projectRoot, ".claude", "worktrees", "agent-1")
	worktreeGitDir := filepath.Join(filepath.Dir(sharedVault), "..", "worktrees", "agent-1")

	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	gitPtr := "gitdir: " + filepath.ToSlash(worktreeGitDir) + "\n"
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte(gitPtr), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(worktreeRoot)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Resolve() from nested worktree = %q, want %q", got, sharedVault)
	}
}

func TestResolve_PaivotManagedWithoutSharedVaultFallsBackToLocal(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "repo")

	// Create .vault/ with paivot markers (makes isPaivotManaged() true)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".vault", "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create .git directory (real repo, not a worktree)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Do NOT create .git/paivot/nd-vault -- this is the bug scenario:
	// isPaivotManaged returns true, gitCommonDir returns .git, but the
	// shared vault path doesn't exist. Should fall through to local .vault/.

	got, err := Resolve(projectRoot)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	want := filepath.Join(projectRoot, ".vault")
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	if got != want {
		t.Fatalf("Resolve() = %q, want %q (should fall through to local .vault)", got, want)
	}
}

// setupSiblingWorktree builds a main checkout with a .git dir plus a linked
// worktree OUTSIDE the main root, so upward config walks from the worktree
// cannot reach the main checkout's .vault.
func setupSiblingWorktree(t *testing.T) (mainRoot, worktreeRoot, sharedVault string) {
	t.Helper()

	base := t.TempDir()
	mainRoot = filepath.Join(base, "repo")
	worktreeRoot = filepath.Join(base, "wt")
	gitDir := filepath.Join(mainRoot, ".git")
	worktreeGitDir := filepath.Join(gitDir, "worktrees", "wt")
	sharedVault = filepath.Join(gitDir, sharedVaultRelPath)

	for _, dir := range []string{worktreeRoot, worktreeGitDir, filepath.Join(mainRoot, ".vault")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitPtr := "gitdir: " + filepath.ToSlash(worktreeGitDir) + "\n"
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte(gitPtr), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return mainRoot, worktreeRoot, sharedVault
}

func TestResolve_SiblingWorktreeFindsMainCheckoutConfig(t *testing.T) {
	mainRoot, worktreeRoot, sharedVault := setupSiblingWorktree(t)

	// Config exists ONLY in the main checkout (e.g. not yet committed to
	// the worktree's branch).
	if err := os.WriteFile(SharedConfigPath(mainRoot), []byte(DefaultSharedConfig()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(worktreeRoot)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Resolve() from sibling worktree = %q, want %q", got, sharedVault)
	}
}

func TestResolve_FallsBackToInitializedSharedVault(t *testing.T) {
	_, worktreeRoot, sharedVault := setupSiblingWorktree(t)

	// No config anywhere, but the shared vault is initialized: it is the
	// live source of record and must win over any local fallback.
	if err := os.MkdirAll(sharedVault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A local .vault in the worktree (tracked checkout content) must lose.
	if err := os.MkdirAll(filepath.Join(worktreeRoot, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(worktreeRoot)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Resolve() = %q, want initialized shared vault %q", got, sharedVault)
	}
}

func setupSharedWorktree(t *testing.T) (projectRoot, sharedVault string) {
	t.Helper()

	base := t.TempDir()
	projectRoot = filepath.Join(base, "repo")
	gitDir := filepath.Join(base, "gitdir", "worktrees", "story")
	commonDir := filepath.Join(base, "gitdir")
	sharedVault = filepath.Join(commonDir, sharedVaultRelPath)

	if err := os.MkdirAll(filepath.Join(projectRoot, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SharedConfigPath(projectRoot), []byte(DefaultSharedConfig()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedVault, 0o755); err != nil {
		t.Fatal(err)
	}

	gitPtr := "gitdir: " + filepath.ToSlash(gitDir) + "\n"
	if err := os.WriteFile(filepath.Join(projectRoot, ".git"), []byte(gitPtr), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return projectRoot, sharedVault
}
