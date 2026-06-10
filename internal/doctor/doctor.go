// Package doctor runs diagnostic checks on paivot vault configuration
// and project health.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/ndvault"
)

// Status represents the outcome of a single check.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
	StatusSkip Status = "skip"
)

// Finding is the result of one diagnostic check.
type Finding struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Fixable bool   `json:"fixable,omitempty"`
}

// Report is the collection of all findings.
type Report struct {
	Findings []Finding `json:"findings"`
	Passed   bool      `json:"passed"`
}

// For testing: allow mocking exec.Command.
var execCommand = exec.Command

// RunAll executes all diagnostic checks and returns a Report.
func RunAll(projectRoot string) Report {
	var r Report

	r.Findings = append(r.Findings, checkVaultResolution(projectRoot))
	r.Findings = append(r.Findings, checkNDReachable())
	r.Findings = append(r.Findings, checkSharedConfigConsistency(projectRoot))
	r.Findings = append(r.Findings, checkNDDoctor(projectRoot))
	r.Findings = append(r.Findings, checkLoopState(projectRoot))
	r.Findings = append(r.Findings, checkWorktreeHygiene(projectRoot))

	r.Passed = true
	for _, f := range r.Findings {
		if f.Status == StatusFail {
			r.Passed = false
			break
		}
	}
	return r
}

// Fix attempts to auto-fix all fixable findings. Returns actions taken.
func Fix(projectRoot string, report Report) []string {
	var actions []string
	for _, f := range report.Findings {
		if !f.Fixable || f.Status != StatusFail {
			continue
		}
		switch f.Name {
		case "worktree-hygiene":
			if msg := fixStaleWorktrees(projectRoot); msg != "" {
				actions = append(actions, msg)
			}
		case "nd-doctor":
			if msg := fixNDDoctor(projectRoot); msg != "" {
				actions = append(actions, msg)
			}
		}
	}
	return actions
}

// FormatText returns a human-readable report.
func FormatText(r Report) string {
	var sb strings.Builder
	for _, f := range r.Findings {
		fmt.Fprintf(&sb, "[%s] %s: %s\n", strings.ToUpper(string(f.Status)), f.Name, f.Message)
	}
	if r.Passed {
		sb.WriteString("\nAll checks passed.\n")
	} else {
		sb.WriteString("\nSome checks failed. Run with --fix to auto-repair fixable issues.\n")
	}
	return sb.String()
}

// FormatJSON returns the report as indented JSON.
func FormatJSON(r Report) (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// --- check implementations ---

func checkVaultResolution(projectRoot string) Finding {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return Finding{Name: "vault-resolution", Status: StatusFail, Message: fmt.Sprintf("cannot resolve vault: %v", err)}
	}
	if info, err := os.Stat(vaultDir); err != nil || !info.IsDir() {
		return Finding{Name: "vault-resolution", Status: StatusFail, Message: fmt.Sprintf("resolved path does not exist: %s", vaultDir)}
	}
	ndYaml := filepath.Join(vaultDir, ".nd.yaml")
	if _, err := os.Stat(ndYaml); err != nil {
		return Finding{Name: "vault-resolution", Status: StatusFail, Message: fmt.Sprintf("vault at %s is missing .nd.yaml (not initialized)", vaultDir)}
	}
	return Finding{Name: "vault-resolution", Status: StatusPass, Message: fmt.Sprintf("resolved to %s", vaultDir)}
}

func checkNDReachable() Finding {
	cmd := execCommand("nd", "--version")
	out, err := cmd.Output()
	if err != nil {
		return Finding{Name: "nd-reachable", Status: StatusFail, Message: "nd binary not found or not executable"}
	}
	ver := strings.TrimSpace(string(out))
	return Finding{Name: "nd-reachable", Status: StatusPass, Message: ver}
}

