package gates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeverityFor(t *testing.T) {
	if severityFor("block") != "block" {
		t.Error("block mode should yield block severity")
	}
	if severityFor("warn") != "warn" {
		t.Error("warn mode should yield warn severity")
	}
	if severityFor("off") != "warn" {
		t.Error("non-block mode should default to warn severity")
	}
}

// Run with only the built-in LOC gate exercises aggregation and the
// Blocked flag without needing external tools. Complexity/duplication are
// turned off so no tool is required.
func TestRun_LOCOnly_WarnNotBlocked(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.go")
	if err := os.WriteFile(big, []byte(strings.Repeat("x\n", 30)), 0644); err != nil {
		t.Fatal(err)
	}

	sett := map[string]string{
		"gates.complexity":   "off",
		"gates.duplication":  "off",
		"gates.file_loc":     "warn",
		"gates.file_loc.max": "20",
	}
	report, err := Run([]string{dir}, sett)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(report.Findings), report.Findings)
	}
	if report.Blocked {
		t.Error("warn-only report must not be Blocked")
	}
}

func TestRun_LOCBlock_Blocked(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.go")
	if err := os.WriteFile(big, []byte(strings.Repeat("x\n", 30)), 0644); err != nil {
		t.Fatal(err)
	}

	sett := map[string]string{
		"gates.complexity":   "off",
		"gates.duplication":  "off",
		"gates.file_loc":     "block",
		"gates.file_loc.max": "20",
	}
	report, err := Run([]string{dir}, sett)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Blocked {
		t.Fatal("block-mode finding must set Blocked=true")
	}
}

// Excluded files are dropped before any metric runs.
func TestRun_ExcludeDropsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte(strings.Repeat("x\n", 30)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.pb.go"), []byte(strings.Repeat("x\n", 30)), 0644); err != nil {
		t.Fatal(err)
	}

	sett := map[string]string{
		"gates.complexity":   "off",
		"gates.duplication":  "off",
		"gates.file_loc":     "warn",
		"gates.file_loc.max": "20",
		"gates.exclude":      "*.pb.go",
	}
	report, err := Run([]string{dir}, sett)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected only keep.go to be scanned, got %+v", report.Findings)
	}
	if !strings.HasSuffix(report.Findings[0].Path, "keep.go") {
		t.Errorf("expected keep.go finding, got %s", report.Findings[0].Path)
	}
}

// Tool-absent complexity gate produces a SKIP note, not a failure.
func TestRun_ComplexityToolAbsent_SkipsNotFails(t *testing.T) {
	restore := stubTools(t, map[string]bool{}, map[string]string{})
	defer restore()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc A(){}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sett := map[string]string{
		"gates.complexity":  "block",
		"gates.duplication": "off",
		"gates.file_loc":    "off",
	}
	report, err := Run([]string{dir}, sett)
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocked {
		t.Error("absent complexity tool must NOT block")
	}
	if len(report.Skipped) == 0 {
		t.Fatal("expected a skip note for absent complexity tool")
	}
	if !strings.Contains(report.Skipped[0], "lizard not found") {
		t.Errorf("skip note = %q", report.Skipped[0])
	}
}

func TestFormatText(t *testing.T) {
	r := &Report{
		Findings: []Finding{
			{Metric: "complexity", Path: "a.go", Symbol: "bar", Value: 35, Threshold: 30, Severity: "block", Detail: "CCN 35"},
			{Metric: "file_loc", Path: "b.go", Value: 450, Threshold: 400, Severity: "warn"},
		},
		Skipped: []string{"duplication: jscpd not found"},
		Blocked: true,
	}
	out := FormatText(r)
	for _, want := range []string{
		"[BLOCK] complexity a.go bar 35>30",
		"[WARN] file_loc b.go 450>400",
		"[SKIP] duplication: jscpd not found",
		"GATES: FAIL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatText missing %q in:\n%s", want, out)
		}
	}

	// PASS path.
	pass := FormatText(&Report{Findings: []Finding{{Metric: "file_loc", Path: "b.go", Value: 401, Threshold: 400, Severity: "warn"}}})
	if !strings.Contains(pass, "GATES: PASS") {
		t.Errorf("expected PASS summary, got:\n%s", pass)
	}
}

func TestFormatJSON(t *testing.T) {
	r := &Report{}
	out, err := FormatJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Report
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("FormatJSON produced invalid JSON: %v", err)
	}
	// nil slices normalized to empty arrays.
	if !strings.Contains(out, `"findings": []`) || !strings.Contains(out, `"skipped": []`) {
		t.Errorf("expected empty arrays in JSON, got:\n%s", out)
	}
}
