// Package lifecycle implements the SessionStart, PreCompact, Stop, and SessionEnd hooks.
package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/vlt"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/vaultcfg"
)

// hookInput matches the JSON Claude Code sends to lifecycle hooks.
// Source is one of "startup", "resume", "clear", or "compact".
type hookInput struct {
	CWD    string `json:"cwd"`
	Source string `json:"source"`
}

// SessionStart loads vault context and project-local knowledge at session start.
// Reads JSON from stdin, outputs structured context to stdout. Always exits 0.
//
// SessionStart stdout IS injected into Claude's context (unlike PreCompact
// stdout, which is user-visible only), so when the hook fires with
// source=="compact" it re-injects the dispatcher rules and loop instructions
// that the compaction summary may have dropped.
func SessionStart() error {
	// 1. Parse hook input
	var input hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		// If parsing fails, use cwd
		input.CWD, _ = os.Getwd()
	}
	if input.CWD == "" {
		input.CWD, _ = os.Getwd()
	}

	// Compaction mid-session: re-inject critical context, keep loop state.
	if input.Source == "compact" {
		return sessionStartAfterCompact(input.CWD)
	}

	// 1b. Clean up stale loop state if persist is disabled. Only fresh
	// session boundaries ("startup", "clear") may clean up -- "compact" and
	// "resume" continue an existing session, where removing the state would
	// kill a running loop.
	if shouldCleanupStaleLoop(input.Source) {
		cleanupStaleLoop(input.CWD)
	}

	// 2. Detect project name
	project := detectProject(input.CWD)

	// 2b. Stack detection (opt-in)
	if readStackDetectionSetting(input.CWD) {
		if stacks := detectStack(input.CWD); len(stacks) > 0 {
			fmt.Printf("[STACK] Detected: %s\n", strings.Join(stacks, ", "))
			fmt.Printf("  Suggested vault search: vlt vault=\"Claude\" search query=\"%s\"\n\n", stacks[0])
		}
	}

	// 3. Open vault
	v, err := vaultcfg.OpenVault()
	if err != nil {
		fmt.Printf("[VAULT] Vault not available -- vault consultation skipped. (%v)\n", err)
		return nil // never block session start
	}

	// 4. Search vault for project context
	// Quote project name to handle spaces/special chars in search.
	searchQuery := project
	if strings.ContainsAny(project, " \t\"") {
		searchQuery = `"` + strings.ReplaceAll(project, `"`, `\"`) + `"`
	}
	results, err := v.Search(vlt.SearchOptions{Query: searchQuery})
	searchOutput := formatVaultSearchOutput(results, err)

	fmt.Printf("[VAULT] Project: %s\nRelevant vault notes:\n\n%s\n\n", project, searchOutput)

	// 4b. Check for project-local knowledge
	projectVaultDir := filepath.Join(input.CWD, ".vault", "knowledge")
	if info, serr := os.Stat(projectVaultDir); serr == nil && info.IsDir() {
		outputProjectKnowledge(projectVaultDir, input.CWD)
	}

	// 5. Read operating mode
	result, err := v.Read("Session Operating Mode", "")
	fmt.Print(formatOperatingModeOutput(result.Content, err))

	return nil
}

func formatVaultSearchOutput(results []vlt.SearchResult, err error) string {
	if err != nil {
		return fmt.Sprintf("(vault search unavailable -- degraded mode: %v)", err)
	}
	if len(results) == 0 {
		return "(none found -- this is a new project to the vault)"
	}

	var lines []string
	for _, r := range results {
		lines = append(lines, fmt.Sprintf("%s (%s)", r.Title, r.RelPath))
	}
	return strings.Join(lines, "\n")
}

func formatOperatingModeOutput(content string, err error) string {
	if err != nil {
		return fmt.Sprintf("[VAULT] Operating mode unavailable from vault -- using built-in fallback. (%v)\n\n%s", err, staticOperatingMode())
	}
	if strings.TrimSpace(content) == "" {
		return staticOperatingMode()
	}
	return fmt.Sprintf("[VAULT] Operating mode for this session (from vault):\n\n%s\n", content)
}