func checkSharedConfigConsistency(projectRoot string) Finding {
	if !ndvault.SharedConfigured(projectRoot) {
		// Paivot-managed git repos must share the live vault across
		// worktrees; without the config every worktree resolves its own
		// vault view and nd writes diverge.
		if _, err := ndvault.FindRepoRoot(projectRoot); err == nil && ndvault.IsPaivotManaged(projectRoot) {
			return Finding{
				Name:    "shared-config-consistency",
				Status:  StatusWarn,
				Message: "no .vault/.nd-shared.yaml -- worktree nd writes will diverge; run 'pvg nd root --ensure' and commit the file",
			}
		}
		return Finding{Name: "shared-config-consistency", Status: StatusPass, Message: "local vault mode (non-git project)"}
	}

	// Shared config exists -- verify target path exists.
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return Finding{Name: "shared-config-consistency", Status: StatusFail, Message: fmt.Sprintf("shared config exists but vault resolution failed: %v", err)}
	}
	if _, err := os.Stat(vaultDir); err != nil {
		return Finding{Name: "shared-config-consistency", Status: StatusFail, Message: fmt.Sprintf("shared config points to nonexistent path: %s", vaultDir)}
	}
	return Finding{Name: "shared-config-consistency", Status: StatusPass, Message: fmt.Sprintf("shared vault at %s", vaultDir)}
}

func checkNDDoctor(projectRoot string) Finding {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return Finding{Name: "nd-doctor", Status: StatusSkip, Message: "skipped (vault resolution failed)"}
	}

	cmd := execCommand("nd", "doctor", "--vault", vaultDir)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		lines := strings.Count(output, "\n") + 1
		return Finding{Name: "nd-doctor", Status: StatusFail, Message: fmt.Sprintf("%d problem(s) found", lines), Fixable: true}
	}
	if output == "" {
		return Finding{Name: "nd-doctor", Status: StatusPass, Message: "no issues found"}
	}
	return Finding{Name: "nd-doctor", Status: StatusPass, Message: "no issues found"}
}

func checkLoopState(projectRoot string) Finding {
	statePath := loop.StatePath(projectRoot)
	if _, err := os.Stat(statePath); err != nil {
		return Finding{Name: "loop-state", Status: StatusSkip, Message: "no loop state file"}
	}

	state, err := loop.ReadState(projectRoot)
	if err != nil {
		return Finding{Name: "loop-state", Status: StatusFail, Message: fmt.Sprintf("invalid loop state: %v", err)}
	}

	desc := fmt.Sprintf("valid (%s mode", state.Mode)
	if state.TargetEpic != "" {
		desc += fmt.Sprintf(", target %s", state.TargetEpic)
	}
	if !state.Active {
		desc += ", inactive"
	}
	desc += ")"
	return Finding{Name: "loop-state", Status: StatusPass, Message: desc}
}

func checkWorktreeHygiene(projectRoot string) Finding {
	worktrees, err := loop.ListWorktrees(projectRoot)
	if err != nil {
		return Finding{Name: "worktree-hygiene", Status: StatusSkip, Message: fmt.Sprintf("cannot list worktrees: %v", err)}
	}
	if len(worktrees) == 0 {
		return Finding{Name: "worktree-hygiene", Status: StatusPass, Message: "no worktrees"}
	}

	var stale []string
	for _, wt := range worktrees {
		if _, err := os.Stat(wt.Path); err != nil {
			stale = append(stale, wt.Path)
		}
	}
	if len(stale) > 0 {
		return Finding{
			Name:    "worktree-hygiene",
			Status:  StatusFail,
			Message: fmt.Sprintf("%d stale worktree(s): %s", len(stale), strings.Join(stale, ", ")),
			Fixable: true,
		}
	}
	return Finding{Name: "worktree-hygiene", Status: StatusPass, Message: fmt.Sprintf("%d worktree(s), all valid", len(worktrees))}
}

// --- fix implementations ---

func fixStaleWorktrees(projectRoot string) string {
	cmd := execCommand("git", "worktree", "prune")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("git worktree prune failed: %v", err)
	}
	return "pruned stale worktrees"
}

func fixNDDoctor(projectRoot string) string {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return fmt.Sprintf("cannot resolve vault for nd doctor --fix: %v", err)
	}
	cmd := execCommand("nd", "doctor", "--fix", "--vault", vaultDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("nd doctor --fix failed: %v", err)
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return "nd doctor --fix completed (no output)"
	}
	return fmt.Sprintf("nd doctor --fix: %s", output)
}
