package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	vltSkillRepo    = "paivot-ai/vlt"
	vltSkillVersion = "v0.10.0"
	vltSkillSHA256  = "81444ce0b64b0d2cb1e9e437f31587afe99d1e95154443669b617a7a86cc69f9"
	vltSkillDestDir = ".claude/skills/vlt-skill"
	vltSkillMarker  = "SKILL.md"
)

// FetchVltSkill downloads and installs the vlt skill from GitHub.
// Skips if already installed unless force is true.
func FetchVltSkill(force bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	destDir := filepath.Join(home, vltSkillDestDir)
	markerPath := filepath.Join(destDir, vltSkillMarker)

	if !force {
		if _, err := os.Stat(markerPath); err == nil {
			fmt.Printf("vlt skill already installed at %s (use --force to update)\n", destDir)
			return nil
		}
	}

	// Download tarball
	tarballURL := fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.tar.gz", vltSkillRepo, vltSkillVersion)
	fmt.Printf("Fetching vlt skill from github.com/%s @ %s...\n", vltSkillRepo, vltSkillVersion)

	tmpDir, err := os.MkdirTemp("", "pvg-vlt-skill-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tarballPath := filepath.Join(tmpDir, "vlt.tar.gz")
	if err := downloadFile(tarballURL, tarballPath); err != nil {
		return fmt.Errorf("download failed: %w\n"+
			"       Check your internet connection or install the vlt skill manually:\n"+
			"       git clone https://github.com/%s.git && cd vlt && make install-skill", err, vltSkillRepo)
	}

	// Verify SHA256
	actual, err := fileSHA256(tarballPath)
	if err != nil {
		return fmt.Errorf("checksum computation failed: %w", err)
	}
	if actual != vltSkillSHA256 {
		return fmt.Errorf("SHA256 checksum mismatch!\n"+
			"  Expected: %s\n"+
			"  Got:      %s\n"+
			"  The downloaded file may have been tampered with", vltSkillSHA256, actual)
	}

	// Extract skill directory
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	stripPrefix := fmt.Sprintf("vlt-%s", vltSkillVersion[1:]) // strip leading 'v'
	stripComponent := fmt.Sprintf("%s/docs/vlt-skill", stripPrefix)

	cmd := exec.Command("tar", "xzf", tarballPath, "--strip-components=3", "-C", extractDir, stripComponent)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract tarball: %w\n%s", err, string(output))
	}

	// Install to destination
	if err := os.MkdirAll(filepath.Dir(destDir), 0755); err != nil {
		return fmt.Errorf("create skill parent dir: %w", err)
	}
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("remove old skill dir: %w", err)
	}
	if err := os.Rename(extractDir, destDir); err != nil {
		return fmt.Errorf("install skill: %w", err)
	}

	fmt.Printf("Installed vlt skill to %s (%s, verified)\n", destDir, vltSkillVersion)
	return nil
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url) // #nosec G107 -- URL is a hardcoded constant
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = io.Copy(f, resp.Body)
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
