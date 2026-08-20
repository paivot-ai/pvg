package governance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readSeededNote(t *testing.T, vaultDir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vaultDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSessionOperatingModeCarriesTheMachinerySkipCondition is the G11
// contract: the seeded orchestration note must give the dispatcher a
// "machinery design exists" skip condition, or a dispatcher following the
// note pushes Full or Light D&F over a design that is already finished.
func TestSessionOperatingModeCarriesTheMachinerySkipCondition(t *testing.T) {
	vaultDir := t.TempDir()
	counters := &Counters{}
	seedSessionOperatingMode(vaultDir, "2026-08-20", false, counters)
	if counters.Created != 1 {
		t.Fatalf("counters: %+v", counters)
	}

	note := readSeededNote(t, vaultDir, filepath.Join("conventions", "Session Operating Mode.md"))
	for _, want := range []string{
		"MACHINERY DESIGN EXISTS",
		"design.machinery",
		"no BLT agent",
		"ESCALATION_FOR_ARCHITECT route is disabled",
		"pvg issues create --type epic",
		"pvg rtm --milestone",
		"pvg lint --backlog",
		"READ-ONLY for every delivery agent",
		"pvg story sync-oracle",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the skip condition must state %q", want)
		}
	}

	// The skip condition belongs to the skip list, not to a stray section.
	skipIdx := strings.Index(note, "When D&F is not needed (skip entirely):")
	machIdx := strings.Index(note, "MACHINERY DESIGN EXISTS")
	if skipIdx < 0 || machIdx < skipIdx {
		t.Errorf("the machinery condition must sit inside the skip list (skip=%d machinery=%d)", skipIdx, machIdx)
	}

	// House style: no em dashes anywhere in seeded prose.
	if strings.Contains(note, "—") {
		t.Error("seeded note contains an em dash")
	}
}

// TestSeededNotesAcknowledgeVaultIntegrity is the G12 contract: pvg writes
// its own notes directly, so it must re-register them with vlt's integrity
// tracker. Otherwise every seeded note reads as tampered forever.
func TestSeededNotesAcknowledgeVaultIntegrity(t *testing.T) {
	vaultDir := t.TempDir()

	var calls [][]string
	oldExec, oldLook := execCommand, lookPath
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	lookPath = func(string) (string, error) { return "/usr/bin/vlt", nil }
	t.Cleanup(func() { execCommand, lookPath = oldExec, oldLook })

	counters := &Counters{}
	seedSessionOperatingMode(vaultDir, "2026-08-20", false, counters)
	if len(calls) != 1 {
		t.Fatalf("expected one integrity acknowledgement on create, got %v", calls)
	}
	want := []string{"vlt", "vault=" + vaultDir, "integrity:acknowledge", "file=Session Operating Mode"}
	if strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got %v want %v", calls[0], want)
	}

	// A skipped note was not written, so nothing is acknowledged.
	calls = nil
	seedSessionOperatingMode(vaultDir, "2026-08-20", false, counters)
	if len(calls) != 0 {
		t.Fatalf("a skipped note must not be acknowledged: %v", calls)
	}

	// A forced rewrite is a pvg write and is acknowledged again.
	seedSessionOperatingMode(vaultDir, "2026-08-20", true, counters)
	if len(calls) != 1 {
		t.Fatalf("a forced update must be acknowledged: %v", calls)
	}
}

// TestIntegrityAcknowledgementIsBestEffort: no vlt on PATH is not a seeding
// failure.
func TestIntegrityAcknowledgementIsBestEffort(t *testing.T) {
	vaultDir := t.TempDir()
	oldExec, oldLook := execCommand, lookPath
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("vlt must not be invoked when it is absent: %v %v", name, args)
		return nil
	}
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { execCommand, lookPath = oldExec, oldLook })

	counters := &Counters{}
	seedSessionOperatingMode(vaultDir, "2026-08-20", false, counters)
	if counters.Created != 1 {
		t.Fatalf("the note is still written: %+v", counters)
	}
}
