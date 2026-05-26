package providercli

import (
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

func TestOpenBacklog_LoadsConfigOrDefaults(t *testing.T) {
	dir := t.TempDir()
	// Empty .paivot dir is enough to anchor LocateProjectRoot, and Load
	// returns Defaults() for the missing config file.
	if err := os.Mkdir(filepath.Join(dir, ".paivot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

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
