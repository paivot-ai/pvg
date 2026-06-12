package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountNonBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	content := "a\n\n  \nb\nc\n" // 3 non-blank lines (a, b, c)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := countNonBlankLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("countNonBlankLines = %d, want 3", n)
	}
}

func TestRunLOC_Threshold(t *testing.T) {
	dir := t.TempDir()

	small := filepath.Join(dir, "small.go")
	if err := os.WriteFile(small, []byte(strings.Repeat("x\n", 10)), 0644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.go")
	if err := os.WriteFile(big, []byte(strings.Repeat("x\n", 50)), 0644); err != nil {
		t.Fatal(err)
	}

	// max=20: big (50) fires, small (10) does not.
	findings := runLOC([]string{small, big}, "warn", 20)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Path != big || findings[0].Value != 50 || findings[0].Severity != "warn" {
		t.Errorf("finding = %+v", findings[0])
	}

	// block mode => block severity.
	blockFindings := runLOC([]string{big}, "block", 20)
	if len(blockFindings) != 1 || blockFindings[0].Severity != "block" {
		t.Fatalf("expected block-severity finding, got %+v", blockFindings)
	}

	// At exactly the threshold (>=), it fires.
	exact := filepath.Join(dir, "exact.go")
	if err := os.WriteFile(exact, []byte(strings.Repeat("x\n", 20)), 0644); err != nil {
		t.Fatal(err)
	}
	if got := runLOC([]string{exact}, "warn", 20); len(got) != 1 {
		t.Errorf("file at exactly threshold should fire, got %+v", got)
	}
}
