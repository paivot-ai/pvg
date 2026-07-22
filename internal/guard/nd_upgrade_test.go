package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// paivotProjectRoot creates a temp project marked Paivot-managed via
// .vault/issues/.
func paivotProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vault", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCheckNDUpgrade_BlocksInPaivotProject(t *testing.T) {
	root := paivotProjectRoot(t)

	for _, command := range []string{
		"nd upgrade",
		"pvg nd upgrade",
		"nd upgrade --force",
		"/usr/local/bin/nd upgrade",
		"cd /tmp && nd upgrade",
		"echo hi; nd upgrade",
	} {
		t.Run(command, func(t *testing.T) {
			result := CheckNDUpgrade(root, command)
			if result.Allowed {
				t.Fatalf("expected %q blocked in Paivot project, got allowed", command)
			}
			if !strings.Contains(result.Reason, "version-pinned by the Paivot channel manifest") {
				t.Fatalf("unexpected reason: %s", result.Reason)
			}
			if !strings.Contains(result.Reason, "pvg update") {
				t.Fatalf("reason must point at pvg update, got: %s", result.Reason)
			}
		})
	}
}

func TestCheckNDUpgrade_BlocksWithPaivotConfigMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".paivot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".paivot", "config.yaml"), []byte("backlog:\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if result := CheckNDUpgrade(root, "nd upgrade"); result.Allowed {
		t.Fatal("expected nd upgrade blocked when .paivot/config.yaml exists")
	}
}

func TestCheckNDUpgrade_BlocksFromSubdirectoryOfPaivotProject(t *testing.T) {
	root := paivotProjectRoot(t)
	sub := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if result := CheckNDUpgrade(sub, "nd upgrade"); result.Allowed {
		t.Fatal("expected nd upgrade blocked from a subdirectory of a Paivot project")
	}
}

func TestCheckNDUpgrade_AllowsOutsidePaivotProject(t *testing.T) {
	if result := CheckNDUpgrade(t.TempDir(), "nd upgrade"); !result.Allowed {
		t.Fatalf("expected nd upgrade allowed outside a Paivot project, got: %s", result.Reason)
	}
}

func TestCheckNDUpgrade_AllowsOtherNDCommands(t *testing.T) {
	root := paivotProjectRoot(t)

	for _, command := range []string{
		"nd update PROJ-a1b --status open",
		"nd ready --json",
		"pvg update",
		"git commit -m \"nd upgrade blocked\"",
		"echo 'nd upgrade'",
	} {
		t.Run(command, func(t *testing.T) {
			if result := CheckNDUpgrade(root, command); !result.Allowed {
				t.Fatalf("expected %q allowed, got blocked: %s", command, result.Reason)
			}
		})
	}
}

func TestCheck_BashRoutesNDUpgradeThroughGuard(t *testing.T) {
	root := paivotProjectRoot(t)
	input := HookInput{
		ToolName:  "Bash",
		ToolInput: ToolInput{Command: "nd upgrade"},
	}
	result := Check("", root, input)
	if result.Allowed {
		t.Fatal("expected Check to block nd upgrade via CheckNDUpgrade")
	}
	if !strings.Contains(result.Reason, "version-pinned") {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}
