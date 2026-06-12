package converge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	vltSkillDirName    = "vlt-skill"
	skillVersionMarker = ".paivot-skill-version"
)

// vltSkillState returns the installed vlt skill version marker ("" when the
// skill or marker is absent).
func vltSkillState() string {
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "skills", vltSkillDirName, skillVersionMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// installVltSkill downloads the vlt source tarball at the pinned tag and
// installs docs/vlt-skill into ~/.claude/skills/vlt-skill, stamping the tag
// in a version marker so convergence is idempotent.
func installVltSkill(repo, tag string) error {
	home, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	srcURL := fmt.Sprintf("%s/%s/archive/refs/tags/%s.tar.gz", githubBase, repo, tag)
	tmpDir, err := os.MkdirTemp("", "pvg-vlt-skill-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tarballPath := filepath.Join(tmpDir, "src.tar.gz")
	if err := downloadFile(srcURL, tarballPath); err != nil {
		return fmt.Errorf("download %s: %w", srcURL, err)
	}

	version := strings.TrimPrefix(tag, "v")
	repoBase := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repoBase = repo[i+1:]
	}
	skillRel := fmt.Sprintf("%s-%s/docs/vlt-skill", repoBase, version)

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	depth := strings.Count(skillRel, "/") + 1
	cmd := execCommand("tar", "xzf", tarballPath,
		fmt.Sprintf("--strip-components=%d", depth), "-C", extractDir, skillRel)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract %s: %w\n%s", skillRel, err, string(output))
	}

	dest := filepath.Join(home, ".claude", "skills", vltSkillDirName)
	if err := replaceDir(extractDir, dest); err != nil {
		return err
	}
	marker := filepath.Join(dest, skillVersionMarker)
	if err := os.WriteFile(marker, []byte(tag+"\n"), 0o644); err != nil {
		return fmt.Errorf("write skill version marker: %w", err)
	}
	return nil
}

// replaceDir swaps dest with the contents of src.
func replaceDir(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("remove old %s: %w", dest, err)
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	// Rename can fail across filesystems (temp dir on another volume); fall
	// back to a recursive copy.
	if err := os.CopyFS(dest, os.DirFS(src)); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	return nil
}
