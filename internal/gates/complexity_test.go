package gates

import (
	"os"
	"os/exec"
	"testing"
)

// --- parser unit tests (pure, no stubbing) ---

func TestParseLizardCSV(t *testing.T) {
	// lizard --csv columns:
	// NLOC,CCN,token,param,length,location,file,function,long_name,start,end
	out := `5,3,40,1,6,foo@10-15@a.go,a.go,foo,foo( int ),10,15
80,32,500,3,90,bar@20-110@a.go,a.go,bar,bar( ),20,110
12,16,90,0,14,baz@1-13@b.py,b.py,baz,baz(),1,13
`
	hits, err := parseLizardCSV(out)
	if err != nil {
		t.Fatalf("parseLizardCSV: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d: %+v", len(hits), hits)
	}
	if hits[1].CCN != 32 || hits[1].Symbol != "bar" || hits[1].Path != "a.go" || hits[1].Line != 20 {
		t.Errorf("hit[1] = %+v", hits[1])
	}
	if hits[2].CCN != 16 || hits[2].Symbol != "baz" || hits[2].Path != "b.py" {
		t.Errorf("hit[2] = %+v", hits[2])
	}
}

func TestParseGocyclo(t *testing.T) {
	out := `32 main run cmd/pvg/main.go:109
16 gates parseGocyclo internal/gates/complexity.go:130
3 gates filterExt internal/gates/complexity.go:250
`
	hits := parseGocyclo(out)
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d: %+v", len(hits), hits)
	}
	if hits[0].CCN != 32 || hits[0].Line != 109 || hits[0].Path != "cmd/pvg/main.go" {
		t.Errorf("hit[0] = %+v", hits[0])
	}
	if hits[1].CCN != 16 || hits[1].Symbol != "gates parseGocyclo" {
		t.Errorf("hit[1] = %+v", hits[1])
	}
}

func TestParseRadonJSON(t *testing.T) {
	out := `{"app.py":[{"name":"handler","classname":null,"complexity":18,"lineno":12},{"name":"helper","classname":"Worker","complexity":4,"lineno":40}]}`
	hits, err := parseRadonJSON(out)
	if err != nil {
		t.Fatalf("parseRadonJSON: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	// map iteration order is nondeterministic; index by symbol.
	bySym := map[string]complexityHit{}
	for _, h := range hits {
		bySym[h.Symbol] = h
	}
	if h := bySym["handler"]; h.CCN != 18 || h.Path != "app.py" || h.Line != 12 {
		t.Errorf("handler hit = %+v", h)
	}
	if h := bySym["Worker.helper"]; h.CCN != 4 {
		t.Errorf("Worker.helper hit = %+v", h)
	}
}

// --- band logic: warn vs block ---

func TestComplexityFindings_Bands(t *testing.T) {
	hits := []complexityHit{
		{Path: "a.go", Symbol: "low", CCN: 10},  // below warn
		{Path: "a.go", Symbol: "mid", CCN: 20},  // warn band
		{Path: "a.go", Symbol: "high", CCN: 35}, // block band
	}

	// block mode: high -> block, mid -> warn, low -> none.
	got := complexityFindings(hits, "block", 15, 30)
	if len(got) != 2 {
		t.Fatalf("block mode: expected 2 findings, got %d: %+v", len(got), got)
	}
	bySym := map[string]Finding{}
	for _, f := range got {
		bySym[f.Symbol] = f
	}
	if bySym["high"].Severity != "block" || bySym["high"].Threshold != 30 {
		t.Errorf("high finding = %+v", bySym["high"])
	}
	if bySym["mid"].Severity != "warn" || bySym["mid"].Threshold != 15 {
		t.Errorf("mid finding = %+v", bySym["mid"])
	}

	// warn mode: even high (>=block_cc) must be warn, never block.
	gotWarn := complexityFindings(hits, "warn", 15, 30)
	if len(gotWarn) != 2 {
		t.Fatalf("warn mode: expected 2 findings, got %d", len(gotWarn))
	}
	for _, f := range gotWarn {
		if f.Severity != "warn" {
			t.Errorf("warn mode produced non-warn finding: %+v", f)
		}
	}
}

// --- orchestration with stubbed tools ---

func TestRunComplexity_LizardPath(t *testing.T) {
	restore := stubTools(t, map[string]bool{"lizard": true}, map[string]string{
		"lizard": `80,32,500,3,90,bar@20-110@a.go,a.go,bar,bar(),20,110
5,16,40,1,6,foo@10-20@a.go,a.go,foo,foo(),10,20
`,
	})
	defer restore()

	findings, skip := runComplexity([]string{"a.go"}, "block", 15, 30)
	if skip != "" {
		t.Fatalf("unexpected skip note: %q", skip)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(findings), findings)
	}
}

func TestRunComplexity_GocycloFallback(t *testing.T) {
	// lizard absent, gocyclo present for .go files.
	restore := stubTools(t, map[string]bool{"gocyclo": true}, map[string]string{
		"gocyclo": "35 main big a.go:5\n",
	})
	defer restore()

	findings, skip := runComplexity([]string{"a.go"}, "block", 15, 30)
	if skip != "" {
		t.Fatalf("unexpected skip note: %q", skip)
	}
	if len(findings) != 1 || findings[0].Severity != "block" || findings[0].Value != 35 {
		t.Fatalf("expected one block finding (CCN 35), got %+v", findings)
	}
}

func TestRunComplexity_ToolAbsent(t *testing.T) {
	// No complexity tool available for the files in scope.
	restore := stubTools(t, map[string]bool{}, map[string]string{})
	defer restore()

	findings, skip := runComplexity([]string{"a.go"}, "block", 15, 30)
	if len(findings) != 0 {
		t.Fatalf("expected no findings when tool absent, got %+v", findings)
	}
	if skip == "" {
		t.Fatal("expected a skip note when no complexity tool is available")
	}
}

// --- integration-style: real lizard if present ---

func TestRunComplexity_RealLizard(t *testing.T) {
	if _, err := exec.LookPath("lizard"); err != nil {
		t.Skip("lizard not installed; skipping real-tool integration test")
	}
	// Run against this package's own source -- must not error and must not skip.
	_, skip := runComplexity([]string{"complexity.go"}, "block", 15, 30)
	if skip != "" {
		t.Fatalf("real lizard run produced skip note: %q", skip)
	}
}

// stubTools installs a fake lookPath + execCommand. available lists tools that
// "exist"; outputs maps a tool name to the stdout the fake should emit.
func stubTools(t *testing.T, available map[string]bool, outputs map[string]string) func() {
	t.Helper()
	origLook := lookPath
	origExec := execCommand

	lookPath = func(name string) (string, error) {
		if available[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		out := outputs[name]
		cmdArgs := []string{"-test.run=TestGatesHelperProcess", "--"}
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_GATES_HELPER=1",
			"GATES_HELPER_STDOUT="+out,
		)
		return cmd
	}

	return func() {
		lookPath = origLook
		execCommand = origExec
	}
}

// TestGatesHelperProcess is the fake subprocess used by stubTools. It writes
// the configured stdout and exits 0.
func TestGatesHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GATES_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("GATES_HELPER_STDOUT"))
	os.Exit(0)
}
