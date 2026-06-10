package settings

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultSoloDev(t *testing.T) {
	val, ok := defaults["workflow.solo_dev"]
	if !ok {
		t.Fatal("workflow.solo_dev missing from defaults")
	}
	if val != "true" {
		t.Fatalf("expected workflow.solo_dev default 'true', got %q", val)
	}
}

func TestLoadSettings_NoFile(t *testing.T) {
	s := loadSettings("/nonexistent/.settings.yaml")
	if len(s) != 0 {
		t.Errorf("expected empty map for missing file, got %d entries", len(s))
	}
}

func TestLoadSettings_ParsesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")
	content := "session_start_max_notes: 5\nauto_capture: false\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := loadSettings(path)
	if s["session_start_max_notes"] != "5" {
		t.Errorf("expected 5, got %q", s["session_start_max_notes"])
	}
	if s["auto_capture"] != "false" {
		t.Errorf("expected false, got %q", s["auto_capture"])
	}
}

func TestWriteSettings_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", ".settings.yaml")

	settings := map[string]string{
		"session_start_max_notes": "20",
		"staleness_days":          "60",
	}

	if err := writeSettings(path, settings); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "session_start_max_notes: 20") {
		t.Error("expected session_start_max_notes: 20 in output")
	}
	if !strings.Contains(content, "staleness_days: 60") {
		t.Error("expected staleness_days: 60 in output")
	}
}

func TestWriteAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")

	original := map[string]string{
		"auto_capture":            "true",
		"session_start_max_notes": "15",
		"staleness_days":          "45",
	}

	if err := writeSettings(path, original); err != nil {
		t.Fatal(err)
	}

	loaded := loadSettings(path)
	for k, v := range original {
		if loaded[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, loaded[k])
		}
	}
}

func TestLoadSettings_SkipsComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")
	content := "# This is a comment\nsession_start_max_notes: 5\n# Another comment\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := loadSettings(path)
	if len(s) != 1 {
		t.Errorf("expected 1 entry (comments should be skipped), got %d", len(s))
	}
	if s["session_start_max_notes"] != "5" {
		t.Errorf("expected 5, got %q", s["session_start_max_notes"])
	}
}

func TestLoadSettings_DotPrefixedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")
	content := "workflow.fsm: true\nworkflow.sequence: open,closed\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := loadSettings(path)
	if s["workflow.fsm"] != "true" {
		t.Errorf("expected 'true', got %q", s["workflow.fsm"])
	}
	if s["workflow.sequence"] != "open,closed" {
		t.Errorf("expected 'open,closed', got %q", s["workflow.sequence"])
	}
}

func TestLoadSettings_ExitRulesWithColons(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")
	// The value contains colons -- SplitN(line, ":", 2) should handle this
	content := "workflow.exit_rules: blocked:open,in_progress;rejected:in_progress\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := loadSettings(path)
	expected := "blocked:open,in_progress;rejected:in_progress"
	if s["workflow.exit_rules"] != expected {
		t.Errorf("expected %q, got %q", expected, s["workflow.exit_rules"])
	}
}

func TestLoadFile_Exported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")
	content := "workflow.fsm: true\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := LoadFile(path)
	if s["workflow.fsm"] != "true" {
		t.Errorf("LoadFile: expected 'true', got %q", s["workflow.fsm"])
	}
}

func TestLoadFile_MissingFile(t *testing.T) {
	s := LoadFile("/nonexistent/.settings.yaml")
	if len(s) != 0 {
		t.Errorf("LoadFile: expected empty map for missing file, got %d entries", len(s))
	}
}

func TestWorkflowKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".settings.yaml")

	original := map[string]string{
		"workflow.fsm":             "true",
		"workflow.sequence":        "open,in_progress,closed",
		"workflow.exit_rules":      "blocked:open;rejected:open",
		"workflow.custom_statuses": "rejected",
	}

	if err := writeSettings(path, original); err != nil {
		t.Fatal(err)
	}

	loaded := loadSettings(path)
	for k, v := range original {
		if loaded[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, loaded[k])
		}
	}
}

func TestRunSingleKey_PrintsConfiguredValue(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".vault", "knowledge", ".settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("stack_detection: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := Run([]string{"stack_detection"})

	_ = w.Close()
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(buf.String()); got != "true" {
		t.Fatalf("expected single-key output true, got %q", got)
	}
}

func TestRunSingleKey_PrintsDefaultValue(t *testing.T) {
	dir := t.TempDir()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := Run([]string{"loop.persist_across_sessions"})

	_ = w.Close()
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(buf.String()); got != "true" {
		t.Fatalf("expected default single-key output true (loop.persist_across_sessions defaults to true), got %q", got)
	}
}

