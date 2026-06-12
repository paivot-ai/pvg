package converge

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

	"github.com/paivot-ai/pvg/internal/channel"
	"github.com/paivot-ai/pvg/internal/paivotcfg"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
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

// startReleaseServer serves a fake GitHub releases endpoint for one tool.
func startReleaseServer(t *testing.T, tool, repo, tag string) *httptest.Server {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", tool, version, runtime.GOOS, runtime.GOARCH)
	binTar := makeTarGz(t, map[string]string{tool: "#!/bin/sh\necho " + tool + " " + version + "\n"})
	checksums := sha256Hex(binTar) + "  " + asset + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/"+repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binTar)
	})
	mux.HandleFunc("/"+repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func withGithubBase(t *testing.T, base string) {
	t.Helper()
	old := githubBase
	githubBase = base
	t.Cleanup(func() { githubBase = old })
}

func withManifest(t *testing.T, m channel.Manifest) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	old := fetchManifest
	fetchManifest = func(ref string) (channel.Manifest, []byte, error) {
		return m, raw, nil
	}
	t.Cleanup(func() { fetchManifest = old })
}

func withSeams(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	oldHome, oldSelf, oldLook, oldVer, oldWritable, oldSudo, oldSh, oldSelfVer :=
		userHomeDir, selfPath, lookPath, toolVersionOutput, dirWritable, sudoNonInteractive, shResolvesPvg, SelfVersion
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() {
		userHomeDir, selfPath, lookPath, toolVersionOutput, dirWritable, sudoNonInteractive, shResolvesPvg, SelfVersion =
			oldHome, oldSelf, oldLook, oldVer, oldWritable, oldSudo, oldSh, oldSelfVer
	})
}

func toolOnlyManifest(ndVersion string) channel.Manifest {
	return channel.Manifest{
		Schema: 1, Channel: "stable", Updated: "2026-06-11",
		Tools: map[string]channel.Pin{
			"nd": {Repo: "paivot-ai/nd", Version: ndVersion},
		},
	}
}

func TestRun_InstallsMissingToolFreshIntoLocalBin(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	server := startReleaseServer(t, "nd", "paivot-ai/nd", "v0.10.20")
	withGithubBase(t, server.URL)
	withManifest(t, toolOnlyManifest("v0.10.20"))

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	dirWritable = func(dir string) bool { return strings.HasPrefix(dir, home) }
	sudoNonInteractive = func() bool { return false }
	shResolvesPvg = func() bool { return true }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"nd"}, PathWiring: true, Out: &buf})
	if err != nil {
		t.Fatalf("Run() error: %v\n%s", err, buf.String())
	}
	if rep.Failed {
		t.Fatalf("Run() failed:\n%s", buf.String())
	}

	bin := filepath.Join(home, ".local", "bin", "nd")
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("binary not installed at %s: %v", bin, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary not executable: %v", info.Mode())
	}
	out := buf.String()
	if !strings.Contains(out, "OK: nd v0.10.20 installed to "+filepath.Join(home, ".local", "bin")) {
		t.Errorf("missing install line:\n%s", out)
	}
	if !strings.Contains(out, "Summary:") || !strings.Contains(out, "installed") {
		t.Errorf("missing summary:\n%s", out)
	}
}

func TestRun_ReplacesOutdatedToolInPlace(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	server := startReleaseServer(t, "nd", "paivot-ai/nd", "v0.10.20")
	withGithubBase(t, server.URL)
	withManifest(t, toolOnlyManifest("v0.10.20"))

	binDir := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(binDir, "nd")
	if err := os.WriteFile(existing, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	lookPath = func(name string) (string, error) {
		if name == "nd" {
			return existing, nil
		}
		return "", os.ErrNotExist
	}
	toolVersionOutput = func(string, ...string) (string, error) { return "nd version v0.10.19", nil }
	sudoNonInteractive = func() bool { return false }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"nd"}, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "old" {
		t.Fatal("binary not replaced in place")
	}
	if !strings.Contains(buf.String(), "updated in place at "+binDir) {
		t.Errorf("missing in-place update line:\n%s", buf.String())
	}
}

