package ndvault

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var execCommand = exec.Command

// Ensure resolves the live nd vault for a repo and initializes it if needed.
//
// In a git repository without .vault/.nd-shared.yaml, Ensure writes that
// config first so every worktree resolves the same git-common-dir vault.
// Without it, the dispatcher (main checkout) and developers (linked
// worktrees) resolve different vault views and nd writes diverge. A legacy
// local live vault (.vault/.nd.yaml plus issues/*.md) is migrated into the
// shared location so an existing backlog survives the switch.
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

	migrated, err := migrateLegacyVault(projectRoot, vaultDir)
	if err != nil {
		return "", err
	}
	if migrated {
		return vaultDir, nil
	}

	cmd := execCommand("nd", "init", "--vault", vaultDir)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("nd init --vault %s: %s", vaultDir, msg)
		}
		return "", fmt.Errorf("nd init --vault %s: %w", vaultDir, err)
	}

	return vaultDir, nil
}

func envOverrideActive() bool {
	return strings.TrimSpace(os.Getenv("ND_VAULT_DIR")) != "" ||
		strings.TrimSpace(os.Getenv("PAIVOT_ND_VAULT")) != ""
}

// ensureSharedConfig writes the tracked shared-vault config at the git repo
// root when the project does not have one yet. Non-git projects keep the
// local .vault fallback; git_common_dir mode is meaningless without git.
func ensureSharedConfig(projectRoot string) error {
	if _, _, err := findSharedConfig(projectRoot); err == nil {
		return nil
	}
	repoRoot, err := findRepoRoot(projectRoot)
	if err != nil {
		return nil
	}
	configPath := SharedConfigPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, []byte(DefaultSharedConfig()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// migrateLegacyVault copies a pre-shared-mode live vault (.nd.yaml plus
// issues/*.md in the nearest local .vault) into the shared vault. The local
// copies stay in place: they may be git-tracked, and with the shared config
// present they are never resolved again.
func migrateLegacyVault(projectRoot, vaultDir string) (bool, error) {
	local, err := nearestLocalVault(projectRoot)
	if err != nil || filepath.Clean(local) == filepath.Clean(vaultDir) {
		return false, nil
	}

	localConfig := filepath.Join(local, ".nd.yaml")
	if _, err := os.Stat(localConfig); err != nil {
		return false, nil
	}
	if err := copyVaultFile(localConfig, filepath.Join(vaultDir, ".nd.yaml")); err != nil {
		return false, fmt.Errorf("migrate .nd.yaml: %w", err)
	}

	entries, err := os.ReadDir(filepath.Join(local, "issues"))
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read legacy issues: %w", err)
	}

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
	return true, nil
}

func copyVaultFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