func detectProject(cwd string) string {
	// Try git remote first
	cmd := exec.Command("git", "-C", cwd, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err == nil {
		url := strings.TrimSpace(string(out))
		if url != "" {
			base := filepath.Base(url)
			return strings.TrimSuffix(base, ".git")
		}
	}
	return filepath.Base(cwd)
}

// outputProjectKnowledge prints summaries of project-local knowledge notes.
func outputProjectKnowledge(projectVaultDir, cwd string) {
	maxNotes := readMaxNotesSetting(cwd)
	subfolders := []string{"conventions", "decisions", "patterns", "debug", "skills", "uat"}

	fmt.Println("Project-local knowledge (.vault/knowledge/):")
	fmt.Println()

	found := false
	for _, sub := range subfolders {
		dir := filepath.Join(projectVaultDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		var mdFiles []os.DirEntry
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				mdFiles = append(mdFiles, e)
			}
		}
		if len(mdFiles) == 0 {
			continue
		}
		found = true

		count := 0
		for _, e := range mdFiles {
			if count >= maxNotes {
				break
			}
			filePath := filepath.Join(dir, e.Name())
			date, firstLine := extractNoteSummary(filePath)
			title := strings.TrimSuffix(e.Name(), ".md")
			fmt.Printf("  %s/%s [%s] %s\n", sub, title, date, firstLine)
			count++
		}
		if len(mdFiles) > maxNotes {
			fmt.Printf("  ... and %d more in %s/\n", len(mdFiles)-maxNotes, sub)
		}
	}

	if found {
		fmt.Println()
		fmt.Println("To read a project note in full, use: Read .vault/knowledge/<subfolder>/<note>.md")
		fmt.Println("For deeper assessment, spawn an Explore agent to review project knowledge.")
		fmt.Println()
	}
}

func readMaxNotesSetting(cwd string) int {
	settingsFile := filepath.Join(cwd, ".vault", "knowledge", ".settings.yaml")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return 10
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "session_start_max_notes:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "session_start_max_notes:"))
			n := 10
			_, _ = fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				return n
			}
		}
	}
	return 10
}

func extractNoteSummary(filePath string) (date, firstLine string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "unknown", "(no summary)"
	}

	lines := strings.Split(string(data), "\n")
	date = "unknown"
	firstLine = "(no summary)"
	inFrontmatter := false
	frontmatterEnd := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			frontmatterEnd = true
			continue
		}

		if inFrontmatter && !frontmatterEnd {
			if strings.HasPrefix(trimmed, "created:") {
				date = strings.TrimSpace(strings.TrimPrefix(trimmed, "created:"))
			}
			continue
		}

		if frontmatterEnd && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			firstLine = trimmed
			break
		}
	}

	return date, firstLine
}

// detectStack checks for common project marker files and returns detected stacks.
func detectStack(cwd string) []string {
	type marker struct {
		file  string
		stack string
	}
	markers := []marker{
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"Gemfile", "ruby"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"mix.exs", "elixir"},
	}

	seen := make(map[string]bool)
	var stacks []string

	for _, m := range markers {
		path := filepath.Join(cwd, m.file)
		if _, err := os.Stat(path); err == nil {
			if !seen[m.stack] {
				seen[m.stack] = true
				stacks = append(stacks, m.stack)
			}
			// Check for typescript in package.json
			if m.file == "package.json" {
				data, err := os.ReadFile(path)
				if err == nil && strings.Contains(string(data), "typescript") {
					if !seen["typescript"] {
						seen["typescript"] = true
						stacks = append(stacks, "typescript")
					}
				}
			}
		}
	}

	// Check for C# projects (glob patterns, no single canonical filename)
	csMatches, _ := filepath.Glob(filepath.Join(cwd, "*.csproj"))
	if len(csMatches) == 0 {
		csMatches, _ = filepath.Glob(filepath.Join(cwd, "*.sln"))
	}
	if len(csMatches) > 0 && !seen["csharp"] {
		seen["csharp"] = true
		stacks = append(stacks, "csharp")
	}

	return stacks
}

// readStackDetectionSetting checks .vault/knowledge/.settings.yaml for stack_detection: true.
func readStackDetectionSetting(cwd string) bool {
	settingsFile := filepath.Join(cwd, ".vault", "knowledge", ".settings.yaml")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "stack_detection:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "stack_detection:"))
			return val == "true"
		}
	}
	return false
}

// shouldCleanupStaleLoop reports whether a SessionStart source represents a
// fresh session boundary where stale loop state may be removed. Compaction
// and resume fire mid-session: an active loop must survive them.
func shouldCleanupStaleLoop(source string) bool {
	return source != "compact" && source != "resume"
}

