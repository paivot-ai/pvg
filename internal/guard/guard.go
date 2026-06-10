// Package guard implements the PreToolUse scope guard for knowledge governance.
//
// It reads Claude Code's PreToolUse hook JSON from stdin, determines if the
// operation targets a protected vault directory, and exits 2 to block or 0
// to allow. Two layers of protection:
//
// Layer 1 -- System vault (global Obsidian vault "Claude"):
//
//	Protected: methodology/, conventions/, decisions/, patterns/,
//	           debug/, concepts/, projects/, people/
//	Allowed:   _inbox/ (proposals and captures), _templates/
//	Mechanism: checkFilePath blocks Edit/Write, checkBashCommand blocks
//	           shell redirects/cp/mv. vlt commands are always allowed.
//
// Layer 2 -- Project vault (.vault/knowledge/ in project root):
//
//	Protected: all files under .vault/knowledge/
//	Exception: .settings.yaml (managed by pvg settings binary)
//	Mechanism: checkProjectVault blocks Edit/Write, checkBashProjectVault
//	           blocks shell writes. vlt commands are always allowed.
//
// Why vlt is the required mechanism: vlt uses advisory file locking
// (.vlt.lock) to serialize concurrent agent writes. Direct file I/O
// bypasses this lock, risking data loss when multiple agents run
// simultaneously.
package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/ndvault"
)

// HookInput matches the JSON structure Claude Code sends to PreToolUse hooks.
type HookInput struct {
	ToolName  string    `json:"tool_name"`
	ToolInput ToolInput `json:"tool_input"`
}

// ToolInput contains the parameters of the tool being called.
type ToolInput struct {
	FilePath string `json:"file_path"`
	Command  string `json:"command"`
}

// ProtectedFolders are vault subdirectories that require proposal workflow.
var ProtectedFolders = []string{
	"methodology",
	"conventions",
	"decisions",
	"patterns",
	"debug",
	"concepts",
	"projects",
	"people",
}

// Result represents the guard's decision.
type Result struct {
	Allowed bool
	Reason  string
}

