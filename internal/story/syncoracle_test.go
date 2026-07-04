package story

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixture builds a machinery-managed repo whose HEAD carries the v1
// oracle, with the working tree already at v2: one modified row, one
// removed id, one added id.
func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	writeFile(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	v1 := `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-A-01 | ORD-aaaaaa | Open | on:pay | guardPaid | Paid | recordPayment |
| T-A-02 | ORD-bbbbbb | Open | on:cancel | - | Cancelled | recordCancel |
`
	writeFile(t, filepath.Join(root, "design", "machines", "Order.oracle.md"), v1)
	run("add", ".")
	run("commit", "-q", "-m", "v1")

	v2 := `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-A-01 | ORD-aaaaaa | Open | on:pay | guardPaid | Paid | recordPayment, emitReceipt |
| T-A-02 | ORD-cccccc | Open | on:hold | guardHoldable | Held | recordHold |
`
	writeFile(t, filepath.Join(root, "design", "machines", "Order.oracle.md"), v2)
	return root
}

func TestSyncOracleDiffAndStoryMapping(t *testing.T) {
	root := gitFixture(t)
	vault := t.TempDir()
	writeFile(t, filepath.Join(vault, "issues", "S-1.md"),
		"---\nid: S-1\nstatus: open\n---\nAC: transition ORD-aaaaaa emits a receipt.\n")
	writeFile(t, filepath.Join(vault, "issues", "S-2.md"),
		"---\nid: S-2\nstatus: closed\n---\nMentions ORD-cccccc but is closed.\n")
	t.Setenv("ND_VAULT_DIR", vault)

	rep, err := SyncOracle(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 3 {
		t.Fatalf("expected modified+removed+added, got %+v", rep.Entries)
	}
	byID := map[string]SyncEntry{}
	for _, e := range rep.Entries {
		byID[e.ID] = e
	}
	if e := byID["ORD-aaaaaa"]; e.Change != "modified" || len(e.Stories) != 1 || e.Stories[0] != "S-1" {
		t.Fatalf("ORD-aaaaaa: %+v", e)
	}
	if e := byID["ORD-bbbbbb"]; e.Change != "removed" || len(e.Stories) != 0 {
		t.Fatalf("ORD-bbbbbb: %+v", e)
	}
	if e := byID["ORD-cccccc"]; e.Change != "added" || len(e.Stories) != 0 {
		t.Fatalf("ORD-cccccc (closed stories never cover): %+v", e)
	}
	if rep.Uncovered != 2 {
		t.Fatalf("uncovered = %d, want 2", rep.Uncovered)
	}
	text := FormatSyncText(rep)
	for _, want := range []string{"modified", "ORD-bbbbbb", "NO COVERING STORY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text report missing %q:\n%s", want, text)
		}
	}
}

func TestSyncOracleUnchangedIsQuiet(t *testing.T) {
	root := gitFixture(t)
	// reset the working tree to HEAD so nothing changed
	cmd := exec.Command("git", "-C", root, "checkout", "-q", "--", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	rep, err := SyncOracle(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 0 {
		t.Fatalf("expected no entries, got %+v", rep.Entries)
	}
}

func TestSyncOracleRejectsBadRef(t *testing.T) {
	if _, err := SyncOracle(t.TempDir(), "--upload-pack=evil"); err == nil {
		t.Fatal("option-shaped refs must be rejected")
	}
}

func TestSyncOracleUnmanagedProject(t *testing.T) {
	if _, err := SyncOracle(t.TempDir(), "HEAD"); err == nil || !strings.Contains(err.Error(), "not machinery-managed") {
		t.Fatalf("unmanaged project must say so, got %v", err)
	}
}
