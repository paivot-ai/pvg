package guard

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/settings"
)

// dfArtifacts are the D&F file basenames that dispatcher mode protects.
var dfArtifacts = map[string]string{
	"BUSINESS.md":     "business-analyst",
	"DESIGN.md":       "designer",
	"ARCHITECTURE.md": "architect",
}

// modelithSuffix identifies a domain-model artifact. Unlike the fixed dfArtifacts
// basenames, the domain model has a variable name (e.g. domain.modelith.yaml),
// so it is matched by suffix. It is architect-owned, but protected only when the
// dnf.domain_model setting is enabled (mirrors the architecture.c4 opt-in).
const modelithSuffix = ".modelith.yaml"

// ndMutatingCommandRe covers bare nd and the sanctioned `pvg nd ...` wrapper.
var ndMutatingCommandRe = regexp.MustCompile(ndCmdPrefix + `(create|update|close|reopen|delete|defer|undefer|labels\s+(?:add|remove|rm)|comments\s+add|dep\s+(?:add|rm|relate|unrelate)|link|unlink)\b`)

// pvgIssuesMutatingRe covers the normalized `pvg issues ...` mutating forms.
var pvgIssuesMutatingRe = regexp.MustCompile(pvgIssuesPrefix + `(create|update|close|reopen|comment|link|unlink)\b`)

var dispatcherMutatingAgents = []string{
	"paivot-graph:sr-pm",
	"paivot-graph:developer",
	"paivot-graph:pm",
}

// CheckDispatcher enforces dispatcher mode: blocks D&F file writes when
// dispatcher mode is active and no BLT agent is currently tracked.
// Fail-open: if state file is missing or unreadable, allows the operation.
func CheckDispatcher(projectRoot string, input HookInput) Result {
	if projectRoot == "" {
		return Result{Allowed: true}
	}

	// root is the orchestrator project root that owns the dispatcher state.
	// projectRoot may be a subagent worktree; settings live at root (the
	// .vault/ tree is gitignored and absent from worktrees), while agent-vs-
	// worktree matching still uses projectRoot.
	state, root, err := dispatcher.ReadStateRoot(projectRoot)
	if err != nil || !state.Enabled {
		return Result{Allowed: true}
	}

	switch input.ToolName {
	case "Edit", "Write":
		return checkDFFilePath(projectRoot, root, state, input.ToolInput.FilePath)
	case "Bash":
		if result := checkDFBashCommand(projectRoot, root, state, input.ToolInput.Command); !result.Allowed {
			return result
		}
		return checkDispatcherNDMutation(projectRoot, state, input.ToolInput.Command)
	default:
		return Result{Allowed: true}
	}
}

func checkDFFilePath(projectRoot, root string, state *dispatcher.State, filePath string) Result {
	if filePath == "" {
		return Result{Allowed: true}
	}

	base := filepath.Base(filePath)
	agentName, isDFFile := dfAgentFor(root, base)
	if !isDFFile {
		return Result{Allowed: true}
	}

	if dfWriteAllowed(projectRoot, state, agentName) {
		return Result{Allowed: true}
	}

	return Result{
		Allowed: false,
		Reason:  dfBlockMsg(base, agentName),
	}
}

func checkDFBashCommand(projectRoot, root string, state *dispatcher.State, command string) Result {
	if command == "" {
		return Result{Allowed: true}
	}

	// Fixed-name D&F artifacts (BUSINESS.md, DESIGN.md, ARCHITECTURE.md).
	for artifact, agentName := range dfArtifacts {
		if !strings.Contains(command, artifact) {
			continue
		}
		if commandHasWriteOp(command, artifact) && !dfWriteAllowed(projectRoot, state, agentName) {
			return Result{
				Allowed: false,
				Reason:  dfBlockMsg(artifact, agentName),
			}
		}
	}

	// Domain model (variable *.modelith.yaml basename), architect-owned, only
	// when dnf.domain_model is enabled for the project.
	if strings.Contains(command, modelithSuffix) && domainModelEnabled(root) {
		if commandHasWriteOp(command, modelithSuffix) && !dfWriteAllowed(projectRoot, state, "architect") {
			return Result{
				Allowed: false,
				Reason:  dfBlockMsg("*.modelith.yaml", "architect"),
			}
		}
	}

	return Result{Allowed: true}
}