// sessionStartAfterCompact handles SessionStart with source=="compact".
// It re-injects the dispatcher rules and loop recovery instructions into the
// model's context, plus the operating-mode fallback. The expensive vault
// project search is skipped to keep post-compaction re-entry fast. Loop state
// is never cleaned up here -- compaction fires mid-session.
func sessionStartAfterCompact(cwd string) error {
	if state, _, err := dispatcher.ReadStateRoot(cwd); err == nil && state.Enabled {
		fmt.Print(staticDispatcherReminder())
		fmt.Print(staticLoopRecoveryReminder())
	}

	if loop.IsActiveFrom(cwd) {
		if state, _, err := loop.ReadStateRoot(cwd); err == nil {
			epic := state.TargetEpic
			if epic == "" {
				epic = "(all)"
			}
			fmt.Printf("[LOOP] Active loop: epic %s, iteration %d. Run `pvg loop recover` FIRST, then `pvg loop next --json`.\n", epic, state.Iteration)
		}
	}

	fmt.Print(staticOperatingMode())
	return nil
}

// cleanupStaleLoop removes loop state left over from a previous session
// when loop.persist_across_sessions is false (default is true).
func cleanupStaleLoop(cwd string) {
	state, root, err := loop.ReadStateRoot(cwd)
	if err != nil || !state.Active {
		return
	}
	if isLoopPersistEnabled(root) {
		return
	}
	_ = loop.RemoveState(root)
}

func staticOperatingMode() string {
	return `[VAULT] Operating mode for this session:

CONCURRENCY LIMITS (HARD RULE -- unless user explicitly overrides):
  Limits are stack-dependent. Detect from project files (Cargo.toml, *.xcodeproj,
  *.csproj, wrangler.toml/wrangler.jsonc, pyproject.toml, package.json, etc.).

  Heavy stacks (Rust, iOS/Swift, C#, CloudFlare Workers):
    - Maximum 2 developer agents simultaneously
    - Maximum 1 PM-Acceptor agent simultaneously
    - Total active subagents (all types) must not exceed 3
    Reason: compile + test cycles are CPU/memory intensive.

  Light stacks (Python, non-CF TypeScript/JavaScript):
    - Maximum 4 developer agents simultaneously
    - Maximum 2 PM-Acceptor agents simultaneously
    - Total active subagents (all types) must not exceed 6

  When a project mixes stacks, use the most restrictive limit that applies.
  These limits prevent context and machine resource exhaustion.

BEFORE STARTING: Read the vault notes listed above. Do not rediscover what is already known.
  vlt vault="Claude" read file="<note>"

WHILE WORKING: Capture knowledge as it emerges -- do not wait for the end.
  - After making a decision (chose X over Y): create a decision note
  - After solving a non-obvious bug: create a debug note
  - After discovering a reusable pattern: create a pattern note
  Use: vlt vault="Claude" create name="<Title>" path="_inbox/<Title>.md" content="..." silent

BEFORE ENDING: Update the project index note with what was accomplished.
  vlt vault="Claude" append file="<Project>" content="## Session update (<date>)\n- <what was done>"

This is not optional. Knowledge that is not captured is knowledge that will be rediscovered at cost.

DISPATCHER MODE: When the user invokes Paivot (phrases like "use Paivot", "Paivot this",
"run Paivot", "engage Paivot", "with Paivot"), you MUST operate as dispatcher-only.
Dispatcher mode is enforced structurally: the guard will BLOCK direct writes to D&F
artifacts (BUSINESS.md, DESIGN.md, ARCHITECTURE.md) unless a BLT agent is active.
You are a coordinator, NOT a producer. D&F files are structurally blocked; source
code, tests, and stories must likewise be produced by the appropriate agent rather
than by you. Spawn the appropriate agent instead.

D&F ORCHESTRATION: BLT agents produce the three documents sequentially.
  Full D&F: BLT agents (BA, Designer, Architect) CANNOT ask the user questions directly.
  - Spawn them sequentially: BA -> Designer -> Architect
  - Check output for QUESTIONS_FOR_USER blocks
  - Relay questions to the user via AskUserQuestion
  - Resume/re-spawn agent with answers; repeat until document produced
  - Pass prior documents as input to each subsequent agent
  Light D&F (brownfield, or user requests "light"/"quick"):
  Same BLT sequence, but agents draft with fewer questioning rounds. The agents
  STILL produce the files. You do NOT write them yourself.
`
}
