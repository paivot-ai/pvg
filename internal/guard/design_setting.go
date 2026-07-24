package guard

// design.machinery is strictly user-opt-in: enabling the machinery design
// substrate carries significant token/time cost, so the decision belongs to
// the user, never to an agent. The guard runs as a PreToolUse hook, meaning
// every command it sees is agent-driven by construction; a human running
// `pvg settings design.machinery=on` in their own terminal is never
// intercepted. Any agent attempt to change the setting is therefore blocked
// outright, in every direction (on, off, or auto).

import "regexp"

// designMachineryMutationRe matches a `pvg settings` invocation carrying a
// design.machinery=<value> assignment anywhere in the same shell command
// segment. A bare read (`pvg settings design.machinery`, no '=') is allowed.
var designMachineryMutationRe = regexp.MustCompile(`\bpvg\s+settings\b[^|;&]*\bdesign\.machinery\s*=`)

const designMachineryBlockMsg = "BLOCKED: design.machinery is a user-only decision.\n" +
	"Enabling (or changing) the machinery design substrate has significant token/time cost,\n" +
	"so agents must not flip it. If you believe the substrate should be enabled, recommend it\n" +
	"to the user instead; they can run it themselves:\n" +
	"  pvg settings design.machinery=on"

// CheckDesignMachinerySetting blocks agent-driven changes to the
// design.machinery setting via the pvg settings command.
func CheckDesignMachinerySetting(command string) Result {
	if command == "" {
		return Result{Allowed: true}
	}
	if designMachineryMutationRe.MatchString(command) {
		return Result{Allowed: false, Reason: designMachineryBlockMsg}
	}
	return Result{Allowed: true}
}
