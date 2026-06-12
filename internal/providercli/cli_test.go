package providercli

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "github.com/paivot-ai/pvg/internal/providers/ndadapter"
	_ "github.com/paivot-ai/pvg/internal/providers/vltadapter"
)

func TestReorderArgs_HoistsFlagsPastPositionals(t *testing.T) {
	known := map[string]bool{"body": true, "labels": true}
	got := reorderArgs(known, []string{"my title", "--body", "the body", "--labels", "x,y"})
	want := []string{"--body", "the body", "--labels", "x,y", "my title"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReorderArgs_HandlesEqualsForm(t *testing.T) {
	got := reorderArgs(nil, []string{"PS-001", "--json", "--status=open"})
	want := []string{"--json", "--status=open", "PS-001"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReorderArgs_StopsAtDoubleDash(t *testing.T) {
	got := reorderArgs(nil, []string{"--json", "--", "--literal", "stays"})
	want := []string{"--json", "--literal", "stays"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCSV(%q) len = %d, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestPropFlag(t *testing.T) {
	p := newPropFlag()
	if err := p.Set("type=pattern"); err != nil {
		t.Fatalf("Set type: %v", err)
	}
	if err := p.Set("status=active"); err != nil {
		t.Fatalf("Set status: %v", err)
	}
	if p.values["type"] != "pattern" || p.values["status"] != "active" {
		t.Errorf("values = %v", p.values)
	}
	if err := p.Set("invalid"); err == nil {
		t.Errorf("expected error on missing =")
	}
}

func TestIsFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	title := fs.String("title", "", "")
	body := fs.String("body", "", "")
	if err := fs.Parse([]string{"--title", "hello"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !isFlagSet(fs, "title") {
		t.Errorf("title should be marked set")
	}
	if isFlagSet(fs, "body") {
		t.Errorf("body should NOT be marked set")
	}
	_ = title
	_ = body
}

func TestRunIssues_NoArgsErrors(t *testing.T) {
	if err := RunIssues(nil); err == nil {
		t.Error("expected error when no subcommand")
	}
}

func TestRunIssuesCommentsHandlesNdMarkdownOutputWithHeadingBody(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeND := filepath.Join(bin, "nd")
	script := `#!/bin/sh
case "$*" in
  *"comments list HXT-heading --json"*)
    cat <<'EOF'
## Comments

### 2026-05-20T07:09:23Z tester
## Comment Heading
Comment body keeps markdown headings.
### Body Subheading
Still part of the same comment body.
EOF
    exit 0
    ;;
esac
echo "unexpected nd args: $*" >&2
exit 42
`
	if err := os.WriteFile(fakeND, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake nd: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ".paivot"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	config := "backlog:\n" +
		"  primary:\n" +
		"    adapter: nd\n" +
		"    config:\n" +
		"      vault: " + strconv.Quote(filepath.Join(dir, "vault")) + "\n"
	if err := os.WriteFile(filepath.Join(project, ".paivot", "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return RunIssues([]string{"comments", "HXT-heading"})
	})
	if err != nil {
		t.Fatalf("RunIssues comments: %v", err)
	}
	for _, fragment := range []string{"tester", "## Comment Heading", "### Body Subheading"} {
		if !strings.Contains(out, fragment) {
			t.Errorf("stdout missing %q: %s", fragment, out)
		}
	}
}

func TestRunNotes_NoArgsErrors(t *testing.T) {
	if err := RunNotes(nil); err == nil {
		t.Error("expected error when no subcommand")
	}
}

// setupFakeND installs a fake nd binary that logs every invocation's args to
// a file and responds per-subcommand, then chdirs into a project configured
// with the nd backlog adapter. Returns the args log path.
func setupFakeND(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logFile := filepath.Join(dir, "nd-args.log")
	script := `#!/bin/sh
echo "$*" >> "` + logFile + `"
case "$*" in
  *" list "*|*" list") printf '[]' ;;
  *"blocked --json"*) printf '[{"ID":"VP-9","Title":"Blocked story","Status":"open"}]' ;;
  *) printf '' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "nd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake nd: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ".paivot"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	config := "backlog:\n" +
		"  primary:\n" +
		"    adapter: nd\n" +
		"    config:\n" +
		"      vault: " + strconv.Quote(filepath.Join(dir, "vault")) + "\n"
	if err := os.WriteFile(filepath.Join(project, ".paivot", "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return logFile
}

func readArgsLog(t *testing.T, logFile string) string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	return string(data)
}

func TestRunIssuesList_PassesTypeAndSortToND(t *testing.T) {
	logFile := setupFakeND(t)

	_, err := captureStdout(t, func() error {
		return RunIssues([]string{"list", "--type", "epic", "--sort", "priority", "--json"})
	})
	if err != nil {
		t.Fatalf("RunIssues list: %v", err)
	}

	logged := readArgsLog(t, logFile)
	for _, fragment := range []string{"--type epic", "--sort priority"} {
		if !strings.Contains(logged, fragment) {
			t.Errorf("nd invocation missing %q: %s", fragment, logged)
		}
	}
}

func TestRunIssuesClose_PassesReasonToND(t *testing.T) {
	logFile := setupFakeND(t)

	_, err := captureStdout(t, func() error {
		return RunIssues([]string{"close", "VP-1", "--reason", "obsolete"})
	})
	if err != nil {
		t.Fatalf("RunIssues close: %v", err)
	}

	logged := readArgsLog(t, logFile)
	if !strings.Contains(logged, "close VP-1 --reason obsolete") {
		t.Errorf("nd invocation missing close reason: %s", logged)
	}
}

func TestRunIssuesClose_WithoutReason(t *testing.T) {
	logFile := setupFakeND(t)

	_, err := captureStdout(t, func() error {
		return RunIssues([]string{"close", "VP-1"})
	})
	if err != nil {
		t.Fatalf("RunIssues close: %v", err)
	}

	logged := readArgsLog(t, logFile)
	if !strings.Contains(logged, "close VP-1") {
		t.Errorf("nd close not invoked: %s", logged)
	}
	if strings.Contains(logged, "--reason") {
		t.Errorf("unexpected --reason flag without reason: %s", logged)
	}
}

func TestRunIssuesClose_MissingIDErrors(t *testing.T) {
	setupFakeND(t)
	if err := RunIssues([]string{"close", "--reason", "no id"}); err == nil {
		t.Error("expected error when close has no issue ID")
	}
}

func TestRunIssuesBlocked_JSONOutput(t *testing.T) {
	setupFakeND(t)

	out, err := captureStdout(t, func() error {
		return RunIssues([]string{"blocked", "--json"})
	})
	if err != nil {
		t.Fatalf("RunIssues blocked: %v", err)
	}

	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("expected JSON array output with --json, got %q", out)
	}
	if !strings.Contains(trimmed, `"VP-9"`) {
		t.Errorf("expected blocked issue in JSON output, got %q", out)
	}
}

func TestRunIssuesBlocked_TextOutputWithoutFlag(t *testing.T) {
	setupFakeND(t)

	out, err := captureStdout(t, func() error {
		return RunIssues([]string{"blocked"})
	})
	if err != nil {
		t.Fatalf("RunIssues blocked: %v", err)
	}

	trimmed := strings.TrimSpace(out)
	if strings.HasPrefix(trimmed, "[") {
		t.Fatalf("expected text output without --json, got %q", out)
	}
	if !strings.Contains(trimmed, "VP-9") {
		t.Errorf("expected blocked issue in text output, got %q", out)
	}
}

func TestRunIssuesShow_JSONIncludesAllBlockedBy(t *testing.T) {
	// nd's show --json surfaces an archived blocker in WasBlockedBy. The CLI's
	// --json output must expose was_blocked_by plus the lifetime union under
	// all_blocked_by so downstream consumers stop rediscovering it.
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := `#!/bin/sh
case "$*" in
  *"show VP-1 --json"*)
    printf '{"ID":"VP-1","Title":"Union story","Status":"open","BlockedBy":["VP-3"],"WasBlockedBy":["VP-2","VP-3"],"FilePath":"issues/VP-1.md"}'
    exit 0
    ;;
esac
echo "unexpected nd args: $*" >&2
exit 42
`
	if err := os.WriteFile(filepath.Join(bin, "nd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake nd: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ".paivot"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	config := "backlog:\n" +
		"  primary:\n" +
		"    adapter: nd\n" +
		"    config:\n" +
		"      vault: " + strconv.Quote(filepath.Join(dir, "vault")) + "\n"
	if err := os.WriteFile(filepath.Join(project, ".paivot", "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Keep the adapter pointed at the configured vault so the fake nd answers.
	oldEnsure := ensureNDVault
	ensureNDVault = func(string) (string, error) { return filepath.Join(dir, "vault"), nil }
	t.Cleanup(func() { ensureNDVault = oldEnsure })

	oldWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return RunIssues([]string{"show", "VP-1", "--json"})
	})
	if err != nil {
		t.Fatalf("RunIssues show: %v", err)
	}

	var got struct {
		WasBlockedBy []string `json:"was_blocked_by"`
		AllBlockedBy []string `json:"all_blocked_by"`
	}
	if uerr := json.Unmarshal([]byte(out), &got); uerr != nil {
		t.Fatalf("unmarshal show --json output %q: %v", out, uerr)
	}
	if want := []string{"VP-2", "VP-3"}; !equalStrings(got.WasBlockedBy, want) {
		t.Errorf("was_blocked_by = %v, want %v", got.WasBlockedBy, want)
	}
	if want := []string{"VP-2", "VP-3"}; !equalStrings(got.AllBlockedBy, want) {
		t.Errorf("all_blocked_by = %v, want %v (deduped + sorted union)", got.AllBlockedBy, want)
	}
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

func TestOpenBacklog_LoadsConfigOrDefaults(t *testing.T) {
	dir := t.TempDir()
	// Empty .paivot dir is enough to anchor LocateProjectRoot, and Load
	// returns Defaults() for the missing config file.
	if err := os.Mkdir(filepath.Join(dir, ".paivot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Vault resolution is exercised separately (TestNormalizeNDVault_*);
	// this test cares about config loading and router wiring.
	oldEnsure := ensureNDVault
	ensureNDVault = func(string) (string, error) { return filepath.Join(dir, ".vault"), nil }
	t.Cleanup(func() { ensureNDVault = oldEnsure })

	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	r, err := openBacklog()
	if err != nil {
		t.Fatalf("openBacklog: %v", err)
	}
	if r.Primary().Name() != "nd" {
		t.Errorf("primary = %q, want nd", r.Primary().Name())
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("read stdout: %v", readErr)
	}
	return string(out), runErr
}

func TestNormalizeNDVault_RewritesRelativeToSharedVault(t *testing.T) {
	old := ensureNDVault
	ensureNDVault = func(string) (string, error) { return "/repo/.git/paivot/nd-vault", nil }
	t.Cleanup(func() { ensureNDVault = old })

	// Default and relative paths are rewritten -- `pvg issues` must resolve
	// the same store the guard and loop read.
	cfg := map[string]interface{}{"vault": ".vault"}
	if err := normalizeNDVault("nd", cfg); err != nil {
		t.Fatalf("normalizeNDVault() error: %v", err)
	}
	if cfg["vault"] != "/repo/.git/paivot/nd-vault" {
		t.Fatalf("vault = %v, want shared vault", cfg["vault"])
	}

	empty := map[string]interface{}{}
	if err := normalizeNDVault("nd", empty); err != nil {
		t.Fatalf("normalizeNDVault() error: %v", err)
	}
	if empty["vault"] != "/repo/.git/paivot/nd-vault" {
		t.Fatalf("vault = %v, want shared vault for empty config", empty["vault"])
	}

	// Explicit absolute paths are overridden too (one live vault per repo;
	// ND_VAULT_DIR is the deliberate escape hatch).
	abs := map[string]interface{}{"vault": "/explicit/override"}
	if err := normalizeNDVault("nd", abs); err != nil {
		t.Fatalf("normalizeNDVault() error: %v", err)
	}
	if abs["vault"] != "/repo/.git/paivot/nd-vault" {
		t.Fatalf("vault = %v, configured absolute path must be overridden by the shared vault", abs["vault"])
	}

	// Non-nd adapters are untouched.
	linear := map[string]interface{}{"vault": ".vault"}
	if err := normalizeNDVault("linear", linear); err != nil {
		t.Fatalf("normalizeNDVault() error: %v", err)
	}
	if linear["vault"] != ".vault" {
		t.Fatalf("vault = %v, non-nd adapters must be untouched", linear["vault"])
	}
}