// commandHasWriteOp reports whether command appears to write to the given
// artifact token, via a redirect targeting it or a common write utility.
// The utility check is intentionally coarse (matches the artifact anywhere in
// the command); it mirrors the pre-existing heuristic.
func commandHasWriteOp(command, artifact string) bool {
	for _, op := range []string{">>", ">"} {
		if idx := strings.Index(command, op); idx >= 0 {
			if strings.Contains(command[idx:], artifact) {
				return true
			}
		}
	}
	for _, pattern := range []string{"tee ", "cp ", "mv ", "cat >", "sed -i", "perl -pi"} {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
}

// dfAgentFor resolves the owning BLT agent for a D&F artifact basename.
// Fixed artifacts come from dfArtifacts. A *.modelith.yaml domain model is
// architect-owned, but only when dnf.domain_model is enabled for the project
// (root is the orchestrator root that owns the settings file).
func dfAgentFor(root, base string) (string, bool) {
	if agentName, ok := dfArtifacts[base]; ok {
		return agentName, true
	}
	if strings.HasSuffix(base, modelithSuffix) && domainModelEnabled(root) {
		return "architect", true
	}
	return "", false
}

// domainModelEnabled reports whether dnf.domain_model is enabled for the project
// rooted at root. Reads the same settings file as the rest of pvg; defaults to
// false (feature off) when unset or unreadable.
func domainModelEnabled(root string) bool {
	s := settings.LoadFile(filepath.Join(root, settingsPath))
	v, ok := s["dnf.domain_model"]
	if !ok {
		v = settings.Default("dnf.domain_model")
	}
	return v == "true"
}

func dfWriteAllowed(projectRoot string, state *dispatcher.State, agentName string) bool {
	if projectRoot == "" {
		return false
	}
	return dispatcher.HasActiveAgentTypeAtPath(state, "paivot-graph:"+agentName, projectRoot)
}

func dfBlockMsg(artifact, agentName string) string {
	return fmt.Sprintf(
		"BLOCKED: Dispatcher mode is active. D&F artifacts must be produced by BLT agents.\n"+
			"Only the matching BLT agent may write each artifact.\n"+
			"Spawn the appropriate agent:\n"+
			"  - BUSINESS.md --> business-analyst agent\n"+
			"  - DESIGN.md --> designer agent\n"+
			"  - ARCHITECTURE.md --> architect agent\n\n"+
			"To write %s, spawn the %s agent.",
		artifact, agentName)
}

func checkDispatcherNDMutation(projectRoot string, state *dispatcher.State, command string) Result {
	if command == "" {
		return Result{Allowed: true}
	}
	if !ndMutatingCommandRe.MatchString(command) && !pvgIssuesMutatingRe.MatchString(command) {
		return Result{Allowed: true}
	}

	if dispatcherWriteAllowed(projectRoot, state) {
		return Result{Allowed: true}
	}

	// Epic completion gate exemption: the dispatcher itself closes and
	// accepts EPICS (pvg issues update EPIC_ID --status closed /
	// --add-label accepted, or pvg nd close EPIC_ID). Commands whose target
	// issues are all of type "epic" are allowed from the coordinator.
	if isEpicMutationCommand(projectRoot, command) {
		return Result{Allowed: true}
	}

	return Result{
		Allowed: false,
		Reason: "BLOCKED: Dispatcher mode is active. Mutating nd commands must be delegated to a tracked production agent.\n" +
			"The coordinator may read nd state, but story/backlog mutations must come from the responsible agent worktree.\n" +
			"Use:\n" +
			"  - sr-pm for story/backlog creation and repair\n" +
			"  - developer for delivery/progress updates\n" +
			"  - pm for accept/reject and close/reopen actions",
	}
}

// isEpicMutationCommand reports whether every issue targeted by the command
// has frontmatter type: epic. Commands with no extractable target IDs are not
// exempt.
func isEpicMutationCommand(projectRoot, command string) bool {
	ids := make(map[string]bool)
	if statusIDs, _, found := parseNdStatusChange(command); found {
		for _, id := range statusIDs {
			ids[id] = true
		}
	}
	if id, _, found := parseNdContractLabelAdd(command); found {
		ids[id] = true
	}
	if len(ids) == 0 {
		return false
	}
	for id := range ids {
		if ReadIssueType(projectRoot, id) != "epic" {
			return false
		}
	}
	return true
}

func dispatcherWriteAllowed(projectRoot string, state *dispatcher.State) bool {
	for _, agentType := range dispatcherMutatingAgents {
		if dispatcher.HasActiveAgentTypeAtPath(state, agentType, projectRoot) {
			return true
		}
	}
	return false
}
