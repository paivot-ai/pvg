package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a story merged into its epic branch by a prior session, but
// still tracked open in nd, was re-dispatched "from scratch" -- a developer
// could clobber the landed foundation. Landed stories must route to PM
// review (in_progress + delivered), never auto-close.
func TestReconcileLanded_RoutesMergedStoryToPMReview(t *testing.T) {
	projectRoot := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", projectRoot}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	run("branch", "epic/PROJ-epic1")
	run("checkout", "-q", "-b", "story/PROJ-s1", "epic/PROJ-epic1")
	if err := os.WriteFile(filepath.Join(projectRoot, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "feat: story work")
	run("checkout", "-q", "epic/PROJ-epic1")
	run("merge", "--no-ff", "story/PROJ-s1", "-m", "merge(story/PROJ-s1): integrate PROJ-s1")
	run("checkout", "-q", "main")

	override := filepath.Join(t.TempDir(), "shared-vault")
	t.Setenv("ND_VAULT_DIR", override)

	var mutations [][]string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "git" {
			return exec.Command(name, args...)
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "list --status open") {
			return exec.Command("echo", `[
				{"ID":"PROJ-s1","Status":"open","Parent":"PROJ-epic1","Type":"task","Labels":["hard-tdd"]},
				{"ID":"PROJ-s2","Status":"open","Parent":"PROJ-epic1","Type":"task","Labels":[]},
				{"ID":"PROJ-s3","Status":"open","Parent":"PROJ-other","Type":"task","Labels":[]},
				{"ID":"PROJ-epic1","Status":"open","Parent":"","Type":"epic","Labels":[]}
			]`)
		}
		mutations = append(mutations, append([]string{name}, args...))
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = oldExec })

	reroutes, err := ReconcileLanded(projectRoot)
	if err != nil {
		t.Fatalf("ReconcileLanded() error: %v", err)
	}
	// Only PROJ-s1 is merged; PROJ-s2 has no merge commit, PROJ-s3's epic
	// branch does not exist, PROJ-epic1 is an epic.
	if len(reroutes) != 1 || reroutes[0].StoryID != "PROJ-s1" || reroutes[0].Epic != "epic/PROJ-epic1" {
		t.Fatalf("expected one reroute for PROJ-s1, got %#v", reroutes)
	}
	if reroutes[0].Commit == "" {
		t.Fatal("expected the merge commit hash to be reported")
	}

	var joined []string
	for _, m := range mutations {
		joined = append(joined, strings.Join(m, " "))
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{
		"update PROJ-s1 --status in_progress",
		"labels add PROJ-s1 delivered",
		"labels add PROJ-s1 red-approved", // hard-tdd: pending review is GREEN
		"comments add PROJ-s1",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing mutation %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "PROJ-s2") || strings.Contains(all, "PROJ-s3") {
		t.Fatalf("must not mutate unmerged stories:\n%s", all)
	}
	if strings.Contains(all, "close") {
		t.Fatalf("landed stories must never be auto-closed:\n%s", all)
	}
}
