package lifecycle

import (
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/loop"
)

// TestDispatcherActivationContextCarriesTheMachinerySkip is G11 at the
// prompt-injection layer: the activation context is what survives context
// compaction, so the machinery-first rule must be in it, not only in the
// vault note.
func TestDispatcherActivationContextCarriesTheMachinerySkip(t *testing.T) {
	for _, want := range []string{
		"MACHINERY-FIRST PROJECTS",
		"design.machinery",
		"skip D&F entirely",
		"spawn no BLT agent",
		"never the Architect",
		"read-only for delivery agents",
	} {
		if !strings.Contains(dispatcherActivationContext, want) {
			t.Errorf("activation context must state %q", want)
		}
	}
	if strings.Contains(dispatcherActivationContext, "—") {
		t.Error("activation context contains an em dash")
	}
}

// TestStaticOperatingModeCarriesTheMachinerySkip: the session-start
// context is the other injection point a fresh dispatcher reads.
func TestStaticOperatingModeCarriesTheMachinerySkip(t *testing.T) {
	text := staticOperatingMode()
	for _, want := range []string{
		"MACHINERY DESIGN EXISTS",
		"Spawn NO BLT agent",
		"ESCALATION_FOR_ARCHITECT is disabled",
		"design/BUILD.md section 9",
		"oracles (machines and formal)",
		"read-only",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("session-start D&F orchestration must state %q", want)
		}
	}
}

// TestContinuationPromptNamesTheMilestoneSealGate is the G8 exit rule at the
// stop hook: a pending epic gate must also tell the dispatcher about the
// milestone seal that follows it, or the loop merges the last slice and
// exits with the milestone unsealed.
func TestContinuationPromptNamesTheMilestoneSealGate(t *testing.T) {
	state := &loop.State{Mode: "epic", TargetEpic: "PROJ-s2"}
	decision := &loop.StopDecision{NewIteration: 1, Reason: "Epic completion gate pending -- merge epic to main before exit"}

	prompt := BuildContinuationPrompt(state, decision, "10", &loop.WorkCounts{})

	for _, want := range []string{
		"MILESTONE SEAL GATE",
		"pvg gates --seal",
		"Anchor seal review",
		"seal_epic",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("continuation prompt must name %q:\n%s", want, prompt)
		}
	}
	// The four pre-existing gate steps are still there, in order.
	for _, step := range []string{"Step 1", "Step 2: Spawn Anchor milestone review", "Step 3: Merge epic/", "Step 4: Spawn retro agent"} {
		if !strings.Contains(prompt, step) {
			t.Errorf("the completion gate must keep %q", step)
		}
	}
}
