package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/gates"
	"github.com/paivot-ai/pvg/internal/loop"
)

// --- vault-resolution ---

func TestCheckVaultResolution_Pass(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".nd.yaml"), []byte("prefix: TEST\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := checkVaultResolution(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckVaultResolution_FailNoVault(t *testing.T) {
	root := t.TempDir()
	f := checkVaultResolution(root)
	if f.Status != StatusFail {
		t.Fatalf("expected fail, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckVaultResolution_FailNoNdYaml(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := checkVaultResolution(root)
	if f.Status != StatusFail {
		t.Fatalf("expected fail, got %s: %s", f.Status, f.Message)
	}
}

// --- nd-reachable ---

func TestCheckNDReachable_Pass(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "nd" {
			return exec.Command("echo", "nd version v0.10.5")
		}
		return exec.Command(name, args...)
	}

	f := checkNDReachable()
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckNDReachable_Fail(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "nd" {
			return exec.Command("false")
		}
		return exec.Command(name, args...)
	}

	f := checkNDReachable()
	if f.Status != StatusFail {
		t.Fatalf("expected fail, got %s: %s", f.Status, f.Message)
	}
}

// --- shared-config-consistency ---

func TestCheckSharedConfigConsistency_NoConfig(t *testing.T) {
	root := t.TempDir()
	// No .vault at all -- local vault mode is the default, should pass.
	f := checkSharedConfigConsistency(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass (local vault mode is default), got %s: %s", f.Status, f.Message)
	}
}

func TestCheckSharedConfigConsistency_PaivotManagedNoShared(t *testing.T) {
	root := t.TempDir()
	// Paivot markers but no git repo -- local vault mode is acceptable.
	if err := os.MkdirAll(filepath.Join(root, ".vault", "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := checkSharedConfigConsistency(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass (local vault mode for non-git project), got %s: %s", f.Status, f.Message)
	}
}

func TestCheckSharedConfigConsistency_GitPaivotManagedNoShared(t *testing.T) {
	root := t.TempDir()
	// Paivot-managed git repo without .nd-shared.yaml -- worktree nd writes
	// would diverge, so the doctor must warn.
	if err := os.MkdirAll(filepath.Join(root, ".vault", "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := checkSharedConfigConsistency(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn (git repo without shared vault config), got %s: %s", f.Status, f.Message)
	}
}

// --- vault-divergence ---

func setupDivergedVaults(t *testing.T) (root, localVault, sharedVault string) {
	t.Helper()
	root = t.TempDir()
	localVault = filepath.Join(root, ".vault")
	sharedVault = filepath.Join(root, ".git", "paivot", "nd-vault")

	// Shared mode configured and initialized.
	if err := os.MkdirAll(filepath.Join(localVault, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sharedVault, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "# nd shared-worktree state\nmode: git_common_dir\npath: paivot/nd-vault\n"
	if err := os.WriteFile(filepath.Join(localVault, ".nd-shared.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedVault, ".nd.yaml"), []byte("vault: shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, localVault, sharedVault
}

func TestCheckVaultDivergence_LegacyMarkerFails(t *testing.T) {
	root, localVault, sharedVault := setupDivergedVaults(t)

	// Legacy local vault still initialized, with a diverging issue file.
	if err := os.WriteFile(filepath.Join(localVault, ".nd.yaml"), []byte("vault: legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localVault, "issues", "TIX-1.md"), []byte("status: open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedVault, "issues", "TIX-1.md"), []byte("status: closed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := checkVaultDivergence(root)
	if f.Status != StatusFail || !f.Fixable {
		t.Fatalf("expected fixable fail, got %s (fixable=%v): %s", f.Status, f.Fixable, f.Message)
	}
	if !strings.Contains(f.Message, "1 overlapping issue file(s) differ") {
		t.Fatalf("expected divergent issue count in message, got: %s", f.Message)
	}

	// --fix decommissions the marker but keeps legacy issue files.
	msg := fixVaultDivergence(root)
	if !strings.Contains(msg, "removed legacy marker") {
		t.Fatalf("fix did not decommission: %s", msg)
	}
	if _, err := os.Stat(filepath.Join(localVault, ".nd.yaml")); !os.IsNotExist(err) {
		t.Fatalf("legacy marker still present (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(localVault, "issues", "TIX-1.md")); err != nil {
		t.Fatalf("legacy issue files must remain: %v", err)
	}

	// After the fix the marker is gone but stale issue files remain -- the
	// check degrades to a WARN pointing at them rather than a silent pass.
	if f := checkVaultDivergence(root); f.Status != StatusWarn {
		t.Fatalf("expected warn after fix (stale issue files remain), got %s: %s", f.Status, f.Message)
	}

	if err := os.Remove(filepath.Join(localVault, "issues", "TIX-1.md")); err != nil {
		t.Fatal(err)
	}
	if f := checkVaultDivergence(root); f.Status != StatusPass {
		t.Fatalf("expected pass after stale files removed, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckVaultDivergence_CleanSharedModePasses(t *testing.T) {
	root, _, _ := setupDivergedVaults(t)
	f := checkVaultDivergence(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass (no legacy marker), got %s: %s", f.Status, f.Message)
	}
}

func TestCheckVaultDivergence_LocalModePasses(t *testing.T) {
	root := t.TempDir()
	localVault := filepath.Join(root, ".vault")
	if err := os.MkdirAll(localVault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localVault, ".nd.yaml"), []byte("vault: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := checkVaultDivergence(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass (single local vault), got %s: %s", f.Status, f.Message)
	}
}

// --- snapshot-drift ---

func TestSnapshotDrift(t *testing.T) {
	cases := []struct {
		name        string
		live        []string
		snapshot    []string
		wantMissing []string
		wantExtra   []string
	}{
		{
			name:     "in sync",
			live:     []string{"TIX-1", "TIX-2"},
			snapshot: []string{"TIX-2", "TIX-1"}, // order-independent
		},
		{
			name:        "live has extra (mid-epic additions)",
			live:        []string{"TIX-1", "TIX-2", "TIX-3"},
			snapshot:    []string{"TIX-1"},
			wantMissing: []string{"TIX-2", "TIX-3"},
		},
		{
			name:      "snapshot has extra (deletions)",
			live:      []string{"TIX-1"},
			snapshot:  []string{"TIX-1", "TIX-2"},
			wantExtra: []string{"TIX-2"},
		},
		{
			name:        "both diverge",
			live:        []string{"TIX-1", "TIX-3"},
			snapshot:    []string{"TIX-1", "TIX-2"},
			wantMissing: []string{"TIX-3"},
			wantExtra:   []string{"TIX-2"},
		},
		{
			name: "both empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, extra := snapshotDrift(tc.live, tc.snapshot)
			if !equalStrings(missing, tc.wantMissing) {
				t.Errorf("missingFromSnapshot = %v, want %v", missing, tc.wantMissing)
			}
			if !equalStrings(extra, tc.wantExtra) {
				t.Errorf("extraInSnapshot = %v, want %v", extra, tc.wantExtra)
			}
		})
	}
}

// writeIssues creates issuesDir and an empty <id>.md for each id.
func writeIssues(t *testing.T, issuesDir string, ids ...string) {
	t.Helper()
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(issuesDir, id+".md"), []byte("status: open\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckSnapshotDrift_DriftWarns(t *testing.T) {
	root := t.TempDir()
	// Live vault with 3 issues, snapshot with 2 of them -> 1 missing.
	vaultDir := filepath.Join(root, ".vault")
	if err := os.WriteFile(filepath.Join(mkdir(t, vaultDir), ".nd.yaml"), []byte("prefix: TIX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIssues(t, filepath.Join(vaultDir, "issues"), "TIX-1", "TIX-2", "TIX-3")
	writeIssues(t, filepath.Join(root, ".vault", "backlog-snapshot", "issues"), "TIX-1", "TIX-2")

	f := checkSnapshotDrift(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn, got %s: %s", f.Status, f.Message)
	}
	for _, want := range []string{"1 live issue(s) not in .vault/backlog-snapshot", "created since last export", "pvg nd sync --commit"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("warn message missing %q in %q", want, f.Message)
		}
	}
}

func TestCheckSnapshotDrift_InSyncPasses(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, ".vault")
	if err := os.WriteFile(filepath.Join(mkdir(t, vaultDir), ".nd.yaml"), []byte("prefix: TIX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIssues(t, filepath.Join(vaultDir, "issues"), "TIX-1", "TIX-2")
	writeIssues(t, filepath.Join(root, ".vault", "backlog-snapshot", "issues"), "TIX-1", "TIX-2")

	f := checkSnapshotDrift(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckSnapshotDrift_NeverExportedWarns(t *testing.T) {
	root := t.TempDir()
	// Live vault populated, no snapshot dir at all.
	vaultDir := filepath.Join(root, ".vault")
	if err := os.WriteFile(filepath.Join(mkdir(t, vaultDir), ".nd.yaml"), []byte("prefix: TIX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIssues(t, filepath.Join(vaultDir, "issues"), "TIX-1")

	f := checkSnapshotDrift(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn, got %s: %s", f.Status, f.Message)
	}
	for _, want := range []string{"never been exported", "pvg nd sync --commit"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("warn message missing %q in %q", want, f.Message)
		}
	}
}

func TestCheckSnapshotDrift_NoSnapshotNoLiveSkips(t *testing.T) {
	root := t.TempDir()
	// .vault exists but is not initialized (no .nd.yaml) and no snapshot dir.
	if err := os.MkdirAll(filepath.Join(root, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := checkSnapshotDrift(root)
	if f.Status != StatusSkip {
		t.Fatalf("expected skip, got %s: %s", f.Status, f.Message)
	}
}

// mkdir creates dir (and parents) and returns it for fluent chaining.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- loop-state ---

func TestCheckLoopState_NoState(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := checkLoopState(root)
	if f.Status != StatusSkip {
		t.Fatalf("expected skip, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckLoopState_ValidState(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := loop.NewState("epic", "TEST-abc", 0)
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(loop.StatePath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}

	f := checkLoopState(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
}

func TestCheckLoopState_InvalidJSON(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loop.StatePath(root), []byte("{broken json"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := checkLoopState(root)
	if f.Status != StatusFail {
		t.Fatalf("expected fail, got %s: %s", f.Status, f.Message)
	}
}

// --- worktree-hygiene ---

func TestCheckWorktreeHygiene_NoWorktrees(t *testing.T) {
	// ListWorktrees shells out to git. In a non-git dir it errors -> skip.
	root := t.TempDir()
	f := checkWorktreeHygiene(root)
	if f.Status != StatusSkip && f.Status != StatusPass {
		t.Fatalf("expected skip or pass, got %s: %s", f.Status, f.Message)
	}
}

// --- code-quality-analyzers ---

func TestCheckAnalyzers_AllPresent(t *testing.T) {
	orig := analyzersMissing
	defer func() { analyzersMissing = orig }()
	analyzersMissing = func() []gates.Analyzer { return nil }

	f := checkAnalyzers()
	if f.Status != StatusPass {
		t.Fatalf("expected pass when analyzers present, got %s: %s", f.Status, f.Message)
	}
	if !strings.Contains(f.Message, "lizard") || !strings.Contains(f.Message, "jscpd") {
		t.Errorf("pass message should name the analyzers, got %q", f.Message)
	}
}

func TestCheckAnalyzers_MissingWarnsNeverFails(t *testing.T) {
	orig := analyzersMissing
	defer func() { analyzersMissing = orig }()
	analyzersMissing = func() []gates.Analyzer {
		return []gates.Analyzer{
			{Name: "lizard", Install: "pip install lizard", Recommended: true},
			{Name: "jscpd", Install: "npm install -g jscpd", Recommended: true},
		}
	}

	f := checkAnalyzers()
	if f.Status != StatusWarn {
		t.Fatalf("expected warn (never fail) when analyzers missing, got %s", f.Status)
	}
	for _, want := range []string{"pip install lizard", "npm install -g jscpd", "apt alone is not enough", "only radon ships"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("warn message missing %q in %q", want, f.Message)
		}
	}
}

// --- formatting ---

func TestFormatText(t *testing.T) {
	r := Report{
		Findings: []Finding{
			{Name: "vault-resolution", Status: StatusPass, Message: "resolved"},
			{Name: "nd-doctor", Status: StatusFail, Message: "5 problems", Fixable: true},
		},
		Passed: false,
	}
	out := FormatText(r)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !contains(out, "[PASS]") || !contains(out, "[FAIL]") {
		t.Fatalf("expected status markers in output: %s", out)
	}
}

func TestFormatJSON(t *testing.T) {
	r := Report{
		Findings: []Finding{
			{Name: "test", Status: StatusPass, Message: "ok"},
		},
		Passed: true,
	}
	out, err := FormatJSON(r)
	if err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}

	var parsed Report
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(parsed.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(parsed.Findings))
	}
}

// --- RunAll integration ---

func TestRunAll_ProducesReport(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	origAnalyzers := analyzersMissing
	defer func() { analyzersMissing = origAnalyzers }()
	analyzersMissing = func() []gates.Analyzer { return nil } // pretend all present

	execCommand = func(name string, args ...string) *exec.Cmd {
		switch name {
		case "nd":
			if len(args) > 0 && args[0] == "--version" {
				return exec.Command("echo", "nd version v0.10.5")
			}
			// nd doctor -- simulate success
			return exec.Command("true")
		case "modelith":
			// modelith --version
			return exec.Command("echo", "modelith version 0.4.0")
		case "git":
			// git worktree list -- empty porcelain output
			return exec.Command("echo", "")
		}
		return exec.Command(name, args...)
	}

	root := t.TempDir()
	vaultDir := filepath.Join(root, ".vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".nd.yaml"), []byte("prefix: TEST\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := RunAll(root)
	if len(r.Findings) != 10 {
		t.Fatalf("expected 10 findings, got %d", len(r.Findings))
	}

	names := make(map[string]bool)
	for _, f := range r.Findings {
		names[f.Name] = true
		if f.Name == "" {
			t.Error("finding with empty name")
		}
		if f.Status == "" {
			t.Errorf("finding %q has empty status", f.Name)
		}
		t.Logf("[%s] %s: %s", f.Status, f.Name, f.Message)
	}

	for _, expected := range []string{"vault-resolution", "nd-reachable", "modelith-reachable", "shared-config-consistency", "snapshot-drift", "nd-doctor", "loop-state", "worktree-hygiene", "code-quality-analyzers"} {
		if !names[expected] {
			t.Errorf("missing check %q", expected)
		}
	}
}

// --- Fix ---

func TestFix_PrunesWorktrees(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	var pruned bool
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) > 0 && args[0] == "worktree" {
			pruned = true
			return exec.Command("true")
		}
		return exec.Command("true")
	}

	report := Report{
		Findings: []Finding{
			{Name: "worktree-hygiene", Status: StatusFail, Fixable: true, Message: "1 stale"},
		},
	}

	actions := Fix(t.TempDir(), report)
	if !pruned {
		t.Error("expected git worktree prune to be called")
	}
	if len(actions) == 0 {
		t.Error("expected at least one action")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && fmt.Sprintf("%s", s) != "" && len(substr) > 0 && findSubstr(s, substr)
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
