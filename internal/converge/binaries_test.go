package converge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withSudoPassthrough fakes execCommand so "sudo -n <cmd> <args...>" runs
// "<cmd> <args...>" directly, recording each sudo invocation.
func withSudoPassthrough(t *testing.T, calls *[]string) {
	t.Helper()
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "sudo" && len(args) >= 2 && args[0] == "-n" {
			*calls = append(*calls, "sudo "+strings.Join(args, " "))
			return exec.Command(args[1], args[2:]...)
		}
		return exec.Command(name, args...)
	}
	t.Cleanup(func() { execCommand = old })
}

func TestSudoInstall_StagesAndRenamesThroughSudo(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "newbin")
	if err := os.WriteFile(src, []byte("new binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(tmp, "bin")

	var calls []string
	withSudoPassthrough(t, &calls)

	if err := sudoInstall(src, destDir, "nd"); err != nil {
		t.Fatalf("sudoInstall() error: %v\ncalls: %v", err, calls)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "nd"))
	if err != nil || string(data) != "new binary" {
		t.Fatalf("binary not installed: %q err=%v", data, err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"sudo -n mkdir -p", "sudo -n cp", "sudo -n chmod 0755", "sudo -n mv -f"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in sudo sequence:\n%s", want, joined)
		}
	}
	// No staging leftovers.
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nd.pvg-new-") {
			t.Errorf("staging file left behind: %s", e.Name())
		}
	}
}

func TestSudoInstall_FailureSurfacesCommand(t *testing.T) {
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	t.Cleanup(func() { execCommand = old })

	err := sudoInstall("/nonexistent", "/also/nonexistent", "nd")
	if err == nil || !strings.Contains(err.Error(), "sudo -n mkdir") {
		t.Fatalf("expected failing sudo command in error, got %v", err)
	}
}

func TestInstalledTool_QueriesBinaryOnPath(t *testing.T) {
	oldLook, oldVer := lookPath, toolVersionOutput
	lookPath = func(name string) (string, error) {
		if name == "vlt" {
			return "/fake/bin/vlt", nil
		}
		return "", os.ErrNotExist
	}
	toolVersionOutput = func(name string, args ...string) (string, error) {
		if name != "/fake/bin/vlt" || len(args) != 1 || args[0] != "version" {
			t.Errorf("unexpected version query: %s %v", name, args)
		}
		return "vlt v0.11.0", nil
	}
	t.Cleanup(func() { lookPath, toolVersionOutput = oldLook, oldVer })

	path, out := installedTool(toolSpec{Name: "vlt", VersionArgs: []string{"version"}})
	if path != "/fake/bin/vlt" || out != "vlt v0.11.0" {
		t.Fatalf("installedTool() = (%q, %q)", path, out)
	}

	// Missing tool: no path, no version.
	path, out = installedTool(toolSpec{Name: "nd", VersionArgs: []string{"--version"}})
	if path != "" || out != "" {
		t.Fatalf("missing tool must report empty, got (%q, %q)", path, out)
	}
}

func TestInstalledTool_PvgUsesSelfPathAndVersion(t *testing.T) {
	oldSelf, oldVer := selfPath, SelfVersion
	selfPath = func() (string, error) { return "/opt/bin/pvg", nil }
	SelfVersion = "1.55.0"
	t.Cleanup(func() { selfPath, SelfVersion = oldSelf, oldVer })

	path, out := installedTool(toolSpec{Name: "pvg", VersionArgs: []string{"version"}})
	if path != "/opt/bin/pvg" || out != "1.55.0" {
		t.Fatalf("installedTool(pvg) = (%q, %q)", path, out)
	}
}

func TestInstalledToolVersion(t *testing.T) {
	oldSelf, oldLook, oldVer := SelfVersion, lookPath, toolVersionOutput
	SelfVersion = "1.55.0"
	lookPath = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	toolVersionOutput = func(name string, args ...string) (string, error) {
		return "nd version v0.10.20", nil
	}
	t.Cleanup(func() { SelfVersion, lookPath, toolVersionOutput = oldSelf, oldLook, oldVer })

	if got := InstalledToolVersion("pvg"); got != "1.55.0" {
		t.Errorf("InstalledToolVersion(pvg) = %q", got)
	}
	if got := InstalledToolVersion("nd"); got != "0.10.20" {
		t.Errorf("InstalledToolVersion(nd) = %q", got)
	}
	if got := InstalledToolVersion("unknown-tool"); got != "" {
		t.Errorf("InstalledToolVersion(unknown) = %q", got)
	}
}
