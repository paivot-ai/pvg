package ndvault

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var execCommand = exec.Command

// Init-lock tuning, overridable in tests.
var (
	initLockTimeout = 60 * time.Second
	initLockStale   = 2 * time.Minute
)

// Ensure resolves the live nd vault for a repo and initializes it if needed.
//
// In a git repository without .vault/.nd-shared.yaml, Ensure writes that
// config first (at the main checkout root, never inside a linked worktree)
// so every worktree resolves the same git-common-dir vault. Without it, the
// dispatcher (main checkout) and developers (linked worktrees) resolve
// different vault views and nd writes diverge. A legacy local live vault
// (.vault/.nd.yaml plus issues/*.md) is migrated into the shared location
// so an existing backlog survives the switch. First-time initialization is
// serialized across concurrent pvg processes with an on-disk lock.
func Ensure(projectRoot string) (string, error) {
	if !envOverrideActive() && projectRoot != "" {
		if err := ensureSharedConfig(filepath.Clean(projectRoot)); err != nil {
			return "", err
		}
	}

	vaultDir, err := Resolve(projectRoot)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		return "", fmt.Errorf("create nd vault %s: %w", vaultDir, err)
	}

	configPath := filepath.Join(vaultDir, ".nd.yaml")
	if _, err := os.Stat(configPath); err == nil {
		return vaultDir, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", configPath, err)
	}

	err = withInitLock(vaultDir, func() error {
		// Re-check under the lock: another process may have initialized
		// the vault while we waited.
		if _, err := os.Stat(configPath); err == nil {
			return nil
		}
		migrated, err := migrateLegacyVault(projectRoot, vaultDir)
		if err != nil || migrated {
			return err
		}
		return runNDInit(vaultDir)
	})
	if err != nil {
		return "", err
	}
	return vaultDir, nil
}

func runNDInit(vaultDir string) error {
	cmd := execCommand("nd", "init", "--vault", vaultDir)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Tolerate losing a race with a process that does not take the
		// lock (bare nd init): an initialized vault is success.
		if _, statErr := os.Stat(filepath.Join(vaultDir, ".nd.yaml")); statErr == nil {
			return nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("nd init --vault %s: %s", vaultDir, msg)
		}
		return fmt.Errorf("nd init --vault %s: %w", vaultDir, err)
	}
	return nil
}

// withInitLock serializes first-time vault initialization across concurrent
// pvg processes (dispatcher, developers, PM agents). The lock lives inside
// the vault directory, is stolen when stale (holder crashed), and waiting
// callers re-run fn so they can observe the winner's result.
func withInitLock(vaultDir string, fn func() error) error {
	lockPath := filepath.Join(vaultDir, ".init.lock")
	deadline := time.Now().Add(initLockTimeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			runErr := fn()
			_ = os.Remove(lockPath)
			return runErr
		}
		if !os.IsExist(err) {
			return fmt.Errorf("acquire init lock %s: %w", lockPath, err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > initLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for nd vault init lock %s", lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func envOverrideActive() bool {
	return strings.TrimSpace(os.Getenv("ND_VAULT_DIR")) != "" ||
		strings.TrimSpace(os.Getenv("PAIVOT_ND_VAULT")) != ""
}

// ensureSharedConfig writes the tracked shared-vault config when the git
// repository does not have one yet. The config always lands at the main
// checkout root (parent of the git common dir): writing it inside a linked
// worktree would dirty the worktree and be lost with it. Non-git projects
// keep the local .vault fallback; git_common_dir mode is meaningless
// without git.
func ensureSharedConfig(projectRoot string) error {
	if SharedConfigured(projectRoot) {
		return nil
	}
	commonDir, err := gitCommonDir(projectRoot)
	if err != nil {
		return nil
	}
	if filepath.Base(commonDir) != ".git" {
		// Cannot locate a main checkout (bare repo). Resolve's
		// initialized-vault fallback still converges all readers.
		return nil
	}
	configPath := SharedConfigPath(filepath.Dir(commonDir))
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	if err := writeFileAtomic(configPath, []byte(DefaultSharedConfig()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// migrateLegacyVault copies a pre-shared-mode live vault (.nd.yaml plus
// issues/*.md in the nearest local .vault) into the shared vault. Issues
// are copied first and .nd.yaml last: .nd.yaml is the initialized marker,
// so a crash mid-migration leaves the vault uninitialized and the next
// Ensure retries. The local copies stay in place: they may be git-tracked,
// and with the shared config present they are never resolved again.
func migrateLegacyVault(projectRoot, vaultDir string) (bool, error) {
	local, err := nearestLocalVault(projectRoot)
	if err != nil || filepath.Clean(local) == filepath.Clean(vaultDir) {
		return false, nil
	}

	localConfig := filepath.Join(local, ".nd.yaml")
	if _, err := os.Stat(localConfig); err != nil {
		return false, nil
	}

	entries, err := os.ReadDir(filepath.Join(local, "issues"))
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read legacy issues: %w", err)
	}
	if len(entries) > 0 {
		issuesDir := filepath.Join(vaultDir, "issues")
		if err := os.MkdirAll(issuesDir, 0o755); err != nil {
			return false, fmt.Errorf("create %s: %w", issuesDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			src := filepath.Join(local, "issues", entry.Name())
			if err := copyVaultFile(src, filepath.Join(issuesDir, entry.Name())); err != nil {
				return false, fmt.Errorf("migrate issue %s: %w", entry.Name(), err)
			}
		}
	}

	if err := copyVaultFile(localConfig, filepath.Join(vaultDir, ".nd.yaml")); err != nil {
		return false, fmt.Errorf("migrate .nd.yaml: %w", err)
	}
	return true, nil
}

func copyVaultFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, data, 0o644)
}

// writeFileAtomic writes via a same-directory temp file and rename so
// concurrent readers never observe a partially written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
