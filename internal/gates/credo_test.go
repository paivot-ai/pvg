package gates

import (
	"os"
	"strings"
	"testing"
)

const credoJSON = `Compiling 3 files (.ex)
{"issues":[
 {"check":"Credo.Check.Refactor.CyclomaticComplexity","message":"Function body is too complex (CC is 14, max is 9).","filename":"lib/hextropian/policy.ex","line_no":42,"trigger":"decide/3"},
 {"check":"Credo.Check.Refactor.CyclomaticComplexity","message":"Function body is too complex (CC is 10, max is 9).","filename":"lib/hextropian/erasure.ex","line_no":7,"trigger":"cascade/1"},
 {"check":"Credo.Check.Readability.ModuleDoc","message":"Modules should have a @moduledoc tag.","filename":"lib/hextropian/policy.ex","line_no":1,"trigger":"Hextropian.Policy"}
]}`

func TestParseCredoJSON(t *testing.T) {
	hits, err := parseCredoJSON(credoJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("only CyclomaticComplexity issues become hits, got %+v", hits)
	}
	if hits[0].CCN != 14 || hits[0].Path != "lib/hextropian/policy.ex" || hits[0].Symbol != "decide/3" || hits[0].Line != 42 {
		t.Errorf("hit[0] = %+v", hits[0])
	}
	if hits[1].CCN != 10 {
		t.Errorf("hit[1] = %+v", hits[1])
	}

	// No JSON at all (credo missing, compile failure): no hits, no panic.
	hits, err = parseCredoJSON("** (Mix) The task \"credo\" could not be found")
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestSplitElixir(t *testing.T) {
	ex, other := splitElixir([]string{"lib/a.ex", "test/a_test.exs", "cmd/main.go", "app/b.py"})
	if len(ex) != 2 || len(other) != 2 {
		t.Fatalf("ex=%v other=%v", ex, other)
	}
	if !isElixir("lib/A.EX") {
		t.Error("extension match is case-insensitive")
	}
}

// TestElixirComplexityNeverSilentlyPasses is the G15 contract: lizard has no
// Elixir support, so handing it .ex files reads as zero functions. Elixir
// files take the credo path, and without credo they are reported as skipped
// by name -- never as a clean pass.
func TestElixirComplexityNeverSilentlyPasses(t *testing.T) {
	// lizard present, credo absent (no mix): the Elixir files are named in
	// a skip note even though lizard "ran" over the rest.
	restore := stubTools(t, map[string]bool{"lizard": true}, map[string]string{
		"lizard": "5,3,40,1,6,foo@10-15@a.go,a.go,foo,foo( int ),10,15\n",
	})
	defer restore()

	findings, note := runComplexity([]string{"lib/policy.ex", "a.go"}, "block", 10, 15)
	if !strings.Contains(note, "not measured") || !strings.Contains(note, "credo") {
		t.Fatalf("Elixir files must be reported as unmeasured: %q", note)
	}
	if !strings.Contains(note, "1 Elixir file(s)") {
		t.Errorf("note must count the skipped files: %q", note)
	}
	_ = findings
}

func TestElixirComplexityUsesCredoWhenAvailable(t *testing.T) {
	// A mix project with credo vendored: mix + deps/credo present.
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/deps/credo", 0o755); err != nil {
		t.Fatal(err)
	}
	origStat := statPath
	statPath = func(name string) (os.FileInfo, error) { return os.Stat(dir + "/" + name) }
	defer func() { statPath = origStat }()

	restore := stubTools(t, map[string]bool{"mix": true}, map[string]string{"mix": credoJSON})
	defer restore()

	if !credoAvailable() {
		t.Fatal("mix + deps/credo means credo is available")
	}
	findings, note := runComplexity([]string{"lib/hextropian/policy.ex", "lib/hextropian/erasure.ex"}, "block", 10, 12)
	if note != "" {
		t.Fatalf("credo ran; no skip note expected: %q", note)
	}
	if len(findings) != 2 {
		t.Fatalf("expected a finding per over-threshold function, got %+v", findings)
	}
	var blocked, warned int
	for _, f := range findings {
		if f.Metric != "complexity" {
			t.Errorf("metric = %q", f.Metric)
		}
		switch f.Severity {
		case "block":
			blocked++
		case "warn":
			warned++
		}
	}
	if blocked != 1 || warned != 1 {
		t.Errorf("CC 14 blocks (>=12) and CC 10 warns (>=10): %+v", findings)
	}
}

func TestCredoAvailableRequiresMix(t *testing.T) {
	restore := stubTools(t, map[string]bool{}, nil)
	defer restore()
	if credoAvailable() {
		t.Fatal("no mix on PATH means no credo")
	}
}
