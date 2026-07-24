package guard

import (
	"strings"
	"testing"
)

func TestCheckDesignMachinerySetting_BlocksMutations(t *testing.T) {
	blocked := []string{
		"pvg settings design.machinery=on",
		"pvg settings design.machinery=off",
		"pvg settings design.machinery=auto",
		"pvg settings design.machinery = on",
		"pvg settings staleness_days=10 design.machinery=on",
		"pvg settings set design.machinery=on", // wrong CLI form, same intent
		"cd /repo && pvg settings design.machinery=on",
	}
	for _, cmd := range blocked {
		r := CheckDesignMachinerySetting(cmd)
		if r.Allowed {
			t.Errorf("must block %q", cmd)
			continue
		}
		if !strings.Contains(r.Reason, "user-only decision") {
			t.Errorf("block reason must say user-only decision, got %q", r.Reason)
		}
		if !strings.Contains(r.Reason, "pvg settings design.machinery=on") {
			t.Errorf("block reason must show the command the user can run, got %q", r.Reason)
		}
	}
}

func TestCheckDesignMachinerySetting_AllowsReadsAndUnrelated(t *testing.T) {
	allowed := []string{
		"",
		"pvg settings",
		"pvg settings design.machinery", // read, not a mutation
		"pvg settings staleness_days=10",
		"pvg gates --changed origin/main",
		"echo design.machinery=on", // no pvg settings invocation
		"pvg settings loop.agent_resume=true; echo design.machinery=on has no settings cmd in segment",
	}
	for _, cmd := range allowed {
		if r := CheckDesignMachinerySetting(cmd); !r.Allowed {
			t.Errorf("must allow %q, blocked with %q", cmd, r.Reason)
		}
	}
}

// The full guard entrypoint must route Bash commands through the check.
func TestGuardCheck_BlocksDesignMachineryViaBash(t *testing.T) {
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: "pvg settings design.machinery=on"},
	}
	r := Check("", t.TempDir(), input)
	if r.Allowed {
		t.Fatal("guard must block agent-driven design.machinery changes")
	}
	if !strings.Contains(r.Reason, "user-only decision") {
		t.Fatalf("unexpected reason: %q", r.Reason)
	}
}
