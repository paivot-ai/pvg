package ndvault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sharedVaultRelPath = "paivot/nd-vault"
const sharedConfigRelPath = ".vault/.nd-shared.yaml"

// Resolve returns the nd vault path for a project root.
//
// In repos configured for shared worktree state, the live nd vault is
// branch-independent and lives under the repository's git common dir. Repos
// without shared state fall back to the nearest local .vault directory.
func Resolve(projectRoot string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("ND_VAULT_DIR")); override != "" {
		return filepath.Clean(override), nil
	}
	if override := strings.TrimSpace(os.Getenv("PAIVOT_ND_VAULT")); override != "" {
		return filepath.Clean(override), nil
	}

	if projectRoot == "" {
		return "", fmt.Errorf("project root is required")
	}

	projectRoot = filepath.Clean(projectRoot)

	// Shared vault when explicitly configured via .nd-shared.yaml.
	if shared, err := resolveSharedVaultDir(projectRoot); err == nil {
		return shared, nil
	}

	// Convergence fallbacks for linked worktrees and stale branches whose
	// checkout does not (yet) carry the tracked config. Without these,
	// concurrent agents resolve different vault views and nd writes
	// diverge across loop iterations.
	if commonDir, err := gitCommonDir(projectRoot); err == nil {
		// The main checkout may hold the config even when this worktree
		// branched before it was committed.
		if filepath.Base(commonDir) == ".git" {
			mainConfig := SharedConfigPath(filepath.Dir(commonDir))
			if _, statErr := os.Stat(mainConfig); statErr == nil {
				if mode, relPath, perr := parseSharedConfig(mainConfig); perr == nil && mode == "git_common_dir" {
					return filepath.Join(commonDir, relPath), nil
				}
			}
		}
		// Or the shared vault may already be initialized even when no
		// visible checkout carries the config.
		shared := filepath.Join(commonDir, sharedVaultRelPath)
		if _, statErr := os.Stat(filepath.Join(shared, ".nd.yaml")); statErr == nil {
			return shared, nil
		}
	}

	// Default: local .vault/ directory.
	local, err := nearestLocalVault(projectRoot)
	if err != nil {
		return "", err
	}
	return local, nil
}

// SharedConfigPath returns the repository-local config file that tells nd-aware
// tools how to resolve a shared live vault from git-common-dir.
func SharedConfigPath(projectRoot string) string {
	return filepath.Join(filepath.Clean(projectRoot), sharedConfigRelPath)
}

// SharedConfigured reports whether projectRoot, an ancestor, or the main
// checkout of its git repository carries a shared-vault config.
func SharedConfigured(projectRoot string) bool {
	projectRoot = filepath.Clean(projectRoot)
	if _, _, err := findSharedConfig(projectRoot); err == nil {
		return true
	}
	if commonDir, err := gitCommonDir(projectRoot); err == nil && filepath.Base(commonDir) == ".git" {
		if _, err := os.Stat(SharedConfigPath(filepath.Dir(commonDir))); err == nil {
			return true
		}
	}
	return false
}

// FindRepoRoot returns the nearest ancestor of start containing .git.
func FindRepoRoot(start string) (string, error) {
	return findRepoRoot(start)
}

// DefaultSharedConfig returns the tracked config that points to the default
// shared worktree nd vault used by paivot-graph.
func DefaultSharedConfig() string {
	return "# nd shared-worktree state\nmode: git_common_dir\npath: " + sharedVaultRelPath + "\n"
}

func resolveSharedVaultDir(start string) (string, error) {
	configPath, root, err := findSharedConfig(start)
	if err != nil {
		return "", err
	}

	mode, relPath, err := parseSharedConfig(configPath)
	if err != nil {
		return "", err
	}
	if mode != "git_common_dir" {
		return "", fmt.Errorf("unsupported shared nd mode %q", mode)
	}

	commonDir, err := gitCommonDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(commonDir, relPath), nil
}

func findSharedConfig(start string) (path, root string, err error) {
	dir := filepath.Clean(start)
	for {
		candidate := SharedConfigPath(dir)
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

func parseSharedConfig(path string) (mode, relPath string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "mode":
			mode = value
		case "path":
			relPath = filepath.Clean(value)
		}
	}

	if mode == "" || relPath == "" {
		return "", "", fmt.Errorf("invalid shared nd config %s", path)
	}
	if filepath.IsAbs(relPath) || relPath == "." || relPath == "" {
		return "", "", fmt.Errorf("invalid shared nd path %q", relPath)
	}
	return mode, relPath, nil
}

// IsPaivotManaged reports whether projectRoot (or an ancestor) contains
// paivot-specific vault artifacts such as knowledge settings, dispatcher
// state, or loop state.
func IsPaivotManaged(projectRoot string) bool { return isPaivotManaged(projectRoot) }

func isPaivotManaged(projectRoot string) bool {
	dir := filepath.Clean(projectRoot)
	for {
		candidates := []string{
			filepath.Join(dir, ".vault", "knowledge", ".settings.yaml"),
			filepath.Join(dir, ".vault", "knowledge"),
			filepath.Join(dir, ".vault", ".dispatcher-state.json"),
			filepath.Join(dir, ".vault", ".piv-loop-state.json"),
		}

		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false
}

func nearestLocalVault(start string) (string, error) {
	dir := filepath.Clean(start)
	for {
		candidate := filepath.Join(dir, ".vault")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("could not find local .vault from %s", start)
}

func gitCommonDir(start string) (string, error) {
	repoRoot, err := findRepoRoot(start)
	if err != nil {
		return "", err
	}

	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", gitPath, err)
	}

	if info.IsDir() {
		return filepath.Clean(gitPath), nil
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", gitPath, err)
	}

	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("%s does not contain a gitdir pointer", gitPath)
	}

	gitDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoRoot, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	commonDirPath := filepath.Join(gitDir, "commondir")
	if data, err := os.ReadFile(commonDirPath); err == nil {
		commonDir := strings.TrimSpace(string(data))
		if commonDir != "" {
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(gitDir, commonDir)
			}
			return filepath.Clean(commonDir), nil
		}
	}

	return gitDir, nil
}

func findRepoRoot(start string) (string, error) {
	dir := filepath.Clean(start)
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("could not find git repo from %s", start)
}