func TestDefaults_LintKeys(t *testing.T) {
	val, ok := defaults["lint.quality_gates"]
	if !ok {
		t.Fatal("lint.quality_gates missing from defaults")
	}
	if val != "" {
		t.Fatalf("expected lint.quality_gates default to be empty, got %q", val)
	}

	val, ok = defaults["lint.brownfield"]
	if !ok {
		t.Fatal("lint.brownfield missing from defaults")
	}
	if val != "false" {
		t.Fatalf("expected lint.brownfield default 'false', got %q", val)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn printed plus fn's error.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	fnErr := fn()

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String(), fnErr
}

func TestRunSingleKey_LintDefaults(t *testing.T) {
	dir := t.TempDir()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	out, runErr := captureStdout(t, func() error { return Run([]string{"lint.brownfield"}) })
	if runErr != nil {
		t.Fatalf("Run get lint.brownfield: %v", runErr)
	}
	if got := strings.TrimSpace(out); got != "false" {
		t.Fatalf("expected lint.brownfield default output 'false', got %q", got)
	}

	out, runErr = captureStdout(t, func() error { return Run([]string{"lint.quality_gates"}) })
	if runErr != nil {
		t.Fatalf("Run get lint.quality_gates: %v", runErr)
	}
	if got := strings.TrimSpace(out); got != "" {
		t.Fatalf("expected lint.quality_gates default output to be empty, got %q", got)
	}
}

func TestRunSetThenGet_LintKeys(t *testing.T) {
	dir := t.TempDir()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	gates := `custom.?gate|tenant.?isolation`
	out, runErr := captureStdout(t, func() error {
		return Run([]string{"lint.quality_gates=" + gates, "lint.brownfield=true"})
	})
	if runErr != nil {
		t.Fatalf("Run set: %v", runErr)
	}
	if !strings.Contains(out, "set lint.quality_gates = "+gates) {
		t.Errorf("set output missing lint.quality_gates confirmation, got %q", out)
	}
	if !strings.Contains(out, "set lint.brownfield = true") {
		t.Errorf("set output missing lint.brownfield confirmation, got %q", out)
	}

	// Values must persist to the settings file read by settings.LoadFile
	// (the same path internal/lint uses).
	loaded := LoadFile(filepath.Join(dir, ".vault", "knowledge", ".settings.yaml"))
	if loaded["lint.quality_gates"] != gates {
		t.Errorf("persisted lint.quality_gates: expected %q, got %q", gates, loaded["lint.quality_gates"])
	}
	if loaded["lint.brownfield"] != "true" {
		t.Errorf("persisted lint.brownfield: expected 'true', got %q", loaded["lint.brownfield"])
	}

	out, runErr = captureStdout(t, func() error { return Run([]string{"lint.quality_gates"}) })
	if runErr != nil {
		t.Fatalf("Run get lint.quality_gates after set: %v", runErr)
	}
	if got := strings.TrimSpace(out); got != gates {
		t.Fatalf("expected lint.quality_gates output %q, got %q", gates, got)
	}

	out, runErr = captureStdout(t, func() error { return Run([]string{"lint.brownfield"}) })
	if runErr != nil {
		t.Fatalf("Run get lint.brownfield after set: %v", runErr)
	}
	if got := strings.TrimSpace(out); got != "true" {
		t.Fatalf("expected lint.brownfield output 'true', got %q", got)
	}
}

func TestSyncNdConfig_UsesNdConfigSetArgsInOrder(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	projectRoot := t.TempDir()
	override := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	syncNdConfig(projectRoot, map[string]string{
		"workflow.fsm":             "true",
		"workflow.sequence":        "open,in_progress,closed",
		"workflow.exit_rules":      "blocked:open,in_progress;deferred:open,in_progress",
		"workflow.custom_statuses": "",
	})

	want := [][]string{
		{"nd", "--vault", override, "config", "set", "status.sequence", "open,in_progress,closed"},
		{"nd", "--vault", override, "config", "set", "status.exit_rules", "blocked:open,in_progress;deferred:open,in_progress"},
		{"nd", "--vault", override, "config", "set", "status.fsm", "true"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected nd config calls:\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestSyncNdConfig_FallsBackToDefaults(t *testing.T) {
	var calls [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	projectRoot := t.TempDir()
	override := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	syncNdConfig(projectRoot, map[string]string{
		"workflow.fsm": "true",
	})

	want := [][]string{
		{"nd", "--vault", override, "config", "set", "status.sequence", defaults["workflow.sequence"]},
		{"nd", "--vault", override, "config", "set", "status.exit_rules", defaults["workflow.exit_rules"]},
		{"nd", "--vault", override, "config", "set", "status.fsm", "true"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected nd config calls:\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestRunRestoresSettingsFileWhenNdSyncFails(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".vault", "knowledge", ".settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "workflow.fsm: false\n"
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	override := filepath.Join(t.TempDir(), "nd-vault")
	if err := os.Setenv("ND_VAULT_DIR", override); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Unsetenv("ND_VAULT_DIR") }()

	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) >= 6 && args[2] == "config" && args[3] == "set" && args[4] == "status.custom" {
			return exec.Command("sh", "-c", "echo invalid custom status >&2; exit 1")
		}
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	err = Run([]string{"workflow.fsm=true", "workflow.custom_statuses=Bad Name"})
	if err == nil {
		t.Fatal("expected Run to fail when nd sync rejects workflow config")
	}
	if !strings.Contains(err.Error(), "invalid custom status") {
		t.Fatalf("unexpected error: %v", err)
	}

	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != original {
		t.Fatalf("settings file not restored after sync failure:\n got: %q\nwant: %q", got, original)
	}
}
