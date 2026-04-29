package providercli

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/paivot-ai/pvg/internal/providers/ndadapter"
	_ "github.com/paivot-ai/pvg/internal/providers/vltadapter"
)

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
