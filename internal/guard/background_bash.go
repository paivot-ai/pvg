package guard

import (
	"github.com/paivot-ai/pvg/internal/dispatcher"
)

const backgroundBashBlockMsg = "BLOCKED: backgrounded Bash is not allowed while dispatcher mode is active.\n\n" +
	"If you are an ephemeral agent (developer/PM): backgrounding a command ENDS YOUR TURN, and an ended turn DISPOSES you -- subagents are never re-invoked when a background task finishes. Your uncommitted work is abandoned with you. This exact pattern has repeatedly killed developers mid-story on slow builds.\n\n" +
	"Run the command SYNCHRONOUSLY instead:\n" +
	"  - set an explicit timeout (up to 600000 ms = 10 minutes)\n" +
	"  - split longer pipelines into stages each under the timeout (deps -> compile -> test); incremental builds make re-runs cheap\n" +
	"  - COMMIT your work to the story branch BEFORE starting a long verify, so nothing is lost even if a stage times out\n\n" +
	"(The dispatcher backgrounds AGENTS via the Agent tool; it has no business backgrounding shell commands.)"

// CheckBackgroundBash blocks Bash run_in_background while dispatcher mode is
// active. Observed failure: developers on a CPU-saturated host backgrounded
// the in-container compile to "wait" for it, which ended their turn -- the
// harness reaped them after ~3 minutes as 'completed', abandoning intact but
// uncommitted work. Only the MAIN session is re-invoked when background
// tasks finish; ephemeral subagents are simply disposed.
func CheckBackgroundBash(projectRoot string, input HookInput) Result {
	if !input.ToolInput.RunInBackground {
		return Result{Allowed: true}
	}
	if projectRoot == "" {
		return Result{Allowed: true}
	}
	state, _, err := dispatcher.ReadStateRoot(projectRoot)
	if err != nil || !state.Enabled {
		return Result{Allowed: true}
	}
	return Result{Allowed: false, Reason: backgroundBashBlockMsg}
}
