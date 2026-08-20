package guard

import "testing"

// TestCheckDispatcher_BashAllowsNDHelpFromCoordinator is the G16 contract:
// asking an nd or `pvg issues` subcommand for its help text mutates nothing.
// Blocking it stops the coordinator from reading the very CLI it is required
// to instruct agents with.
func TestCheckDispatcher_BashAllowsNDHelpFromCoordinator(t *testing.T) {
	root, _ := setupDispatcher(t)
	tests := []string{
		`pvg nd create --help`,
		`nd create --help`,
		`pvg nd update -h`,
		`pvg issues create --help`,
		`pvg nd dep add --help`,
		`nd labels add --help | head -30`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			input := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: command}}
			if result := CheckDispatcher(root, input); !result.Allowed {
				t.Fatalf("read-only help must be allowed, got blocked: %s", result.Reason)
			}
		})
	}
}

// TestCheckDispatcher_BashStillBlocksMutationBesideHelp: a help flag on one
// segment never launders a mutation in another, and a help mention inside a
// body string is not a help flag.
func TestCheckDispatcher_BashStillBlocksMutationBesideHelp(t *testing.T) {
	root, _ := setupDispatcher(t)
	closeCmd := "pvg nd " + "close PROJ-a1b2"
	tests := []string{
		`pvg nd create --help; ` + closeCmd,
		closeCmd + ` && pvg nd create --help`,
		`pvg nd create "Story" --body "see the --help output for options"`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			input := HookInput{ToolName: "Bash", ToolInput: ToolInput{Command: command}}
			if result := CheckDispatcher(root, input); result.Allowed {
				t.Fatalf("a mutation beside help must still block: %q", command)
			}
		})
	}
}
