package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// toolSpec describes a companion tool released on GitHub whose binary and
// Claude skill paivot-graph depends on.
type toolSpec struct {
	Name        string
	Repo        string   // owner/name on github.com
	VersionArgs []string // argv that prints the installed version
}

var toolSpecs = []toolSpec{
	{Name: "vlt", Repo: "paivot-ai/vlt", VersionArgs: []string{"version"}},
	{Name: "nd", Repo: "paivot-ai/nd", VersionArgs: []string{"--version"}},
}

// Overridable for tests.
var (
	githubAPIBase = "https://api.github.com"
	githubBase    = "https://github.com"
	lookPath      = exec.LookPath
	toolVersion   = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
)

const skillVersionMarker = ".paivot-skill-version"

// FetchTools installs or updates the vlt and nd binaries plus their Claude
// skills to the latest GitHub release. With force, everything is reinstalled
// even when already current. A tool that is installed but cannot be checked
// against GitHub (offline, rate limit) is left alone with a warning.
func FetchTools(force bool) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("fetch-tools does not support windows; install vlt and nd manually")
	}

	var failures []string
	for _, spec := range toolSpecs {
		if err := fetchTool(spec, force); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", spec.Name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("fetch-tools: %s", strings.Join(failures, "; "))
	}
	return nil
}

func fetchTool(spec toolSpec, force bool) error {
	binPath, lookErr := lookPath(spec.Name)
	installed := lookErr == nil

	tag, err := latestReleaseTag(spec.Repo)
	if err != nil {
		if installed {
			fmt.Printf("WARN: %s: cannot check latest release (%v) -- keeping installed version\n", spec.Name, err)
			return nil
		}
		return fmt.Errorf("not installed and latest release lookup failed: %w", err)
	}

	// Release binaries print the version without the leading "v" while
	// source builds stamp the v-prefixed git tag; match the bare version.
	binaryCurrent := false
	if installed && !force {
		if out, verr := toolVersion(binPath, spec.VersionArgs...); verr == nil && strings.Contains(out, strings.TrimPrefix(tag, "v")) {
			binaryCurrent = true
		}
	}

	if binaryCurrent {
		fmt.Printf("OK: %s %s is current\n", spec.Name, tag)
	} else {
		dest := filepath.Dir(binPath)
		if !installed {
			home, herr := os.UserHomeDir()
			if herr != nil {
				return fmt.Errorf("cannot determine home directory: %w", herr)
			}
			dest = filepath.Join(home, "go", "bin")
		}
		if err := installToolBinary(spec, tag, dest); err != nil {
			return err
		}
		fmt.Printf("OK: %s %s installed to %s\n", spec.Name, tag, dest)
	}

	if !force && binaryCurrent && skillCurrent(spec, tag) {
		fmt.Printf("OK: %s skill %s is current\n", spec.Name, tag)
		return nil
	}
	if err := installToolSkill(spec, tag); err != nil {
		return fmt.Errorf("skill install: %w", err)
	}
	return nil
}

// latestReleaseTag asks the GitHub API for the latest release tag of a repo.
func latestReleaseTag(repo string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, repo)
	resp, err := http.Get(url) // #nosec G107 -- base is a package constant, repo a fixed spec
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode release JSON: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("release has no tag_name")
	}
	return release.TagName, nil
}

