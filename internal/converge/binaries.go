package converge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Overridable for tests.
var (
	githubBase  = "https://github.com"
	execCommand = exec.Command
	lookPath    = exec.LookPath
	userHomeDir = os.UserHomeDir
	selfPath    = os.Executable
	httpClient  = &http.Client{Timeout: 5 * time.Minute}

	toolVersionOutput = func(name string, args ...string) (string, error) {
		out, err := execCommand(name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	dirWritable = func(dir string) bool {
		f, err := os.CreateTemp(dir, ".pvg-writable-*")
		if err != nil {
			return false
		}
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return true
	}
	sudoNonInteractive = func() bool {
		return execCommand("sudo", "-n", "true").Run() == nil
	}
)

// SelfVersion is the running pvg's version, set from main.version at
// startup. Used instead of exec'ing pvg to learn its own version.
var SelfVersion = ""

// installBinary downloads the release tarball for tool@tag from repo,
// verifies it against the release checksums.txt, and installs the binary
// into target.Dir. The final step is always a same-directory rename, which
// is atomic on unix and safe even when the old binary (including the
// running pvg) is in use.
func installBinary(tool, repo, tag string, target installTarget) error {
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", tool, version, runtime.GOOS, runtime.GOARCH)
	assetURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", githubBase, repo, tag, asset)
	sumsURL := fmt.Sprintf("%s/%s/releases/download/%s/checksums.txt", githubBase, repo, tag)

	tmpDir, err := os.MkdirTemp("", "pvg-converge-"+tool+"-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tarballPath := filepath.Join(tmpDir, asset)
	if err := downloadFile(assetURL, tarballPath); err != nil {
		return fmt.Errorf("download %s: %w", assetURL, err)
	}
	sumsPath := filepath.Join(tmpDir, "checksums.txt")
	if err := downloadFile(sumsURL, sumsPath); err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}

	expected, err := checksumFor(sumsPath, asset)
	if err != nil {
		return err
	}
	actual, err := fileSHA256(tarballPath)
	if err != nil {
		return fmt.Errorf("checksum computation failed: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("SHA256 mismatch for %s: expected %s, got %s", asset, expected, actual)
	}

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	cmd := execCommand("tar", "xzf", tarballPath, "-C", extractDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract tarball: %w\n%s", err, string(output))
	}

	extracted := filepath.Join(extractDir, tool)
	if _, err := os.Stat(extracted); err != nil {
		return fmt.Errorf("binary %s not found in release tarball", tool)
	}

	if target.Sudo {
		return sudoInstall(extracted, target.Dir, tool)
	}
	return plainInstall(extracted, target.Dir, tool)
}

// plainInstall stages the binary next to its destination and renames over
// it. Same-directory rename is atomic on unix, so replacing the running pvg
// binary is safe.
func plainInstall(src, destDir, tool string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}
	staged := filepath.Join(destDir, fmt.Sprintf(".%s.pvg-new-%d", tool, os.Getpid()))
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read extracted binary: %w", err)
	}
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		return fmt.Errorf("stage binary: %w", err)
	}
	if err := os.Rename(staged, filepath.Join(destDir, tool)); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

// sudoInstall is plainInstall through passwordless sudo: cp to a staging
// file inside destDir, chmod, then a same-directory mv (rename, atomic).
func sudoInstall(src, destDir, tool string) error {
	staged := filepath.Join(destDir, fmt.Sprintf(".%s.pvg-new-%d", tool, os.Getpid()))
	final := filepath.Join(destDir, tool)
	steps := [][]string{
		{"sudo", "-n", "mkdir", "-p", destDir},
		{"sudo", "-n", "cp", src, staged},
		{"sudo", "-n", "chmod", "0755", staged},
		{"sudo", "-n", "mv", "-f", staged, final},
	}
	for _, argv := range steps {
		cmd := execCommand(argv[0], argv[1:]...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, string(output))
		}
	}
	return nil
}

// downloadFile fetches a URL to a local path. A GITHUB_TOKEN environment
// variable, when set, is sent as a bearer token; net/http strips the
// Authorization header automatically on the cross-host redirect GitHub
// release downloads use.
func downloadFile(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil) // #nosec G107 -- URL built from package constants plus manifest pins
	if err != nil {
		return err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
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

// checksumFor finds the sha256 for an asset name in a goreleaser
// checksums.txt ("<sha256>  <filename>" lines).
func checksumFor(sumsPath, asset string) (string, error) {
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", asset)
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