func TestRun_CurrentToolIsLeftAlone(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	withManifest(t, toolOnlyManifest("v0.10.20"))
	// No release server: any download attempt would fail loudly.
	withGithubBase(t, "http://127.0.0.1:0")

	lookPath = func(name string) (string, error) { return "/fake/bin/nd", nil }
	toolVersionOutput = func(string, ...string) (string, error) { return "nd version v0.10.20", nil }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"nd"}, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	if !strings.Contains(buf.String(), "OK: nd v0.10.20 is current") {
		t.Errorf("missing current line:\n%s", buf.String())
	}
}

func TestRun_DryRunMutatesNothing(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	withManifest(t, toolOnlyManifest("v0.10.20"))
	withGithubBase(t, "http://127.0.0.1:0")

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	dirWritable = func(dir string) bool { return strings.HasPrefix(dir, home) }
	sudoNonInteractive = func() bool { return false }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"nd"}, DryRun: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nd")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not install anything")
	}
	if !strings.Contains(buf.String(), "would install nd v0.10.20") {
		t.Errorf("missing dry-run plan line:\n%s", buf.String())
	}
	if len(rep.Artifacts) != 1 || rep.Artifacts[0].State != "would-change" {
		t.Errorf("artifacts = %+v", rep.Artifacts)
	}
}

func TestRun_SelfUpdateUsesOwnExecutablePath(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	server := startReleaseServer(t, "pvg", "paivot-ai/pvg", "v1.55.0")
	withGithubBase(t, server.URL)
	withManifest(t, channel.Manifest{
		Schema: 1, Channel: "stable",
		Tools: map[string]channel.Pin{"pvg": {Repo: "paivot-ai/pvg", Version: "v1.55.0"}},
	})

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(binDir, "pvg")
	if err := os.WriteFile(self, []byte("old pvg"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = func() (string, error) { return self, nil }
	SelfVersion = "1.54.0"
	sudoNonInteractive = func() bool { return false }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"pvg"}, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	data, _ := os.ReadFile(self)
	if string(data) == "old pvg" {
		t.Fatal("pvg not replaced at its own location")
	}
}

func TestRun_ChecksumMismatchFailsStepButContinues(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)

	version := "0.10.20"
	asset := fmt.Sprintf("nd_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	binTar := makeTarGz(t, map[string]string{"nd": "evil"})
	mux := http.NewServeMux()
	mux.HandleFunc("/paivot-ai/nd/releases/download/v"+version+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binTar)
	})
	mux.HandleFunc("/paivot-ai/nd/releases/download/v"+version+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  " + asset + "\n"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	withGithubBase(t, server.URL)
	withManifest(t, toolOnlyManifest("v"+version))

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	dirWritable = func(dir string) bool { return strings.HasPrefix(dir, home) }
	sudoNonInteractive = func() bool { return false }

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"nd"}, Out: &buf})
	if err != nil {
		t.Fatalf("Run() hard error: %v", err)
	}
	if !rep.Failed {
		t.Fatalf("checksum mismatch must mark the run failed:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "SHA256 mismatch") {
		t.Errorf("missing mismatch detail:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nd")); !os.IsNotExist(err) {
		t.Fatal("tampered binary must not be installed")
	}
}

