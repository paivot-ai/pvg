package converge

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/channel"
)

const marketplaceListOutput = `Configured marketplaces:

  ❯ claude-plugins-official
    Source: GitHub (anthropics/claude-plugins-official)

  ❯ nd
    Source: Directory (/Users/u/workspace/nd/nd-skill)

  ❯ paivot-graph
    Source: GitHub (paivot-ai/paivot-graph)
`

const pluginListOutput = `Installed plugins:

  ❯ nd@nd
    Version: 0.10.20
    Scope: user
    Status: enabled

  ❯ paivot-graph@paivot-graph
    Version: 1.54.0
    Scope: user
    Status: enabled

  ❯ codex@openai-codex
    Version: 1.0.4
    Scope: user
    Status: enabled
`

func TestParseMarketplaceList(t *testing.T) {
	entries := parseMarketplaceList(marketplaceListOutput)
	if len(entries) != 3 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	want := []marketplaceEntry{
		{Name: "claude-plugins-official", SourceType: "GitHub", Source: "anthropics/claude-plugins-official"},
		{Name: "nd", SourceType: "Directory", Source: "/Users/u/workspace/nd/nd-skill"},
		{Name: "paivot-graph", SourceType: "GitHub", Source: "paivot-ai/paivot-graph"},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry[%d] = %+v, want %+v", i, entries[i], w)
		}
	}
}

func TestParsePluginList(t *testing.T) {
	entries := parsePluginList(pluginListOutput)
	if len(entries) != 3 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	want := []pluginEntry{
		{Name: "nd", Marketplace: "nd", Version: "0.10.20"},
		{Name: "paivot-graph", Marketplace: "paivot-graph", Version: "1.54.0"},
		{Name: "codex", Marketplace: "openai-codex", Version: "1.0.4"},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry[%d] = %+v, want %+v", i, entries[i], w)
		}
	}
}

func TestParseLists_EmptyAndGarbage(t *testing.T) {
	if got := parseMarketplaceList(""); len(got) != 0 {
		t.Errorf("empty input parsed to %+v", got)
	}
	if got := parsePluginList("No plugins installed.\n"); len(got) != 0 {
		t.Errorf("garbage input parsed to %+v", got)
	}
}

// fakeClaude records claude CLI calls and serves canned responses.
type fakeClaude struct {
	calls     []string
	responses map[string]struct {
		out string
		err error
	}
}

func (f *fakeClaude) run(args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if r, ok := f.responses[key]; ok {
		return r.out, r.err
	}
	return "", nil
}

func (f *fakeClaude) respond(key, out string, err error) {
	if f.responses == nil {
		f.responses = map[string]struct {
			out string
			err error
		}{}
	}
	f.responses[key] = struct {
		out string
		err error
	}{out, err}
}

func withFakeClaude(t *testing.T, f *fakeClaude) {
	t.Helper()
	oldRun, oldLook := runClaude, lookPath
	runClaude = f.run
	lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/fake/bin/claude", nil
		}
		return oldLook(name)
	}
	t.Cleanup(func() { runClaude, lookPath = oldRun, oldLook })
}

func pluginManifest() channel.Manifest {
	return channel.Manifest{
		Schema: 1, Channel: "stable",
		Tools: map[string]channel.Pin{"pvg": {Repo: "paivot-ai/pvg", Version: "v1.55.0"}},
		Plugins: map[string]channel.PluginPin{
			"paivot-graph": {Marketplace: "paivot-ai/paivot-graph", Version: "1.55.0"},
			"nd":           {Marketplace: "paivot-ai/nd", Version: "0.10.20"},
		},
	}
}

func collectReports(lines *[]string) func(status, format string, args ...any) {
	return func(status, format string, args ...any) {
		*lines = append(*lines, status+": "+fmt.Sprintf(format, args...))
	}
}

