package story

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oracleFixture = `## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-DEAL-01 | DEAL-eb0c40 | Lead | on:advanceStage | guardCanAdvance | persisting | setPendingAdvance |
| T-DEAL-02 | DEAL-38ba11 | Lead | on:advanceStage | - | (internal) | recordAdvanceDenied |
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeMachinery puts a stub machinery binary on PATH that exits with the
// given code, so designRedGate's shell-out is deterministic.
func fakeMachinery(t *testing.T, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexit " + map[int]string{0: "0", 1: "1"}[exitCode] + "\n"
	if err := os.WriteFile(filepath.Join(dir, "machinery"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// managedProject builds a machinery-managed fixture with one oracle and the
// user's explicit opt-in (design.machinery defaults to off; artifact
// presence alone never enables the substrate).
func managedProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), "design.machinery: auto\n")
	writeFile(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeFile(t, filepath.Join(root, "design", "machines", "Deal.oracle.md"), oracleFixture)
	return root
}

func TestDesignRedGateNotManagedIsNoop(t *testing.T) {
	note, err := designRedGate(t.TempDir(), "S-1", "any body")
	if err != nil || note != "" {
		t.Fatalf("unmanaged project must be a no-op, got note=%q err=%v", note, err)
	}
}

func TestDesignRedGateDefaultOffIsNoop(t *testing.T) {
	// Machinery artifacts but no settings: design.machinery defaults to off,
	// so the gate must not run.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	writeFile(t, filepath.Join(root, "design", "machines", "Deal.oracle.md"), oracleFixture)
	note, err := designRedGate(root, "S-1", "references DEAL-eb0c40")
	if err != nil || note != "" {
		t.Fatalf("default off must be a no-op despite artifacts, got note=%q err=%v", note, err)
	}

	// Explicit off behaves identically.
	writeFile(t, filepath.Join(root, ".vault", "knowledge", ".settings.yaml"), "design.machinery: off\n")
	note, err = designRedGate(root, "S-1", "references DEAL-eb0c40")
	if err != nil || note != "" {
		t.Fatalf("explicit off must be a no-op, got note=%q err=%v", note, err)
	}
}

func TestDesignRedGateBlocksOnRedCheck(t *testing.T) {
	root := managedProject(t)
	fakeMachinery(t, 1)
	_, err := designRedGate(root, "S-1", "references DEAL-eb0c40")
	if err == nil || !strings.Contains(err.Error(), "design gate is red") {
		t.Fatalf("a red design must block approval, got %v", err)
	}
}

func TestDesignRedGateBlocksOnMissingIDCoverage(t *testing.T) {
	root := managedProject(t)
	fakeMachinery(t, 0)
	// the story references both ids; only one is carried by a test
	writeFile(t, filepath.Join(root, "internal", "deal_test.go"), "package deal\n// covers DEAL-eb0c40\n")
	_, err := designRedGate(root, "S-1", "ACs: DEAL-eb0c40 and DEAL-38ba11")
	if err == nil || !strings.Contains(err.Error(), "DEAL-38ba11") {
		t.Fatalf("an uncovered referenced id must block with the id named, got %v", err)
	}
}

func TestDesignRedGatePassesWithFullCoverage(t *testing.T) {
	root := managedProject(t)
	fakeMachinery(t, 0)
	writeFile(t, filepath.Join(root, "tests", "deal_lifecycle_test.py"), "# DEAL-eb0c40\n# DEAL-38ba11\n")
	note, err := designRedGate(root, "S-1", "ACs: DEAL-eb0c40, DEAL-38ba11")
	if err != nil {
		t.Fatalf("full coverage must pass: %v", err)
	}
	if !strings.Contains(note, "2 oracle stable id(s) covered") {
		t.Fatalf("note must report coverage, got %q", note)
	}
}

func TestDesignRedGateNoReferencedIDs(t *testing.T) {
	root := managedProject(t)
	fakeMachinery(t, 0)
	note, err := designRedGate(root, "S-1", "a story about CRUD screens, no machine ids")
	if err != nil {
		t.Fatalf("no referenced ids reduces to check green: %v", err)
	}
	if !strings.Contains(note, "not applicable") {
		t.Fatalf("note must say id-coverage was not applicable, got %q", note)
	}
}
