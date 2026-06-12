package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJscpdReport(t *testing.T) {
	data := []byte(`{
      "statistics": {"total": {"percentage": 12.5}},
      "duplicates": [
        {"lines": 60, "firstFile": {"name": "a.go", "start": 10, "end": 70}},
        {"lines": 20, "firstFile": {"name": "b.go", "start": 1, "end": 21}}
      ]
    }`)
	report, err := parseJscpdReport(data)
	if err != nil {
		t.Fatalf("parseJscpdReport: %v", err)
	}
	if report.Statistics.Total.Percentage != 12.5 {
		t.Errorf("percentage = %v, want 12.5", report.Statistics.Total.Percentage)
	}
	if len(report.Duplicates) != 2 || report.Duplicates[0].duplicatedLines() != 60 {
		t.Errorf("duplicates = %+v", report.Duplicates)
	}
}

func TestDuplicationFindings_PctOrMinLines(t *testing.T) {
	// Percentage below max, but one clone exceeds min_lines => 1 finding.
	report := &jscpdReport{}
	report.Statistics.Total.Percentage = 5
	report.Duplicates = []jscpdClone{
		{Lines: 60, FirstFile: struct {
			Name  string `json:"name"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		}{Name: "a.go"}},
		{Lines: 10},
	}
	got := duplicationFindings(report, "block", 10, 50)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (clone over min_lines), got %d: %+v", len(got), got)
	}
	if got[0].Severity != "block" || got[0].Value != 60 || got[0].Path != "a.go" {
		t.Errorf("finding = %+v", got[0])
	}

	// Percentage over max, no clone over min_lines => 1 total finding.
	report2 := &jscpdReport{}
	report2.Statistics.Total.Percentage = 15
	report2.Duplicates = []jscpdClone{{Lines: 10}}
	got2 := duplicationFindings(report2, "warn", 10, 50)
	if len(got2) != 1 {
		t.Fatalf("expected 1 total finding, got %d: %+v", len(got2), got2)
	}
	if got2[0].Severity != "warn" || got2[0].Path != "(total)" {
		t.Errorf("total finding = %+v", got2[0])
	}

	// Both conditions => 2 findings (1 total + 1 clone).
	report3 := &jscpdReport{}
	report3.Statistics.Total.Percentage = 15
	report3.Duplicates = []jscpdClone{{Lines: 60}}
	got3 := duplicationFindings(report3, "block", 10, 50)
	if len(got3) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(got3), got3)
	}
}

func TestRunDuplication_ToolAbsent(t *testing.T) {
	restore := stubTools(t, map[string]bool{}, map[string]string{})
	defer restore()

	findings, skip := runDuplication([]string{"."}, "block", 10, 50)
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %+v", findings)
	}
	if !strings.Contains(skip, "duplication: jscpd not found") {
		t.Fatalf("expected jscpd-not-found skip, got %q", skip)
	}
	if !strings.Contains(skip, "npm install -g jscpd") {
		t.Fatalf("expected jscpd install hint in skip, got %q", skip)
	}
}

func TestRunDuplication_StubbedReport(t *testing.T) {
	// jscpd "present"; fake subprocess writes a report to the --output dir.
	restore := stubJscpd(t, `{"statistics":{"total":{"percentage":20.0}},"duplicates":[]}`)
	defer restore()

	findings, skip := runDuplication([]string{"."}, "block", 10, 50)
	if skip != "" {
		t.Fatalf("unexpected skip: %q", skip)
	}
	if len(findings) != 1 || findings[0].Severity != "block" || findings[0].Path != "(total)" {
		t.Fatalf("expected one block total finding, got %+v", findings)
	}
}

func TestRunDuplication_RealJscpd(t *testing.T) {
	if _, err := exec.LookPath("jscpd"); err != nil {
		t.Skip("jscpd not installed; skipping real-tool integration test")
	}
	dir := t.TempDir()
	// A file with no duplication -- must run without error or skip.
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc A() int { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, skip := runDuplication([]string{dir}, "warn", 10, 50)
	if skip != "" {
		t.Fatalf("real jscpd produced skip note: %q", skip)
	}
}

// stubJscpd installs a fake jscpd that writes the given report JSON to the
// jscpd-report.json file inside the --output directory passed in its args.
func stubJscpd(t *testing.T, reportJSON string) func() {
	t.Helper()
	origLook := lookPath
	origExec := execCommand

	lookPath = func(name string) (string, error) {
		if name == "jscpd" {
			return "/usr/bin/jscpd", nil
		}
		return "", exec.ErrNotFound
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Find --output <dir> in args.
		outDir := ""
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--output" {
				outDir = args[i+1]
			}
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestJscpdHelperProcess", "--")
		cmd.Env = append(os.Environ(),
			"GO_WANT_JSCPD_HELPER=1",
			"JSCPD_HELPER_OUTDIR="+outDir,
			"JSCPD_HELPER_REPORT="+reportJSON,
		)
		return cmd
	}

	return func() {
		lookPath = origLook
		execCommand = origExec
	}
}

// TestJscpdHelperProcess is the fake jscpd subprocess. It writes the report to
// <outdir>/jscpd-report.json.
func TestJscpdHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_JSCPD_HELPER") != "1" {
		return
	}
	outDir := os.Getenv("JSCPD_HELPER_OUTDIR")
	if outDir != "" {
		_ = os.WriteFile(filepath.Join(outDir, "jscpd-report.json"),
			[]byte(os.Getenv("JSCPD_HELPER_REPORT")), 0644)
	}
	os.Exit(0)
}