func TestRun_VltSkillInstalledAtPinnedTag(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)

	srcTar := makeTarGz(t, map[string]string{
		"vlt-0.11.0/docs/vlt-skill/SKILL.md": "# vlt skill\n",
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/paivot-ai/vlt/archive/refs/tags/v0.11.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(srcTar)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	withGithubBase(t, server.URL)
	withManifest(t, channel.Manifest{
		Schema: 1, Channel: "stable",
		Tools:  map[string]channel.Pin{"pvg": {Repo: "paivot-ai/pvg", Version: "v1.55.0"}},
		Skills: map[string]channel.Pin{"vlt-skill": {Repo: "paivot-ai/vlt", Version: "v0.11.0"}},
	})

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"none"}, VltSkill: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}

	skillDir := filepath.Join(home, ".claude", "skills", "vlt-skill")
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(skillDir, skillVersionMarker))
	if err != nil || strings.TrimSpace(string(marker)) != "v0.11.0" {
		t.Fatalf("skill marker = %q, err %v", marker, err)
	}

	// Second run: skill is current, no re-download (server would still
	// serve it, so assert via the OK-current line).
	buf.Reset()
	rep, err = Run(Options{Tools: []string{"none"}, VltSkill: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("second Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	if !strings.Contains(buf.String(), "OK: vlt skill v0.11.0 is current") {
		t.Errorf("missing skill-current line:\n%s", buf.String())
	}
}

func TestRun_PluginsConvergedAndSummarized(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	withManifest(t, pluginManifest())

	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	f.respond("plugin list", strings.Replace(pluginListOutput, "1.54.0", "1.55.0", 1), nil)
	withFakeClaude(t, f)

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"none"}, Plugins: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "OK: plugin nd@nd at 0.10.20") ||
		!strings.Contains(out, "OK: plugin paivot-graph@paivot-graph at 1.55.0") {
		t.Errorf("missing plugin verification lines:\n%s", out)
	}
	var pluginRows int
	for _, a := range rep.Artifacts {
		if a.Kind == "plugin" {
			pluginRows++
			if a.State != "ok" {
				t.Errorf("plugin artifact %s state = %s", a.Name, a.State)
			}
		}
	}
	if pluginRows != 2 {
		t.Errorf("expected 2 plugin summary rows, got %d: %+v", pluginRows, rep.Artifacts)
	}
}

// Regression: a real `pvg setup` run printed per-step verification
// "OK: plugin nd@nd at 0.10.20" but the summary table still showed
// "(not installed)" for both plugins -- the summary artifacts discarded
// the post-convergence versions. The summary must reflect the same
// `claude plugin list` snapshot the verification step used, including
// when that list holds many plugins from other marketplaces.
func TestRun_SummaryShowsInstalledPluginVersions(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	withManifest(t, pluginManifest())

	// Realistic multi-marketplace list: U+276F headers, two-space indented
	// fields, blank-line separated entries, unknown and commit-hash versions.
	const realisticPluginList = `Installed plugins:

  ❯ codex@openai-codex
    Version: 1.0.4
    Scope: user
    Status: enabled

  ❯ commit-commands@claude-plugins-official
    Version: unknown
    Scope: user
    Status: enabled

  ❯ elixir-phoenix@artistree
    Version: 3f9ac2e
    Scope: user
    Status: enabled

  ❯ nd@nd
    Version: 0.10.20
    Scope: user
    Status: enabled

  ❯ paivot-graph@paivot-graph
    Version: 1.55.0
    Scope: user
    Status: enabled

  ❯ plugin-dev@claude-plugins-official
    Version: unknown
    Scope: user
    Status: enabled
`
	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	f.respond("plugin list", realisticPluginList, nil)
	withFakeClaude(t, f)

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"none"}, Plugins: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}

	want := map[string]string{
		"nd (plugin)":           "0.10.20",
		"paivot-graph (plugin)": "1.55.0",
	}
	for _, a := range rep.Artifacts {
		if a.Kind != "plugin" {
			continue
		}
		if a.Installed != want[a.Name] {
			t.Errorf("artifact %s Installed = %q, want %q", a.Name, a.Installed, want[a.Name])
		}
		if a.State != "ok" {
			t.Errorf("artifact %s State = %q, want ok", a.Name, a.State)
		}
	}

	out := buf.String()
	summary := out[strings.Index(out, "Summary:"):]
	if strings.Contains(summary, "(not installed)") {
		t.Errorf("summary must not show (not installed) for verified plugins:\n%s", summary)
	}
	for _, line := range []string{"nd (plugin)", "paivot-graph (plugin)"} {
		if !strings.Contains(summary, line) {
			t.Errorf("summary missing row %q:\n%s", line, summary)
		}
	}
	if !strings.Contains(summary, "0.10.20") || !strings.Contains(summary, "1.55.0") {
		t.Errorf("summary missing installed versions:\n%s", summary)
	}
}

