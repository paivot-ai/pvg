package ndvault

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolve_UsesPaivotVaultOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-paivot-vault")
	if err := os.Setenv("PAIVOT_ND_VAULT", override); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Unsetenv("PAIVOT_ND_VAULT")
	}()

	got, err := Resolve("/does/not/matter")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got != override {
		t.Fatalf("Resolve() = %q, want %q", got, override)
	}
}

func TestEnsure_InitializesMissingVault(t *testing.T) {
	projectRoot, sharedVault := setupSharedWorktree(t)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	var calls [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if name != "nd" {
			t.Fatalf("unexpected command %q", name)
		}
		if len(args) != 3 || args[0] != "init" || args[1] != "--vault" || args[2] != sharedVault {
			t.Fatalf("unexpected nd args: %v", args)
		}
		if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
			t.Fatalf("write .nd.yaml: %v", err)
		}
		return exec.Command("sh", "-c", "exit 0")
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Ensure() = %q, want %q", got, sharedVault)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 nd init call, got %d", len(calls))
	}
}

func TestEnsure_SkipsInitWhenVaultAlreadyConfigured(t *testing.T) {
	projectRoot, sharedVault := setupSharedWorktree(t)
	if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("unexpected exec %s %v", name, args)
		return nil
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Ensure() = %q, want %q", got, sharedVault)
	}
}

func TestEnsure_WritesSharedConfigInGitRepo(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	sharedVault := filepath.Join(projectRoot, ".git", sharedVaultRelPath)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) != 3 || args[0] != "init" || args[1] != "--vault" || args[2] != sharedVault {
			t.Fatalf("unexpected nd args: %v", args)
		}
		if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
			t.Fatalf("write .nd.yaml: %v", err)
		}
		return exec.Command("sh", "-c", "exit 0")
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Ensure() = %q, want %q", got, sharedVault)
	}

	data, err := os.ReadFile(SharedConfigPath(projectRoot))
	if err != nil {
		t.Fatalf("shared config not written: %v", err)
	}
	if string(data) != DefaultSharedConfig() {
		t.Fatalf("shared config = %q, want %q", data, DefaultSharedConfig())
	}
}

func TestEnsure_MigratesLegacyLocalVault(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "repo")
	localVault := filepath.Join(projectRoot, ".vault")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(localVault, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localVault, ".nd.yaml"), []byte("vault: legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localVault, "issues", "TIX-1.md"), []byte("# TIX-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("nd init must not run when a legacy vault is migrated: %s %v", name, args)
		return nil
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}

	want := filepath.Join(projectRoot, ".git", sharedVaultRelPath)
	if got != want {
		t.Fatalf("Ensure() = %q, want %q", got, want)
	}

	migrated, err := os.ReadFile(filepath.Join(want, ".nd.yaml"))
	if err != nil {
		t.Fatalf(".nd.yaml not migrated: %v", err)
	}
	if string(migrated) != "vault: legacy\n" {
		t.Fatalf("migrated .nd.yaml = %q", migrated)
	}
	if _, err := os.Stat(filepath.Join(want, "issues", "TIX-1.md")); err != nil {
		t.Fatalf("issue not migrated: %v", err)
	}

	// The legacy marker must be decommissioned so no resolver (or hardcoded
	// `nd --vault .vault`) can silently target the stale store again.
	if _, err := os.Stat(filepath.Join(localVault, ".nd.yaml")); !os.IsNotExist(err) {
		t.Fatalf("legacy .nd.yaml not decommissioned (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(localVault, "issues", "TIX-1.md")); err != nil {
		t.Fatalf("legacy issue files must stay as inert history: %v", err)
	}
}

func TestEnsure_NonGitProjectKeepsLocalVault(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "proj")
	localVault := filepath.Join(projectRoot, ".vault")
	if err := os.MkdirAll(localVault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("unexpected exec %s %v", name, args)
		return nil
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != localVault {
		t.Fatalf("Ensure() = %q, want %q", got, localVault)
	}
	if _, err := os.Stat(SharedConfigPath(projectRoot)); !os.IsNotExist(err) {
		t.Fatalf("shared config must not be written outside a git repo (stat err = %v)", err)
	}
}

func TestEnsure_EnvOverrideSkipsSharedConfig(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	override := filepath.Join(t.TempDir(), "override-vault")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ND_VAULT_DIR", override)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("unexpected exec %s %v", name, args)
		return nil
	}

	got, err := Ensure(projectRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != override {
		t.Fatalf("Ensure() = %q, want %q", got, override)
	}
	if _, err := os.Stat(SharedConfigPath(projectRoot)); !os.IsNotExist(err) {
		t.Fatalf("shared config must not be written under env override (stat err = %v)", err)
	}
}

func TestEnsure_FromSiblingWorktreeWritesConfigAtMainRoot(t *testing.T) {
	mainRoot, worktreeRoot, sharedVault := setupSiblingWorktree(t)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
			t.Fatalf("write .nd.yaml: %v", err)
		}
		return exec.Command("sh", "-c", "exit 0")
	}

	got, err := Ensure(worktreeRoot)
	if err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	if got != sharedVault {
		t.Fatalf("Ensure() = %q, want %q", got, sharedVault)
	}

	// Config must land in the MAIN checkout, never inside the worktree
	// (an untracked file there would dirty it and be lost with cleanup).
	if _, err := os.Stat(SharedConfigPath(mainRoot)); err != nil {
		t.Fatalf("config not written at main root: %v", err)
	}
	if _, err := os.Stat(SharedConfigPath(worktreeRoot)); !os.IsNotExist(err) {
		t.Fatalf("config must not be written inside the worktree (stat err = %v)", err)
	}
}

func TestEnsure_ConcurrentCallsInitOnce(t *testing.T) {
	projectRoot, sharedVault := setupSharedWorktree(t)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	var mu sync.Mutex
	initCalls := 0
	execCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		initCalls++
		mu.Unlock()
		// Widen the race window before the vault becomes initialized.
		time.Sleep(100 * time.Millisecond)
		if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
			t.Errorf("write .nd.yaml: %v", err)
		}
		return exec.Command("sh", "-c", "exit 0")
	}

	const workers = 8
	results := make(chan error, workers)
	dirs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		go func() {
			dir, err := Ensure(projectRoot)
			dirs <- dir
			results <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Ensure() error: %v", err)
		}
		if dir := <-dirs; dir != sharedVault {
			t.Fatalf("concurrent Ensure() = %q, want %q", dir, sharedVault)
		}
	}

	if initCalls != 1 {
		t.Fatalf("nd init ran %d times, want exactly 1", initCalls)
	}
	if _, err := os.Stat(filepath.Join(sharedVault, ".init.lock")); !os.IsNotExist(err) {
		t.Fatalf("init lock not released (stat err = %v)", err)
	}
}

func TestEnsure_PropagatesInitFailure(t *testing.T) {
	projectRoot, _ := setupSharedWorktree(t)

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", fmt.Sprintf("echo 'boom' >&2; exit 3"))
	}

	_, err := Ensure(projectRoot)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Ensure() error = %v, want stderr included", err)
	}
}