// installToolBinary downloads the release tarball for the host platform,
// verifies it against the release checksums.txt, and installs the binary
// into destDir with an atomic rename.
func installToolBinary(spec toolSpec, tag, destDir string) error {
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", spec.Name, version, runtime.GOOS, runtime.GOARCH)
	assetURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", githubBase, spec.Repo, tag, asset)
	sumsURL := fmt.Sprintf("%s/%s/releases/download/%s/checksums.txt", githubBase, spec.Repo, tag)

	tmpDir, err := os.MkdirTemp("", "pvg-fetch-"+spec.Name+"-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tarballPath := filepath.Join(tmpDir, asset)
	fmt.Printf("Fetching %s %s (%s)...\n", spec.Name, tag, asset)
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
	cmd := exec.Command("tar", "xzf", tarballPath, "-C", extractDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract tarball: %w\n%s", err, string(output))
	}

	extracted := filepath.Join(extractDir, spec.Name)
	if _, err := os.Stat(extracted); err != nil {
		return fmt.Errorf("binary %s not found in release tarball", spec.Name)
	}
	if err := os.Chmod(extracted, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", destDir, err)
	}
	// Stage next to the destination so the final rename is atomic and safe
	// even when the old binary is currently running.
	staged := filepath.Join(destDir, "."+spec.Name+".pvg-new")
	if err := copyExecutable(extracted, staged); err != nil {
		return fmt.Errorf("stage binary: %w", err)
	}
	if err := os.Rename(staged, filepath.Join(destDir, spec.Name)); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

// installToolSkill downloads the source tarball at tag and installs the
// tool's Claude skill: vlt ships docs/vlt-skill as a plain skill directory;
// nd ships nd-skill as a Claude Code plugin installed into the plugin cache.
func installToolSkill(spec toolSpec, tag string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	srcURL := fmt.Sprintf("%s/%s/archive/refs/tags/%s.tar.gz", githubBase, spec.Repo, tag)
	tmpDir, err := os.MkdirTemp("", "pvg-skill-"+spec.Name+"-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tarballPath := filepath.Join(tmpDir, "src.tar.gz")
	if err := downloadFile(srcURL, tarballPath); err != nil {
		return fmt.Errorf("download %s: %w", srcURL, err)
	}

	version := strings.TrimPrefix(tag, "v")
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	var skillRel string
	switch spec.Name {
	case "vlt":
		skillRel = fmt.Sprintf("vlt-%s/docs/vlt-skill", version)
	case "nd":
		skillRel = fmt.Sprintf("nd-%s/nd-skill", version)
	default:
		return fmt.Errorf("no skill layout known for %s", spec.Name)
	}

	depth := strings.Count(skillRel, "/") + 1
	cmd := exec.Command("tar", "xzf", tarballPath,
		fmt.Sprintf("--strip-components=%d", depth), "-C", extractDir, skillRel)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract %s: %w\n%s", skillRel, err, string(output))
	}

	switch spec.Name {
	case "vlt":
		dest := filepath.Join(home, ".claude", "skills", "vlt-skill")
		if err := replaceDir(extractDir, dest); err != nil {
			return err
		}
		if err := writeSkillMarker(dest, tag); err != nil {
			return err
		}
		fmt.Printf("OK: vlt skill %s installed to %s\n", tag, dest)
		return nil
	case "nd":
		if err := installNDPlugin(home, extractDir, version); err != nil {
			return err
		}
		fmt.Printf("OK: nd plugin %s installed to plugin cache\n", tag)
		return nil
	}
	return nil
}

// installNDPlugin mirrors the nd repo's `make install-plugin`: copy the
// plugin payload into the Claude plugin cache, stamp the version, enable
// nd@nd in settings.json, and drop any stale skill-style install.
func installNDPlugin(home, srcDir, version string) error {
	cacheDir := filepath.Join(home, ".claude", "plugins", "cache", "nd", "nd", version)
	if err := replaceDir(srcDir, cacheDir); err != nil {
		return err
	}

	for _, rel := range []string{
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join(".claude-plugin", "marketplace.json"),
	} {
		if err := stampVersion(filepath.Join(cacheDir, rel), version); err != nil {
			return err
		}
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := enablePlugin(settingsPath, "nd@nd"); err != nil {
		return err
	}

	stale := filepath.Join(home, ".claude", "skills", "nd-skill")
	if _, err := os.Stat(stale); err == nil {
		if err := os.RemoveAll(stale); err != nil {
			return fmt.Errorf("remove stale %s: %w", stale, err)
		}
	}
	return nil
}

// skillCurrent reports whether the installed skill matches tag.
func skillCurrent(spec toolSpec, tag string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	switch spec.Name {
	case "vlt":
		data, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "vlt-skill", skillVersionMarker))
		return err == nil && strings.TrimSpace(string(data)) == tag
	case "nd":
		version := strings.TrimPrefix(tag, "v")
		_, err := os.Stat(filepath.Join(home, ".claude", "plugins", "cache", "nd", "nd", version, ".claude-plugin", "plugin.json"))
		return err == nil
	}
	return false
}

func writeSkillMarker(dir, tag string) error {
	return os.WriteFile(filepath.Join(dir, skillVersionMarker), []byte(tag+"\n"), 0o644)
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

// replaceDir atomically-ish swaps dest with the contents of src.
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

func copyExecutable(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o755)
}

// stampVersion sets the top-level "version" field of a JSON file, plus
// plugins[0].version when a marketplace-style plugins array is present.
func stampVersion(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if _, ok := doc["version"]; ok {
		doc["version"] = version
	}
	if plugins, ok := doc["plugins"].([]any); ok && len(plugins) > 0 {
		if first, ok := plugins[0].(map[string]any); ok {
			first["version"] = version
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// enablePlugin sets enabledPlugins[key] = true in a Claude settings.json,
// creating the file when absent and preserving every other key.
func enablePlugin(settingsPath, key string) error {
	doc := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	enabled, ok := doc["enabledPlugins"].(map[string]any)
	if !ok {
		enabled = map[string]any{}
	}
	if v, ok := enabled[key].(bool); ok && v {
		doc["enabledPlugins"] = enabled
		return nil
	}
	enabled[key] = true
	doc["enabledPlugins"] = enabled

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(settingsPath), err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o644)
}