func TestRun_VltSkillDryRunMutatesNothing(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	withManifest(t, channel.Manifest{
		Schema: 1, Channel: "stable",
		Tools:  map[string]channel.Pin{"pvg": {Repo: "paivot-ai/pvg", Version: "v1.55.0"}},
		Skills: map[string]channel.Pin{"vlt-skill": {Repo: "paivot-ai/vlt", Version: "v0.11.0"}},
	})

	var buf bytes.Buffer
	rep, err := Run(Options{Tools: []string{"none"}, VltSkill: true, DryRun: true, Out: &buf})
	if err != nil || rep.Failed {
		t.Fatalf("Run() err=%v failed=%v\n%s", err, rep.Failed, buf.String())
	}
	if !strings.Contains(buf.String(), "would install vlt skill v0.11.0") {
		t.Errorf("missing dry-run skill line:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "vlt-skill")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not install the skill")
	}
}

func TestRun_ManifestFetchFailureIsHardError(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	old := fetchManifest
	fetchManifest = func(string) (channel.Manifest, []byte, error) {
		return channel.Manifest{}, nil, fmt.Errorf("HTTP 404")
	}
	t.Cleanup(func() { fetchManifest = old })

	var buf bytes.Buffer
	rep, err := Run(Options{Out: &buf})
	if err == nil || !rep.Failed {
		t.Fatalf("manifest failure must be a hard error, got err=%v failed=%v", err, rep.Failed)
	}
}

func TestEnsurePathWiring_AppendsOnceWithMarker(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	shResolvesPvg = func() bool { return false }

	profile := filepath.Join(home, ".profile")
	if _, err := ensurePathWiring(); err != nil {
		t.Fatalf("ensurePathWiring() error: %v", err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatalf(".profile not created: %v", err)
	}
	if !strings.Contains(string(data), pathMarker) || !strings.Contains(string(data), pathExportLine) {
		t.Fatalf(".profile missing PATH wiring:\n%s", data)
	}

	// Re-run must not duplicate the line.
	if _, err := ensurePathWiring(); err != nil {
		t.Fatalf("second ensurePathWiring() error: %v", err)
	}
	data, _ = os.ReadFile(profile)
	if strings.Count(string(data), pathExportLine) != 1 {
		t.Fatalf("PATH line duplicated:\n%s", data)
	}
}

func TestEnsurePathWiring_SkipsWhenShellResolvesPvg(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)
	shResolvesPvg = func() bool { return true }

	if _, err := ensurePathWiring(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".profile")); !os.IsNotExist(err) {
		t.Fatal(".profile must not be touched when PATH already resolves pvg")
	}
}

func TestCheckPins(t *testing.T) {
	home := t.TempDir()
	withSeams(t, home)

	SelfVersion = "1.55.0"
	lookPath = func(name string) (string, error) {
		if name == "nd" {
			return "/fake/bin/nd", nil
		}
		return "", os.ErrNotExist // vlt missing
	}
	toolVersionOutput = func(name string, args ...string) (string, error) {
		return "nd version v0.10.19", nil
	}

	tc := paivotcfg.Toolchain{Channel: "stable", Pvg: "v1.55.0", Nd: "v0.10.20", Vlt: "v0.11.0"}
	warnings := CheckPins(tc)
	joined := strings.Join(warnings, "\n")
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings:\n%s", len(warnings), joined)
	}
	if !strings.Contains(joined, "nd is 0.10.19") || !strings.Contains(joined, "v0.10.20") {
		t.Errorf("nd drift warning missing:\n%s", joined)
	}
	if !strings.Contains(joined, "vlt pinned at v0.11.0") {
		t.Errorf("vlt missing-tool warning missing:\n%s", joined)
	}
	if strings.Contains(joined, "pvg is") {
		t.Errorf("pvg matches the pin, no warning expected:\n%s", joined)
	}

	// All convergent: no warnings.
	toolVersionOutput = func(string, ...string) (string, error) { return "nd version v0.10.20", nil }
	if w := CheckPins(paivotcfg.Toolchain{Pvg: "v1.55.0", Nd: "v0.10.20"}); len(w) != 0 {
		t.Errorf("expected no warnings, got %v", w)
	}

	// Empty pin block: nothing to check.
	if w := CheckPins(paivotcfg.Toolchain{Channel: "stable"}); len(w) != 0 {
		t.Errorf("empty pins must yield no warnings, got %v", w)
	}
}