func TestConvergePlugins_ReplacesDirectoryMarketplaceAndVerifies(t *testing.T) {
	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	f.respond("plugin list", strings.Replace(pluginListOutput, "1.54.0", "1.55.0", 1), nil)
	withFakeClaude(t, f)

	var lines []string
	ok := convergePlugins(pluginManifest(), false, collectReports(&lines))
	if !ok {
		t.Fatalf("convergePlugins() failed:\n%s", strings.Join(lines, "\n"))
	}

	joined := strings.Join(f.calls, "\n")
	wantCalls := []string{
		"plugin marketplace list",
		// nd has a Directory source: removed then re-added from GitHub.
		"plugin marketplace remove nd",
		"plugin marketplace add paivot-ai/nd",
		"plugin install nd@nd",
		"plugin marketplace update nd",
		"plugin update nd@nd",
		// paivot-graph is already a GitHub marketplace: no remove/add.
		"plugin install paivot-graph@paivot-graph",
		"plugin marketplace update paivot-graph",
		"plugin update paivot-graph@paivot-graph",
		"plugin list",
	}
	if joined != strings.Join(wantCalls, "\n") {
		t.Errorf("claude calls:\n%s\nwant:\n%s", joined, strings.Join(wantCalls, "\n"))
	}
}

func TestConvergePlugins_AddsMissingMarketplace(t *testing.T) {
	f := &fakeClaude{}
	f.respond("plugin marketplace list", "Configured marketplaces:\n\n  ❯ other\n    Source: GitHub (x/y)\n", nil)
	f.respond("plugin list", strings.Replace(pluginListOutput, "1.54.0", "1.55.0", 1), nil)
	withFakeClaude(t, f)

	var lines []string
	if ok := convergePlugins(pluginManifest(), false, collectReports(&lines)); !ok {
		t.Fatalf("convergePlugins() failed:\n%s", strings.Join(lines, "\n"))
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{"plugin marketplace add paivot-ai/nd", "plugin marketplace add paivot-ai/paivot-graph"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "marketplace remove") {
		t.Errorf("unexpected marketplace remove in:\n%s", joined)
	}
}

func TestConvergePlugins_ToleratesAlreadyInstalled(t *testing.T) {
	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	f.respond("plugin install paivot-graph@paivot-graph", "Error: plugin already installed", errors.New("exit 1"))
	f.respond("plugin list", strings.Replace(pluginListOutput, "1.54.0", "1.55.0", 1), nil)
	withFakeClaude(t, f)

	var lines []string
	if ok := convergePlugins(pluginManifest(), false, collectReports(&lines)); !ok {
		t.Fatalf("already-installed must be tolerated:\n%s", strings.Join(lines, "\n"))
	}
}

func TestConvergePlugins_VersionMismatchFails(t *testing.T) {
	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	f.respond("plugin list", pluginListOutput, nil) // paivot-graph still at 1.54.0
	withFakeClaude(t, f)

	var lines []string
	if ok := convergePlugins(pluginManifest(), false, collectReports(&lines)); ok {
		t.Fatal("version mismatch must fail the step")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "1.54.0") || !strings.Contains(joined, "1.55.0") {
		t.Errorf("failure must show both versions:\n%s", joined)
	}
}

func TestConvergePlugins_MissingClaudeCLIFails(t *testing.T) {
	oldLook := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPath = oldLook })

	var lines []string
	if ok := convergePlugins(pluginManifest(), false, collectReports(&lines)); ok {
		t.Fatal("missing claude CLI must fail the step")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "https://claude.com/claude-code") {
		t.Errorf("failure must point at the Claude Code install page:\n%s", joined)
	}
}

func TestConvergePlugins_DryRunMutatesNothing(t *testing.T) {
	f := &fakeClaude{}
	f.respond("plugin marketplace list", marketplaceListOutput, nil)
	withFakeClaude(t, f)

	var lines []string
	if ok := convergePlugins(pluginManifest(), true, collectReports(&lines)); !ok {
		t.Fatalf("dry-run failed:\n%s", strings.Join(lines, "\n"))
	}
	for _, call := range f.calls {
		if call != "plugin marketplace list" {
			t.Errorf("dry-run made mutating call %q", call)
		}
	}
}
