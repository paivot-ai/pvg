package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/channel"
	"github.com/paivot-ai/pvg/internal/paivotcfg"
)

func withNudgeSeams(t *testing.T, manifest channel.Manifest, manifestOK bool, installed string) {
	t.Helper()
	oldForNudge, oldInstalled := channelForNudge, installedPvgVersion
	channelForNudge = func() (channel.Manifest, bool) { return manifest, manifestOK }
	installedPvgVersion = func() string { return installed }
	t.Cleanup(func() { channelForNudge, installedPvgVersion = oldForNudge, oldInstalled })
}

func nudgeManifest(pvgVersion string) channel.Manifest {
	return channel.Manifest{
		Schema: 1, Channel: "stable",
		Tools: map[string]channel.Pin{"pvg": {Repo: "paivot-ai/pvg", Version: pvgVersion}},
	}
}

func writeProjectSetting(t *testing.T, cwd, content string) {
	t.Helper()
	dir := filepath.Join(cwd, ".vault", "knowledge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".settings.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChannelNudgeLine_EmitsOnDrift(t *testing.T) {
	cwd := t.TempDir()
	withNudgeSeams(t, nudgeManifest("v1.55.0"), true, "1.54.0")

	got := channelNudgeLine(cwd)
	want := "paivot: channel stable has pvg v1.55.0 (installed 1.54.0) -- run pvg update"
	if got != want {
		t.Fatalf("channelNudgeLine() = %q, want %q", got, want)
	}
}

func TestChannelNudgeLine_SilentWhenCurrent(t *testing.T) {
	cwd := t.TempDir()
	withNudgeSeams(t, nudgeManifest("v1.55.0"), true, "1.55.0")
	if got := channelNudgeLine(cwd); got != "" {
		t.Fatalf("expected no nudge when current, got %q", got)
	}
}

func TestChannelNudgeLine_SilentOnDevBuild(t *testing.T) {
	cwd := t.TempDir()
	withNudgeSeams(t, nudgeManifest("v1.55.0"), true, "")
	if got := channelNudgeLine(cwd); got != "" {
		t.Fatalf("expected no nudge for unparseable installed version, got %q", got)
	}
}

func TestChannelNudgeLine_SilentWhenManifestUnavailable(t *testing.T) {
	cwd := t.TempDir()
	withNudgeSeams(t, channel.Manifest{}, false, "1.54.0")
	if got := channelNudgeLine(cwd); got != "" {
		t.Fatalf("expected no nudge when channel unavailable, got %q", got)
	}
}

func TestChannelNudgeLine_RespectsOptOut(t *testing.T) {
	cwd := t.TempDir()
	writeProjectSetting(t, cwd, "update.nudge: false\n")

	called := false
	oldForNudge, oldInstalled := channelForNudge, installedPvgVersion
	channelForNudge = func() (channel.Manifest, bool) {
		called = true
		return nudgeManifest("v1.55.0"), true
	}
	installedPvgVersion = func() string { return "1.54.0" }
	t.Cleanup(func() { channelForNudge, installedPvgVersion = oldForNudge, oldInstalled })

	if got := channelNudgeLine(cwd); got != "" {
		t.Fatalf("expected no nudge with update.nudge=false, got %q", got)
	}
	if called {
		t.Fatal("opt-out must short-circuit before any channel access")
	}
}

func TestToolchainPinWarnings_NoPinNoWarnings(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := toolchainPinWarnings(cwd); len(got) != 0 {
		t.Fatalf("expected no warnings without a pin, got %v", got)
	}
}

func TestToolchainPinWarnings_DelegatesToCheckPins(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := paivotcfg.WriteToolchain(cwd, paivotcfg.Toolchain{Channel: "stable", Nd: "v0.10.20"}); err != nil {
		t.Fatal(err)
	}

	old := checkToolchainPins
	checkToolchainPins = func(tc paivotcfg.Toolchain) []string {
		if tc.Nd != "v0.10.20" {
			t.Errorf("CheckPins received %+v", tc)
		}
		return []string{"WARNING: toolchain pin: nd is 0.10.19 but the project pins v0.10.20 -- run pvg update"}
	}
	t.Cleanup(func() { checkToolchainPins = old })

	got := toolchainPinWarnings(cwd)
	if len(got) != 1 || !strings.Contains(got[0], "WARNING: toolchain pin") {
		t.Fatalf("toolchainPinWarnings() = %v", got)
	}
}

func TestToolchainPinWarnings_ResolvesProjectRootFromSubdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := paivotcfg.WriteToolchain(root, paivotcfg.Toolchain{Vlt: "v0.11.0"}); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	old := checkToolchainPins
	checkToolchainPins = func(tc paivotcfg.Toolchain) []string {
		return []string{"WARNING: drift on " + tc.Vlt}
	}
	t.Cleanup(func() { checkToolchainPins = old })

	got := toolchainPinWarnings(sub)
	if len(got) != 1 || !strings.Contains(got[0], "v0.11.0") {
		t.Fatalf("pin not found from subdirectory: %v", got)
	}
}

func TestUpdateNudgeEnabled_Default(t *testing.T) {
	if !updateNudgeEnabled(t.TempDir()) {
		t.Fatal("update.nudge must default to enabled")
	}
	cwd := t.TempDir()
	writeProjectSetting(t, cwd, "update.nudge: true\n")
	if !updateNudgeEnabled(cwd) {
		t.Fatal("explicit true must be enabled")
	}
	writeProjectSetting(t, cwd, "update.nudge: false\n")
	if updateNudgeEnabled(cwd) {
		t.Fatal("explicit false must disable the nudge")
	}
}
