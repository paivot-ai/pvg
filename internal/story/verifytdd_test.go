package story

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// tddRepo initializes a throwaway git repo on a `main` branch.
func tddRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-b", "main").CombinedOutput(); err != nil {
		// Older git without `-b`: init then rename after the first commit.
		if out2, err2 := exec.Command("git", "-C", root, "init").CombinedOutput(); err2 != nil {
			t.Skipf("git init failed (git unavailable?): %s / %s", out, out2)
		}
	}
	tddGitRun(t, root, "config", "commit.gpgsign", "false")
	return root
}

func tddGitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func tddCommit(t *testing.T, root, file, content, msg string) {
	t.Helper()
	full := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tddGitRun(t, root, "add", "-A")
	tddGitRun(t, root, "commit", "-m", msg)
}

func tddHead(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return firstLine(string(out))
}

func TestVerifyTDD_DetectsUnauthorizedTestEdit(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "foo.go", "package foo\n", "base")
	tddGitRun(t, root, "branch", "-M", "main")
	base := tddHead(t, root)

	tddCommit(t, root, "foo_test.go", "package foo // tests\n", "add failing tests\n\ntdd-red")
	tddCommit(t, root, "foo.go", "package foo\nvar X = 1\n", "implement X")           // prod only, no marker -> OK
	tddCommit(t, root, "foo_test.go", "package foo // weakened\n", "tweak the tests") // test edit, no marker -> violation

	result, err := VerifyTDD(root, VerifyTDDOptions{Range: base + "..HEAD"})
	if err != nil {
		t.Fatalf("VerifyTDD: %v", err)
	}
	if result.Commits != 3 {
		t.Errorf("expected 3 non-merge commits checked, got %d", result.Commits)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %d: %+v", len(result.Violations), result.Violations)
	}
	v := result.Violations[0]
	if v.Subject != "tweak the tests" {
		t.Errorf("violation subject = %q, want %q", v.Subject, "tweak the tests")
	}
	if len(v.Files) != 1 || v.Files[0] != "foo_test.go" {
		t.Errorf("violation files = %v, want [foo_test.go]", v.Files)
	}
}

func TestVerifyTDD_AuthorizedTestEditPasses(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "foo.go", "package foo\n", "base")
	base := tddHead(t, root)
	tddCommit(t, root, "foo_test.go", "package foo // fix flake\n", "repair flaky test\n\n[test-edit-authorized]")

	result, err := VerifyTDD(root, VerifyTDDOptions{Range: base + "..HEAD"})
	if err != nil {
		t.Fatalf("VerifyTDD: %v", err)
	}
	if len(result.Violations) != 0 {
		t.Errorf("authorized test edit must pass, got violations: %+v", result.Violations)
	}
}

func TestVerifyTDD_AllowsNewTestFileInGreen(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "foo.go", "package foo\n", "base")
	base := tddHead(t, root)

	// RED authors the original tests under the marker.
	tddCommit(t, root, "foo_test.go", "package foo // red\n", "author failing tests\n\ntdd-red")
	// GREEN implements...
	tddCommit(t, root, "foo.go", "package foo\nvar X = 1\n", "implement X")
	// ...and adds a NEW coverage test file with NO marker. This is an allowed
	// GREEN addition -- it cannot weaken the frozen RED test, which still runs.
	tddCommit(t, root, "extra_test.go", "package foo // extra coverage\n", "add coverage for X")

	result, err := VerifyTDD(root, VerifyTDDOptions{Range: base + "..HEAD"})
	if err != nil {
		t.Fatalf("VerifyTDD: %v", err)
	}
	if len(result.Violations) != 0 {
		t.Fatalf("adding a new test file in GREEN must pass unmarked, got violations: %+v", result.Violations)
	}
}

func TestVerifyTDD_FlagsDeletedRedTest(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "foo.go", "package foo\n", "base")
	base := tddHead(t, root)
	tddCommit(t, root, "foo_test.go", "package foo // red\n", "author tests\n\ntdd-red")

	// GREEN deletes the RED test file with no marker -- removing a RED test is
	// touching the frozen set and must be a violation, unlike a pure addition.
	if err := os.Remove(filepath.Join(root, "foo_test.go")); err != nil {
		t.Fatal(err)
	}
	tddGitRun(t, root, "add", "-A")
	tddGitRun(t, root, "commit", "-m", "drop the inconvenient test")

	result, err := VerifyTDD(root, VerifyTDDOptions{Range: base + "..HEAD"})
	if err != nil {
		t.Fatalf("VerifyTDD: %v", err)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("deleting a RED test must be a violation, got %d: %+v", len(result.Violations), result.Violations)
	}
	if len(result.Violations[0].Files) != 1 || result.Violations[0].Files[0] != "foo_test.go" {
		t.Errorf("violation files = %v, want [foo_test.go]", result.Violations[0].Files)
	}
}

func TestVerifyTDD_FailsLoudOnUnresolvableRange(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "foo.go", "package foo\n", "only commit")

	// A range whose endpoint cannot be resolved must error, not pass silently.
	if _, err := VerifyTDD(root, VerifyTDDOptions{Range: "HEAD~50..HEAD"}); err == nil {
		t.Error("expected error for unresolvable range, got nil (silent pass)")
	}

	// No range and no base is also a hard error, never a silent success.
	if _, err := VerifyTDD(root, VerifyTDDOptions{}); err == nil {
		t.Error("expected error when neither --range nor --base is given, got nil")
	}
}

func TestVerifyTDD_SkipsMergeCommits(t *testing.T) {
	root := tddRepo(t)
	tddCommit(t, root, "main.go", "package main\n", "base")
	tddGitRun(t, root, "branch", "-M", "main")
	base := tddHead(t, root)

	tddGitRun(t, root, "checkout", "-b", "feature")
	tddCommit(t, root, "foo_test.go", "package foo // tests\n", "author tests\n\ntdd-red")
	tddGitRun(t, root, "checkout", "main")
	// --no-ff forces a merge commit that itself carries no marker.
	tddGitRun(t, root, "merge", "--no-ff", "feature", "-m", "merge feature")

	result, err := VerifyTDD(root, VerifyTDDOptions{Range: base + "..HEAD"})
	if err != nil {
		t.Fatalf("VerifyTDD: %v", err)
	}
	if result.MergesSkip != 1 {
		t.Errorf("expected 1 merge commit skipped, got %d", result.MergesSkip)
	}
	if len(result.Violations) != 0 {
		t.Errorf("merge commit must not be flagged; the RED constituent carries its marker. got: %+v", result.Violations)
	}
}
