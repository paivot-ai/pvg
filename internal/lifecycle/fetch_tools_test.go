package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// startToolServer serves a fake GitHub for one tool at the given tag.
func startToolServer(t *testing.T, spec toolSpec, tag string, sourceFiles map[string]string) (*httptest.Server, *int) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", spec.Name, version, runtime.GOOS, runtime.GOARCH)
	binTar := makeTarGz(t, map[string]string{spec.Name: "#!/bin/sh\necho " + spec.Name + " " + tag + "\n"})
	checksums := sha256Hex(binTar) + "  " + asset + "\n"
	srcTar := makeTarGz(t, sourceFiles)

	requests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+spec.Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
	})
	mux.HandleFunc("/"+spec.Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(binTar)
	})
	mux.HandleFunc("/"+spec.Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(checksums))
	})
	mux.HandleFunc("/"+spec.Repo+"/archive/refs/tags/"+tag+".tar.gz", func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(srcTar)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &requests
}

func withFakeGitHub(t *testing.T, server *httptest.Server) {
	t.Helper()
	oldAPI, oldBase := githubAPIBase, githubBase
	githubAPIBase, githubBase = server.URL, server.URL
	t.Cleanup(func() { githubAPIBase, githubBase = oldAPI, oldBase })
}

func TestFetchTool_InstallsVltBinaryAndSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	spec := toolSpec{Name: "vlt", Repo: "paivot-ai/vlt", VersionArgs: []string{"version"}}
	server, _ := startToolServer(t, spec, "v1.2.3", map[string]string{
		"vlt-1.2.3/docs/vlt-skill/SKILL.md": "# vlt skill\n",
	})
	withFakeGitHub(t, server)

	oldLook := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookPath = oldLook })

	if err := fetchTool(spec, false); err != nil {
		t.Fatalf("fetchTool() error: %v", err)
	}

	binPath := filepath.Join(home, "go", "bin", "vlt")
	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary not executable: %v", info.Mode())
	}

	skillFile := filepath.Join(home, ".claude", "skills", "vlt-skill", "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "vlt-skill", skillVersionMarker))
	if err != nil || strings.TrimSpace(string(marker)) != "v1.2.3" {
		t.Fatalf("skill version marker = %q, err %v", marker, err)
	}
}

func TestFetchTool_InstallsNDPluginAndEnablesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	spec := toolSpec{Name: "nd", Repo: "paivot-ai/nd", VersionArgs: []string{"--version"}}
	server, _ := startToolServer(t, spec, "v0.9.0", map[string]string{
		"nd-0.9.0/nd-skill/.claude-plugin/plugin.json":      `{"name":"nd","version":"0.0.0"}`,
		"nd-0.9.0/nd-skill/.claude-plugin/marketplace.json": `{"plugins":[{"name":"nd","version":"0.0.0"}]}`,
		"nd-0.9.0/nd-skill/skills/nd/SKILL.md":              "# nd skill\n",
	})
	withFakeGitHub(t, server)

	// Pre-existing settings.json with unrelated keys must survive.
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"model":"opus","enabledPlugins":{"x@y":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stale skill-style install must be removed.
	staleSkill := filepath.Join(home, ".claude", "skills", "nd-skill")
	if err := os.MkdirAll(staleSkill, 0o755); err != nil {
		t.Fatal(err)
	}

	oldLook := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookPath = oldLook })

	if err := fetchTool(spec, false); err != nil {
		t.Fatalf("fetchTool() error: %v", err)
	}

	cacheDir := filepath.Join(home, ".claude", "plugins", "cache", "nd", "nd", "0.9.0")
	pluginJSON, err := os.ReadFile(filepath.Join(cacheDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("plugin.json not installed: %v", err)
	}
	if !strings.Contains(string(pluginJSON), `"version": "0.9.0"`) {
		t.Fatalf("plugin.json version not stamped: %s", pluginJSON)
	}
	marketJSON, err := os.ReadFile(filepath.Join(cacheDir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("marketplace.json not installed: %v", err)
	}
	if !strings.Contains(string(marketJSON), `"version": "0.9.0"`) {
		t.Fatalf("marketplace.json version not stamped: %s", marketJSON)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "skills", "nd", "SKILL.md")); err != nil {
		t.Fatalf("nd skill files not installed: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json invalid after install: %v", err)
	}
	if settings["model"] != "opus" {
		t.Fatalf("unrelated settings key lost: %v", settings)
	}
	enabled := settings["enabledPlugins"].(map[string]any)
	if enabled["nd@nd"] != true || enabled["x@y"] != true {
		t.Fatalf("enabledPlugins wrong: %v", enabled)
	}

	if _, err := os.Stat(staleSkill); !os.IsNotExist(err) {
		t.Fatalf("stale ~/.claude/skills/nd-skill not removed (err=%v)", err)
	}
}

func TestFetchTool_SkipsWhenBinaryAndSkillCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	spec := toolSpec{Name: "vlt", Repo: "paivot-ai/vlt", VersionArgs: []string{"version"}}
	server, requests := startToolServer(t, spec, "v1.2.3", map[string]string{
		"vlt-1.2.3/docs/vlt-skill/SKILL.md": "# vlt skill\n",
	})
	withFakeGitHub(t, server)

	skillDir := filepath.Join(home, ".claude", "skills", "vlt-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSkillMarker(skillDir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}

	// Release binaries print "vlt 1.2.3" (no leading v) -- must still match
	// the v-prefixed release tag.
	oldLook, oldVer := lookPath, toolVersion
	lookPath = func(string) (string, error) { return "/fake/bin/vlt", nil }
	toolVersion = func(string, ...string) (string, error) { return "vlt 1.2.3", nil }
	t.Cleanup(func() { lookPath, toolVersion = oldLook, oldVer })

	if err := fetchTool(spec, false); err != nil {
		t.Fatalf("fetchTool() error: %v", err)
	}
	if *requests != 1 {
		t.Fatalf("expected only the release-tag request, got %d requests", *requests)
	}
}

func TestFetchTool_ChecksumMismatchFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	spec := toolSpec{Name: "vlt", Repo: "paivot-ai/vlt", VersionArgs: []string{"version"}}
	version := "1.2.3"
	asset := fmt.Sprintf("vlt_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	binTar := makeTarGz(t, map[string]string{"vlt": "binary\n"})

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/paivot-ai/vlt/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v" + version})
	})
	mux.HandleFunc("/paivot-ai/vlt/releases/download/v"+version+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binTar)
	})
	mux.HandleFunc("/paivot-ai/vlt/releases/download/v"+version+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  " + asset + "\n"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	withFakeGitHub(t, server)

	oldLook := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookPath = oldLook })

	err := fetchTool(spec, false)
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
}

func TestFetchTool_OfflineKeepsInstalledTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	withFakeGitHub(t, server)

	spec := toolSpec{Name: "vlt", Repo: "paivot-ai/vlt", VersionArgs: []string{"version"}}

	oldLook := lookPath
	lookPath = func(string) (string, error) { return "/fake/bin/vlt", nil }
	t.Cleanup(func() { lookPath = oldLook })

	if err := fetchTool(spec, false); err != nil {
		t.Fatalf("installed tool must survive a failed release lookup, got %v", err)
	}

	// Missing tool with no reachable release is a hard error.
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := fetchTool(spec, false); err == nil {
		t.Fatal("missing tool with failed release lookup must error")
	}
}
