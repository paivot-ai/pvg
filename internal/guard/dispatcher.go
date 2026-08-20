package guard

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/paivot-ai/pvg/internal/design"
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
	//
	// Enforce ONLY when dispatcher state exists and is enabled: without state
	// there is no agent tracking, so enforcement would block legitimate
	// subagent mutations (an Sr PM spawned by /intake could not run
	// `pvg issues create`). `pvg loop setup` enables dispatcher mode itself,
	// so loop runs are covered WITH tracking rather than without it.
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

	// The machinery design tree has its own rule (read-only for delivery
	// agents); when it applies it decides the write outright.
	if result, applied := checkDesignTreeWrite(projectRoot, root, state, filePath); applied {
		return result
	}

	artifact, agentName, isDFFile := dfArtifactForPath(root, filePath)
	if !isDFFile {
		return Result{Allowed: true}
	}

	if dfWriteAllowed(projectRoot, state, agentName) {
		return Result{Allowed: true}
	}

	return Result{
		Allowed: false,
		Reason:  dfBlockMsg(artifact, agentName),
	}
}

// checkDFBashCommand guards WRITE INTENT at a resolved path: only the paths
// the command actually writes to (see bashWriteTargets) are tested against the
// D&F artifact set. A command that merely NAMES an artifact -- a grep/awk read,
// or a heredoc whose prose mentions "BUSINESS.md" while writing another file --
// is not a write and passes.
func checkDFBashCommand(projectRoot, root string, state *dispatcher.State, command string) Result {
	if command == "" {
		return Result{Allowed: true}
	}

	for _, target := range bashWriteTargets(command) {
		if result, applied := checkDesignTreeWrite(projectRoot, root, state, target); applied {
			if !result.Allowed {
				return result
			}
			continue
		}
		artifact, agentName, isDFFile := dfArtifactForPath(root, target)
		if !isDFFile {
			continue
		}
		if dfWriteAllowed(projectRoot, state, agentName) {
			continue
		}
		return Result{
			Allowed: false,
			Reason:  dfBlockMsg(artifact, agentName),
		}
	}

	return Result{Allowed: true}
}

// isFixturePath reports whether filePath lives under a fixture directory: any
// path segment equal to "evals" or "testdata". Fixture copies of D&F artifact
// basenames (eval harness inputs like evals/x/inputs/BUSINESS.md, Go testdata)
// are test data, not D&F artifacts, and must not trip the guard.
func isFixturePath(filePath string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(filePath), "/") {
		if seg == "evals" || seg == "testdata" {
			return true
		}
	}
	return false
}

// isMachineryDesignPath reports whether filePath lives under the machinery
// design tree (design/ by default, or the "design" key of .machinery.json).
//
// The two toolchains use the same basename for different documents: Paivot's
// architect owns ARCHITECTURE.md at the repo root, while machinery owns
// design/ARCHITECTURE.md (the C4 model plus Architecture Contract, maintained
// by the conductor and verified by machinery's G2 gate). Guarding the
// machinery copy would make it unmaintainable, so the design tree is carved
// out of the fixed-basename protection. The carve-out is deliberately NOT
// applied to *.modelith.yaml: under dnf.domain_model the domain model IS the
// architect's D&F artifact and conventionally lives in design/.
func isMachineryDesignPath(root, filePath string) bool {
	cfg, _ := design.Load(root)
	dir := strings.Trim(filepath.ToSlash(cfg.Dir), "/")
	if dir == "" {
		return false
	}
	p := filepath.ToSlash(filePath)
	return strings.HasPrefix(p, dir+"/") ||
		strings.HasPrefix(p, "./"+dir+"/") ||
		strings.Contains(p, "/"+dir+"/")
}

// dfArtifactForPath resolves a path to the D&F artifact it is (its display
// name and owning BLT agent). Fixed artifacts come from dfArtifacts, minus the
// fixture and machinery-design carve-outs. A *.modelith.yaml domain model is
// architect-owned, but only when dnf.domain_model is enabled for the project
// (root is the orchestrator root that owns the settings file).
func dfArtifactForPath(root, path string) (string, string, bool) {
	if path == "" || isFixturePath(path) {
		return "", "", false
	}
	base := filepath.Base(path)
	if agentName, ok := dfArtifacts[base]; ok {
		if isMachineryDesignPath(root, path) {
			return "", "", false
		}
		return base, agentName, true
	}
	if strings.HasSuffix(base, modelithSuffix) && domainModelEnabled(root) {
		return base, "architect", true
	}
	return "", "", false
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
	if !hasNDMutation(command) {
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

// hasNDMutation reports whether any simple command in the shell line is a
// mutating nd / pvg issues invocation. A segment that only asks for help
// (`--help` or `-h` as a standalone WORD, e.g. `pvg nd create --help`)
// mutates nothing and is read-only for the coordinator.
//
// The line is split with the quote-aware parser (parseShell) so a --help
// that appears inside a quoted argument -- a story body, a comment, a commit
// message -- is one token of prose and never exempts the mutation beside it.
// Rejoining the unquoted tokens for the regex is the conservative direction:
// a mutating command hidden inside quotes still matches and still blocks.
func hasNDMutation(command string) bool {
	for _, tokens := range parseShell(command).segments {
		segment := strings.Join(tokens, " ")
		if !ndMutatingCommandRe.MatchString(segment) && !pvgIssuesMutatingRe.MatchString(segment) {
			continue
		}
		if !hasHelpToken(tokens) {
			return true
		}
	}
	return false
}

// hasHelpToken reports whether a parsed segment carries --help or -h as a
// standalone word.
func hasHelpToken(tokens []string) bool {
	for _, tok := range tokens {
		if tok == "--help" || tok == "-h" {
			return true
		}
	}
	return false
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
