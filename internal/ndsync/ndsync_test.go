package ndsync

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newProjectRoot creates a temp git repo to act as the project root.
func newProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
	return root
}

// newVault creates a temp nd vault with .nd.yaml and the given issue files.
func newVault(t *testing.T, issues map[string]string) string {
	t.Helper()
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"),
		[]byte("version: \"1\"\nprefix: PROJ\ncreated_by: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range issues {
		if err := os.WriteFile(filepath.Join(vault, "issues", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func issueContent(id string) string {
	return "---\nid: " + id + "\nstatus: open\ntype: task\n---\nbody of " + id + "\n"
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestSync_ExportsIssuesConfigAndReadme(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{
		"PROJ-a.md": issueContent("PROJ-a"),
		"PROJ-b.md": issueContent("PROJ-b"),
	})

	res, err := Sync(root, vault)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Issues != 2 {
		t.Errorf("Issues = %d, want 2", res.Issues)
	}
	if res.SnapshotDir != SnapshotDir(root) {
		t.Errorf("SnapshotDir = %s", res.SnapshotDir)
	}

	snap := SnapshotDir(root)
	if got := readFile(t, filepath.Join(snap, "issues", "PROJ-a.md")); got != issueContent("PROJ-a") {
		t.Errorf("snapshot issue content mismatch: %q", got)
	}
	if got := readFile(t, filepath.Join(snap, ".nd.yaml")); !strings.Contains(got, "prefix: PROJ") {
		t.Errorf("snapshot .nd.yaml mismatch: %q", got)
	}

	readme := readFile(t, filepath.Join(snap, "README.md"))
	for _, want := range []string{"point-in-time EXPORT", "live queue is the shared", "Never hand-edit", "pvg nd restore", "pvg nd sync"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README missing %q:\n%s", want, readme)
		}
	}
}

func TestSync_MirrorRemovesStaleSnapshotFiles(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{
		"PROJ-a.md": issueContent("PROJ-a"),
		"PROJ-b.md": issueContent("PROJ-b"),
	})

	if _, err := Sync(root, vault); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	// Delete one live issue and modify another, then re-sync.
	if err := os.Remove(filepath.Join(vault, "issues", "PROJ-b.md")); err != nil {
		t.Fatal(err)
	}
	updated := issueContent("PROJ-a") + "updated\n"
	if err := os.WriteFile(filepath.Join(vault, "issues", "PROJ-a.md"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(root, vault)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if res.Issues != 1 || res.Removed != 1 {
		t.Errorf("Issues = %d, Removed = %d, want 1/1", res.Issues, res.Removed)
	}

	snap := SnapshotDir(root)
	if _, err := os.Stat(filepath.Join(snap, "issues", "PROJ-b.md")); !os.IsNotExist(err) {
		t.Error("stale snapshot file was not removed")
	}
	if got := readFile(t, filepath.Join(snap, "issues", "PROJ-a.md")); got != updated {
		t.Errorf("snapshot not refreshed: %q", got)
	}
}

func TestSync_UninitializedVaultFails(t *testing.T) {
	root := newProjectRoot(t)
	vault := t.TempDir() // no .nd.yaml

	if _, err := Sync(root, vault); err == nil {
		t.Fatal("expected error for uninitialized vault")
	}
}

func TestSync_EmptyVaultExportsZero(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, nil)

	res, err := Sync(root, vault)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Issues != 0 {
		t.Errorf("Issues = %d, want 0", res.Issues)
	}
}

func TestRestore_IntoEmptyVault(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{"PROJ-a.md": issueContent("PROJ-a")})
	if _, err := Sync(root, vault); err != nil {
		t.Fatal(err)
	}

	// Fresh vault: directory does not exist yet (e.g. fresh clone, shared dir).
	freshVault := filepath.Join(t.TempDir(), "paivot", "nd-vault")

	res, err := Restore(root, freshVault, false)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Issues != 1 {
		t.Errorf("Issues = %d, want 1", res.Issues)
	}
	if !res.ConfigRestored {
		t.Error("expected .nd.yaml to be restored into a vault that lacks one")
	}
	if got := readFile(t, filepath.Join(freshVault, "issues", "PROJ-a.md")); got != issueContent("PROJ-a") {
		t.Errorf("restored issue mismatch: %q", got)
	}
	if got := readFile(t, filepath.Join(freshVault, ".nd.yaml")); !strings.Contains(got, "prefix: PROJ") {
		t.Errorf("restored config mismatch: %q", got)
	}
}

func TestRestore_DoesNotOverwriteExistingConfig(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{"PROJ-a.md": issueContent("PROJ-a")})
	if _, err := Sync(root, vault); err != nil {
		t.Fatal(err)
	}

	// Target vault has its own .nd.yaml but no issues.
	target := newVault(t, nil)
	custom := "version: \"1\"\nprefix: OTHER\ncreated_by: someone\n"
	if err := os.WriteFile(filepath.Join(target, ".nd.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(root, target, false)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.ConfigRestored {
		t.Error("must not replace an existing .nd.yaml")
	}
	if got := readFile(t, filepath.Join(target, ".nd.yaml")); got != custom {
		t.Errorf("existing .nd.yaml was modified: %q", got)
	}
}

func TestRestore_RefusesNonEmptyVaultWithoutForce(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{"PROJ-a.md": issueContent("PROJ-a")})
	if _, err := Sync(root, vault); err != nil {
		t.Fatal(err)
	}

	target := newVault(t, map[string]string{"PROJ-x.md": issueContent("PROJ-x")})

	_, err := Restore(root, target, false)
	if err == nil {
		t.Fatal("expected refusal for non-empty live vault")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal message should mention --force: %v", err)
	}
	// Existing issues untouched.
	if _, err := os.Stat(filepath.Join(target, "issues", "PROJ-x.md")); err != nil {
		t.Errorf("existing issue should be untouched: %v", err)
	}
}

func TestRestore_ForceReplacesExistingIssues(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{"PROJ-a.md": issueContent("PROJ-a")})
	if _, err := Sync(root, vault); err != nil {
		t.Fatal(err)
	}

	target := newVault(t, map[string]string{
		"PROJ-x.md": issueContent("PROJ-x"),
		"PROJ-y.md": issueContent("PROJ-y"),
	})

	res, err := Restore(root, target, true)
	if err != nil {
		t.Fatalf("Restore --force: %v", err)
	}
	if res.Issues != 1 || res.Replaced != 2 {
		t.Errorf("Issues = %d, Replaced = %d, want 1/2", res.Issues, res.Replaced)
	}
	if _, err := os.Stat(filepath.Join(target, "issues", "PROJ-x.md")); !os.IsNotExist(err) {
		t.Error("force restore should remove pre-existing issues")
	}
	if _, err := os.Stat(filepath.Join(target, "issues", "PROJ-a.md")); err != nil {
		t.Errorf("snapshot issue missing after force restore: %v", err)
	}
}

func TestRestore_NoSnapshotFails(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, nil)

	if _, err := Restore(root, vault, false); err == nil {
		t.Fatal("expected error when no snapshot exists")
	}
}

func TestSyncRestore_RoundTrip(t *testing.T) {
	root := newProjectRoot(t)
	vault := newVault(t, map[string]string{
		"PROJ-a.md": issueContent("PROJ-a"),
		"PROJ-b.md": issueContent("PROJ-b"),
		"PROJ-c.md": issueContent("PROJ-c"),
	})

	if _, err := Sync(root, vault); err != nil {
		t.Fatal(err)
	}

	// Simulate vault loss.
	if err := os.RemoveAll(filepath.Join(vault, "issues")); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(root, vault, false)
	if err != nil {
		t.Fatalf("Restore after vault loss: %v", err)
	}
	if res.Issues != 3 {
		t.Errorf("Issues = %d, want 3", res.Issues)
	}
	for _, id := range []string{"PROJ-a", "PROJ-b", "PROJ-c"} {
		if got := readFile(t, filepath.Join(vault, "issues", id+".md")); got != issueContent(id) {
			t.Errorf("round-trip content mismatch for %s", id)
		}
	}
}