// Check reads hook input and returns whether the operation should be allowed.
// projectRoot is the CWD of the invoking process (used to resolve .vault/knowledge/ and .vault/issues/).
func Check(vaultDir, projectRoot string, input HookInput) Result {
	switch input.ToolName {
	case "Edit", "Write":
		if r := checkFilePath(vaultDir, input.ToolInput.FilePath); !r.Allowed {
			return r
		}
		if r := checkProjectVault(projectRoot, input.ToolInput.FilePath); !r.Allowed {
			return r
		}
		if r := checkProjectIssues(projectRoot, input.ToolInput.FilePath); !r.Allowed {
			return r
		}
		return CheckDispatcher(projectRoot, input)
	case "Bash":
		if r := CheckCWDDrift(projectRoot); !r.Allowed {
			return r
		}
		if r := CheckWorktreeCd(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := CheckWorktreeAgentCheckout(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := CheckStoryCheckoutAtRoot(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := checkBashCommand(vaultDir, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := checkBashProjectVault(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := checkBashProjectIssues(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := CheckFSM(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := CheckLabelContract(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		if r := CheckMergeGate(projectRoot, input.ToolInput.Command); !r.Allowed {
			return r
		}
		return CheckDispatcher(projectRoot, input)
	default:
		return Result{Allowed: true}
	}
}

// ParseInput reads and parses the hook JSON from stdin.
func ParseInput() (HookInput, error) {
	var input HookInput
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("failed to parse hook input: %w", err)
	}
	return input, nil
}

// normalizePath resolves symlinks and cleans a file path for reliable prefix
// comparison. Falls back to filepath.Clean if symlink resolution fails (e.g.
// the path does not exist yet).
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		// Path may not exist yet (Write to a new file). Clean it at least.
		return filepath.Clean(p)
	}
	return resolved
}

func systemBlockMsg(folder string) string {
	return fmt.Sprintf(
		"BLOCKED: Direct modification of system-scoped vault content in %s/.\n\n"+
			"System vault directories are protected by knowledge governance.\n"+
			"To change system notes:\n"+
			"  1. Run /vault-evolve to create a proposal\n"+
			"  2. Run /vault-triage to review and apply it\n\n"+
			"Only _inbox/ is writable directly (for proposals and new captures).",
		folder)
}

func checkFilePath(vaultDir, filePath string) Result {
	if filePath == "" || vaultDir == "" {
		return Result{Allowed: true}
	}

	// Normalize both paths so symlinks and case tricks don't bypass the guard.
	normVault := normalizePath(vaultDir)
	normFile := normalizePath(filePath)

	for _, folder := range ProtectedFolders {
		protected := filepath.Join(normVault, folder) + "/"
		if strings.HasPrefix(normFile, protected) {
			return Result{Allowed: false, Reason: systemBlockMsg(folder)}
		}
	}

	// Also check the raw (non-resolved) path in case the file doesn't exist yet
	// and EvalSymlinks fell back to Clean -- the vault dir itself may resolve.
	if normFile != filePath {
		cleanFile := filepath.Clean(filePath)
		for _, folder := range ProtectedFolders {
			protected := filepath.Join(normVault, folder) + "/"
			if strings.HasPrefix(cleanFile, protected) {
				return Result{Allowed: false, Reason: systemBlockMsg(folder)}
			}
		}
	}

	return Result{Allowed: true}
}

func checkBashCommand(vaultDir, command string) Result {
	if command == "" || vaultDir == "" {
		return Result{Allowed: true}
	}

	// vlt commands are the intended mechanism -- always allow
	trimmed := strings.TrimSpace(command)
	if isBareToolInvocation(trimmed, "vlt") {
		return Result{Allowed: true}
	}

	normVault := normalizePath(vaultDir)

	// Check for write operations targeting protected dirs.
	// Key improvement: verify the protected path appears in the write
	// *destination* (after the redirect operator or as a later argument),
	// not just anywhere in the command string.
	for _, folder := range ProtectedFolders {
		protected := filepath.Join(normVault, folder)

		if !strings.Contains(command, protected) {
			continue
		}

		// Check redirect operators: the protected path must appear
		// AFTER the operator to be a write destination.
		for _, op := range []string{">>", ">"} {
			if idx := strings.Index(command, op); idx >= 0 {
				afterOp := command[idx:]
				if strings.Contains(afterOp, protected) {
					return Result{Allowed: false, Reason: systemBlockMsg(folder)}
				}
			}
		}

		// Check file operation commands where protected path is the
		// destination (typically the last path argument).
		destPatterns := []string{
			"tee ", "cp ", "mv ", "cat >", "rm ",
			"sed -i", "perl -pi", "install ", "rsync ", "dd ", "patch ",
		}
		for _, pattern := range destPatterns {
			if strings.Contains(command, pattern) && strings.Contains(command, protected) {
				return Result{Allowed: false, Reason: systemBlockMsg(folder)}
			}
		}

		// Detect interpreter-based writes: python3 -c 'open(...)', ruby -e,
		// node -e, perl -e, etc.
		if containsInterpreterWrite(command, protected) {
			return Result{Allowed: false, Reason: systemBlockMsg(folder)}
		}
	}

	return Result{Allowed: true}
}

// interpreterPrefixes lists interpreter command prefixes that can write files.
var interpreterPrefixes = []string{
	"python3 -c", "python -c", "python2 -c",
	"ruby -e", "node -e", "perl -e", "lua -e",
}

// containsInterpreterWrite returns true if the command uses a scripting
// interpreter and references a protected path (likely a file write).
func containsInterpreterWrite(command, protectedPath string) bool {
	for _, prefix := range interpreterPrefixes {
		if strings.Contains(command, prefix) && strings.Contains(command, protectedPath) {
			return true
		}
	}
	return false
}

const projectVaultBlockMsg = "BLOCKED: Direct modification of project vault. " +
	"Use vlt vault=\"<path>\" commands instead. " +
	"vlt provides locking for concurrent agent safety."

const projectIssuesBlockMsg = "BLOCKED: Direct modification of issue tracker. " +
	"Use nd commands instead (nd create, nd update, nd close). " +
	"nd provides locking and FSM validation for concurrent agent safety. " +
	"For body edits use: nd update <id> -d '<new description>' or nd update <id> --body-file -. " +
	"Load the nd skill first (/nd) if you are unfamiliar with nd commands."

// projectVaultPath is the relative path segment that identifies project vault files.
const projectVaultPath = "/.vault/knowledge/"
const projectVaultRelPath = ".vault/knowledge/"

// projectIssuesPath is the relative path segment that identifies project issue files.
const projectIssuesPath = "/.vault/issues/"
const projectIssuesRelPath = ".vault/issues/"
const sharedProjectIssuesRelPath = "paivot/nd-vault/issues/"

func checkProjectVault(projectRoot, filePath string) Result {
	if filePath == "" || projectRoot == "" {
		return Result{Allowed: true}
	}

	// Resolve relative paths against project root before comparison.
	resolvedFile := filePath
	if !filepath.IsAbs(filePath) {
		resolvedFile = filepath.Join(filepath.Clean(projectRoot), filePath)
	}
	normFile := normalizePath(resolvedFile)
	cleanFile := filepath.Clean(resolvedFile)

	if !hasAnyPrefix(normFile, projectPathPrefixes(projectRoot, ".vault", "knowledge")) &&
		!hasAnyPrefix(cleanFile, projectPathPrefixes(projectRoot, ".vault", "knowledge")) {
		return Result{Allowed: true}
	}

	// Allow .settings.yaml -- managed by pvg settings (our own binary)
	if filepath.Base(filePath) == ".settings.yaml" {
		return Result{Allowed: true}
	}

	return Result{Allowed: false, Reason: projectVaultBlockMsg}
}

func checkBashProjectVault(projectRoot, command string) Result {
	if command == "" || projectRoot == "" {
		return Result{Allowed: true}
	}

	trimmed := strings.TrimSpace(command)
	if isBareToolInvocation(trimmed, "vlt") {
		return Result{Allowed: true}
	}

	vaultSegments := projectPathPrefixes(projectRoot, ".vault", "knowledge")
	if !stringsContainAny(command, vaultSegments) && !strings.Contains(command, projectVaultRelPath) {
		return Result{Allowed: true}
	}

	// Check redirect operators: protected path must be after the operator.
	for _, op := range []string{">>", ">"} {
		if idx := strings.Index(command, op); idx >= 0 {
			afterOp := command[idx:]
			if stringsContainAny(afterOp, vaultSegments) || strings.Contains(afterOp, projectVaultRelPath) {
				return Result{Allowed: false, Reason: projectVaultBlockMsg}
			}
		}
	}

	// Check write commands with protected path.
	writePatterns := []string{
		"tee ", "cp ", "mv ", "cat >", "mkdir ", "rm ",
		"sed -i", "perl -pi", "install ", "rsync ", "dd ", "patch ",
	}
	for _, pattern := range writePatterns {
		if strings.Contains(command, pattern) &&
			(stringsContainAny(command, vaultSegments) || strings.Contains(command, projectVaultRelPath)) {
			return Result{Allowed: false, Reason: projectVaultBlockMsg}
		}
	}

	// Detect interpreter-based writes targeting project vault.
	for _, segment := range vaultSegments {
		if containsInterpreterWrite(command, segment) {
			return Result{Allowed: false, Reason: projectVaultBlockMsg}
		}
	}
	if containsInterpreterWrite(command, projectVaultRelPath) {
		return Result{Allowed: false, Reason: projectVaultBlockMsg}
	}

	return Result{Allowed: true}
}

func checkProjectIssues(projectRoot, filePath string) Result {
	if filePath == "" || projectRoot == "" {
		return Result{Allowed: true}
	}

	// Resolve relative paths against project root before comparison.
	resolvedFile := filePath
	if !filepath.IsAbs(filePath) {
		resolvedFile = filepath.Join(filepath.Clean(projectRoot), filePath)
	}
	normFile := normalizePath(resolvedFile)
	cleanFile := filepath.Clean(resolvedFile)

	issuePrefixes := projectIssuePrefixes(projectRoot)
	if !hasAnyPrefix(normFile, issuePrefixes) &&
		!hasAnyPrefix(cleanFile, issuePrefixes) {
		return Result{Allowed: true}
	}

	return Result{Allowed: false, Reason: projectIssuesBlockMsg}
}

func checkBashProjectIssues(projectRoot, command string) Result {
	if command == "" || projectRoot == "" {
		return Result{Allowed: true}
	}

	trimmed := strings.TrimSpace(command)
	// nd commands are the intended mechanism -- always allow
	if isBareToolInvocation(trimmed, "nd") {
		return Result{Allowed: true}
	}

	issueSegments := projectIssueCommandSegments(projectRoot)
	if !stringsContainAny(command, issueSegments) && !strings.Contains(command, projectIssuesRelPath) {
		return Result{Allowed: true}
	}

	// Check redirect operators: protected path must be after the operator.
	for _, op := range []string{">>", ">"} {
		if idx := strings.Index(command, op); idx >= 0 {
			afterOp := command[idx:]
			if stringsContainAny(afterOp, issueSegments) || strings.Contains(afterOp, projectIssuesRelPath) {
				return Result{Allowed: false, Reason: projectIssuesBlockMsg}
			}
		}
	}

	// Check write commands with protected path.
	writePatterns := []string{
		"tee ", "cp ", "mv ", "cat >", "mkdir ", "rm ",
		"sed -i", "perl -pi", "install ", "rsync ", "dd ", "patch ",
	}
	for _, pattern := range writePatterns {
		if strings.Contains(command, pattern) &&
			(stringsContainAny(command, issueSegments) || strings.Contains(command, projectIssuesRelPath)) {
			return Result{Allowed: false, Reason: projectIssuesBlockMsg}
		}
	}

	// Detect interpreter-based writes targeting project issues.
	for _, segment := range issueSegments {
		if containsInterpreterWrite(command, segment) {
			return Result{Allowed: false, Reason: projectIssuesBlockMsg}
		}
	}
	if containsInterpreterWrite(command, projectIssuesRelPath) {
		return Result{Allowed: false, Reason: projectIssuesBlockMsg}
	}

	return Result{Allowed: true}
}

func projectIssuePrefixes(projectRoot string) []string {
	prefixes := projectPathPrefixes(projectRoot, ".vault", "issues")

	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return prefixes
	}

	sharedIssuesDir := filepath.Join(vaultDir, "issues")
	prefixes = appendUniquePrefix(prefixes, filepath.Clean(sharedIssuesDir)+string(os.PathSeparator))
	prefixes = appendUniquePrefix(prefixes, normalizePath(sharedIssuesDir)+string(os.PathSeparator))
	return prefixes
}

func projectIssueCommandSegments(projectRoot string) []string {
	segments := projectIssuePrefixes(projectRoot)
	segments = appendUniquePrefix(segments, sharedProjectIssuesRelPath)

	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return segments
	}

	sharedIssuesDir := filepath.Join(vaultDir, "issues")
	if rel, relErr := filepath.Rel(filepath.Clean(projectRoot), sharedIssuesDir); relErr == nil && rel != "" && rel != "." {
		segments = appendUniquePrefix(segments, filepath.Clean(rel)+string(os.PathSeparator))
	}
	return segments
}

func projectPathPrefixes(projectRoot string, relParts ...string) []string {
	rawPrefix := filepath.Join(filepath.Clean(projectRoot), filepath.Join(relParts...)) + string(os.PathSeparator)
	normPrefix := filepath.Join(normalizePath(projectRoot), filepath.Join(relParts...)) + string(os.PathSeparator)

	if normPrefix == rawPrefix || normPrefix == string(os.PathSeparator) {
		return []string{rawPrefix}
	}
	return []string{rawPrefix, normPrefix}
}

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func stringsContainAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func appendUniquePrefix(prefixes []string, prefix string) []string {
	if prefix == "" {
		return prefixes
	}
	for _, existing := range prefixes {
		if existing == prefix {
			return prefixes
		}
	}
	return append(prefixes, prefix)
}

func isBareToolInvocation(command, tool string) bool {
	if !(command == tool || strings.HasPrefix(command, tool+" ") || strings.HasPrefix(command, tool+"\t")) {
		return false
	}
	return !hasShellComposition(command)
}

func hasShellComposition(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			escaped = false
			continue
		}

		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}

		switch ch {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = !inDouble
		case '`':
			return true
		case '$':
			if i+1 < len(command) && command[i+1] == '(' {
				return true
			}
		case ';', '|', '>', '<', '\n':
			if !inDouble {
				return true
			}
		case '&':
			if !inDouble {
				return true
			}
		}
	}

	return false
}
