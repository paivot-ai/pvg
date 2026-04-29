// Package governance implements vault seeding and knowledge governance operations.
package governance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paivot-ai/vlt"

	"github.com/paivot-ai/pvg/internal/vaultcfg"
)

// Counters tracks seed operations.
type Counters struct {
	Created int
	Updated int
	Skipped int
}

// Seed writes vault notes to disk under an exclusive vlt lock. This prevents
// data corruption if an active Claude Code session has agents writing through
// vlt concurrently. Obsidian picks up the files via iCloud sync.
func Seed(force bool, pluginDir string) error {
	today := time.Now().Format("2006-01-02")

	// Open vault
	v, err := vaultcfg.OpenVault()
	if err != nil {
		return fmt.Errorf("cannot open vault: %w", err)
	}
	vaultDir := v.Dir()

	// Acquire exclusive vault lock so we don't race with active sessions.
	unlock, lockErr := vlt.LockVault(vaultDir, true)
	if lockErr != nil {
		fmt.Printf("WARNING: Could not acquire vault lock (%v). Proceeding without lock.\n", lockErr)
	} else {
		defer unlock()
	}

	// Resolve plugin dir if not provided
	if pluginDir == "" {
		exe, eerr := os.Executable()
		if eerr == nil {
			// Assume pvg is at bin/pvg or pvg-cli/pvg, walk up to plugin root
			pluginDir = filepath.Dir(filepath.Dir(exe))
		}
	}
	// If pluginDir still empty, try CLAUDE_PLUGIN_ROOT
	if pluginDir == "" {
		pluginDir = os.Getenv("CLAUDE_PLUGIN_ROOT")
	}

	// Resolve agent source
	agentSrc, err := resolveAgentSrc(pluginDir)
	if err != nil {
		return err
	}

	counters := &Counters{}

	fmt.Println("paivot-graph vault seeder")
	fmt.Println("=========================")
	if force {
		fmt.Println("Mode: force (overwriting existing notes)")
	} else {
		fmt.Println("Mode: safe (skipping existing notes)")
	}
	fmt.Println()

	// 1. Seed agent prompts
	fmt.Println("Seeding agent prompts...")
	agents := []struct {
		slug      string
		vaultName string
	}{
		{"sr-pm", "Sr PM Agent"},
		{"pm", "PM Acceptor Agent"},
		{"developer", "Developer Agent"},
		{"architect", "Architect Agent"},
		{"designer", "Designer Agent"},
		{"business-analyst", "Business Analyst Agent"},
		{"anchor", "Anchor Agent"},
		{"retro", "Retro Agent"},
		{"ba-challenger", "BA Challenger Agent"},
		{"designer-challenger", "Designer Challenger Agent"},
		{"architect-challenger", "Architect Challenger Agent"},
	}

	for _, agent := range agents {
		seedAgent(vaultDir, agentSrc, agent.slug, agent.vaultName, today, force, counters)
	}

	// 2. Seed skill content
	fmt.Println()
	fmt.Println("Seeding skill content...")
	seedSkill(vaultDir, agentSrc, today, force, counters)

	// 3. Seed behavioral notes
	fmt.Println()
	fmt.Println("Seeding behavioral notes...")
	seedSessionOperatingMode(vaultDir, today, force, counters)
	seedPreCompactChecklist(vaultDir, today, force, counters)
	seedStopCaptureChecklist(vaultDir, today, force, counters)

	fmt.Println()
	fmt.Printf("Done. Created: %d, Updated: %d, Skipped: %d\n",
		counters.Created, counters.Updated, counters.Skipped)

	return nil
}

func resolveAgentSrc(pluginDir string) (string, error) {
	src := os.Getenv("AGENT_SRC")
	if src != "" {
		return src, nil
	}

	if pluginDir != "" {
		localAgents := filepath.Join(pluginDir, "agents")
		if info, err := os.Stat(localAgents); err == nil && info.IsDir() {
			return localAgents, nil
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	// Walk the paivot-graph cache to find agents/ directories.
	cacheBase := filepath.Join(home, ".claude", "plugins", "cache", "paivot-graph")
	var candidates []string

	_ = filepath.WalkDir(cacheBase, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() && d.Name() == "agents" {
			candidates = append(candidates, path)
		}
		return nil
	})

	if len(candidates) == 0 {
		return "", fmt.Errorf("could not find paivot-graph agents directory in plugin cache.\nSet AGENT_SRC=/path/to/agents manually, or install paivot-graph first")
	}

	// Pick the newest candidate by directory modification time.
	// WalkDir returns lexicographic order which breaks for semver
	// (e.g. 1.10.0 < 1.9.0 lexicographically). Mtime is reliable
	// because the newest plugin version was installed most recently.
	best := candidates[0]
	bestTime := modTime(best)
	for _, c := range candidates[1:] {
		t := modTime(c)
		if t.After(bestTime) {
			best = c
			bestTime = t
		}
	}

	return best, nil
}

// modTime returns the modification time of a path, or zero time on error.
func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func writeNote(vaultDir, relPath, content string, force bool, counters *Counters) {
	fullPath := filepath.Join(vaultDir, relPath)

	if _, err := os.Stat(fullPath); err == nil {
		if force {
			if werr := os.WriteFile(fullPath, []byte(content), 0644); werr != nil {
				fmt.Printf("  ERROR: %s: %v\n", relPath, werr)
				return
			}
			fmt.Printf("  UPDATED: %s\n", relPath)
			counters.Updated++
		} else {
			fmt.Printf("  SKIP: %s (already exists)\n", relPath)
			counters.Skipped++
		}
		return
	}

	// Ensure parent directory
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		fmt.Printf("  ERROR: cannot create directory for %s: %v\n", relPath, err)
		return
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		fmt.Printf("  ERROR: %s: %v\n", relPath, err)
		return
	}
	fmt.Printf("  CREATED: %s\n", relPath)
	counters.Created++
}

// findAgentSource resolves the best source file for a given agent.
// Priority: seed/ directory (rich prompts) > agents/ directory (fallback loaders).
// The seed/ directory uses vault-style names (e.g. "Sr PM Playbook.md"),
// while agents/ uses slug names (e.g. "sr-pm.md").
func findAgentSource(agentSrc, slug, vaultName string) string {
	// agentSrc points to <plugin>/agents/. Derive seed dir as sibling.
	seedDir := filepath.Join(filepath.Dir(agentSrc), "seed")

	// Check seed/ directory for files matching the vault name pattern.
	// Convention: seed files may use the vault name or a descriptive title.
	if entries, err := os.ReadDir(seedDir); err == nil {
		vaultLower := strings.ToLower(vaultName)
		// Strip " Agent" suffix for matching (e.g. "Sr PM Agent" -> "Sr PM")
		baseName := strings.TrimSuffix(vaultLower, " agent")
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			nameLower := strings.ToLower(strings.TrimSuffix(e.Name(), ".md"))
			// Match if filename starts with the base name (e.g. "sr pm playbook" starts with "sr pm")
			if strings.HasPrefix(nameLower, baseName) {
				return filepath.Join(seedDir, e.Name())
			}
		}
	}

	// Fall back to agents/<slug>.md
	agentFile := filepath.Join(agentSrc, slug+".md")
	if _, err := os.Stat(agentFile); err == nil {
		return agentFile
	}

	return ""
}

func extractBody(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	fmCount := 0
	bodyStart := 0

	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			fmCount++
			if fmCount >= 2 {
				bodyStart = i + 1
				break
			}
		}
	}

	if bodyStart == 0 {
		return string(data), nil
	}

	return strings.Join(lines[bodyStart:], "\n"), nil
}

func seedAgent(vaultDir, agentSrc, slug, vaultName, today string, force bool, counters *Counters) {
	// Check for a richer seed source first. Files in seed/ contain the full
	// authoritative prompt (e.g. "seed/Sr PM Playbook.md" for Sr PM Agent).
	// Files in agents/ are thin vault-loaders with fallback content.
	srcFile := findAgentSource(agentSrc, slug, vaultName)
	if srcFile == "" {
		fmt.Printf("  WARN: no source found for %s (checked agents/ and seed/), skipping\n", vaultName)
		counters.Skipped++
		return
	}

	body, err := extractBody(srcFile)
	if err != nil {
		fmt.Printf("  WARN: cannot read %s: %v\n", srcFile, err)
		counters.Skipped++
		return
	}

	content := fmt.Sprintf(`---
type: methodology
scope: system
project: paivot
stack: [claude-code]
domain: dev-tools-workflow
status: active
created: %s
---

%s

## Changelog

- %s: Seeded from paivot-graph plugin (initial version)
`, today, strings.TrimSpace(body), today)

	writeNote(vaultDir, filepath.Join("methodology", vaultName+".md"), content, force, counters)
}

func seedSkill(vaultDir, agentSrc, today string, force bool, counters *Counters) {
	// agentSrc points to <plugin>/agents/; skills/ is a sibling directory.
	skillSrc := filepath.Join(filepath.Dir(agentSrc), "skills", "vault-knowledge", "SKILL.md")
	if _, err := os.Stat(skillSrc); err != nil {
		fmt.Printf("  WARN: %s not found\n", skillSrc)
		counters.Skipped++
		return
	}

	body, err := extractBody(skillSrc)
	if err != nil {
		fmt.Printf("  WARN: cannot read %s: %v\n", skillSrc, err)
		counters.Skipped++
		return
	}

	content := fmt.Sprintf(`---
type: convention
scope: system
project: paivot-graph
stack: [claude-code, obsidian]
domain: dev-tools-knowledge
status: active
created: %s
---

%s

## Changelog

- %s: Seeded from paivot-graph plugin (initial version)
`, today, strings.TrimSpace(body), today)

	writeNote(vaultDir, filepath.Join("conventions", "Vault Knowledge Skill.md"), content, force, counters)
}

func seedSessionOperatingMode(vaultDir, today string, force bool, counters *Counters) {
	content := fmt.Sprintf(`---
type: convention
scope: system
project: paivot-graph
stack: [claude-code, obsidian]
domain: dev-tools-workflow
status: active
created: %s
---

# Session Operating Mode

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

## DISPATCHER MODE

When the user invokes Paivot (phrases like "use Paivot", "Paivot this", "run Paivot",
"engage Paivot", "with Paivot"), you MUST operate as dispatcher-only for the remainder
of the session.

Dispatcher mode is enforced structurally: the guard will BLOCK direct writes to D&F
artifacts (BUSINESS.md, DESIGN.md, ARCHITECTURE.md) unless a BLT agent is active.

In dispatcher mode you are a coordinator, NOT a producer. You:
  - Spawn BLT agents (BA, Designer, Architect) and relay their questions
  - Spawn execution agents (Sr PM, Developer, PM-Acceptor, Anchor, Retro)
  - Relay QUESTIONS_FOR_USER blocks from subagents to the user via AskUserQuestion
  - Summarize agent outputs for the user
  - Use pvg loop next --json to determine what should happen next
  - Use pvg story deliver|accept|reject for structural story transitions
  - Use pvg nd ... for all live backlog reads and writes
  - Capture knowledge to the vault

You NEVER:
  - Write BUSINESS.md, DESIGN.md, or ARCHITECTURE.md yourself
  - Write source code, tests, or story files yourself
  - Make architectural or design decisions yourself
  - Skip agents to "save time"
  - Query nd globally for dispatch decisions (use pvg loop next --json)
  - Spawn generic/Explore agents for tasks you can do with direct tool calls

If you catch yourself about to write a file that an agent should produce, STOP.
Spawn the appropriate agent instead.

DIRECT TOOL CALLS (do NOT spawn agents for these):
  - Reading stories: pvg nd show <id>
  - Checking ready work: pvg nd ready or pvg loop next --json
  - Creating branches: git checkout -b epic/<epic-id> main
  - Listing issues: pvg nd list ...
  - Running gates: pvg lint, pvg rtm check, pvg nd dep cycles
  - Reading files: use the Read tool directly

Only spawn agents for PRODUCTION work:
  - paivot-graph:developer for story implementation
  - paivot-graph:pm for delivery review
  - paivot-graph:sr-pm for backlog CRUD
  - paivot-graph:anchor for backlog/milestone review
  - BLT agents (business-analyst, designer, architect) for D&F

Execution priority is structural, enforced by pvg loop next --json:
  1. The loop drains ONE EPIC AT A TIME (default behavior since pvg v1.44.0)
  2. Within the epic: delivered -> rejected -> ready (priority order)
  3. All parallelization happens INSIDE the current epic, not across epics
  4. Epic completion triggers a gate: e2e tests + Anchor review + merge to main
  5. After the gate passes, spawn paivot-graph:retro to extract learnings from the epic
  6. After retro completes, the loop auto-rotates to the next highest-priority epic
  7. No cherry-picking. No cross-epic work. The epic is a containment boundary.

Do not re-implement that ordering yourself. Run pvg loop next --json each iteration.
It returns a JSON decision (act, epic_complete, epic_blocked, wait, complete)
that tells you exactly what to do next.

## D&F ORCHESTRATION

Full D&F: BLT agents produce the three documents sequentially.
  1. Spawn BA with existing context (vault notes, codebase)
  2. Check output for QUESTIONS_FOR_USER block
     - If present: relay to user via AskUserQuestion, resume agent with answers, repeat
     - If absent: BUSINESS.md is done
  3. SPECIALIST REVIEW (if dnf.specialist_review is enabled):
     a. Spawn ba-challenger with BUSINESS.md + user context + iteration=1
     b. Parse output for REVIEW_RESULT:
        - APPROVED: proceed to step 4
        - REJECTED: re-spawn BA with FEEDBACK_FOR_CREATOR content, then
          re-spawn ba-challenger with iteration+1. Repeat up to dnf.max_iterations
          (default 3). If still REJECTED after max iterations, relay remaining
          ISSUES to user via AskUserQuestion and let user decide: fix or proceed.
  4. Spawn Designer with BUSINESS.md content
  5. Same relay loop until DESIGN.md is produced
  6. SPECIALIST REVIEW (if enabled):
     Same loop as step 3, but with designer-challenger reviewing DESIGN.md
     against BUSINESS.md + user context.
  7. Spawn Architect with BUSINESS.md + DESIGN.md
  8. Same relay loop until ARCHITECTURE.md is produced
  9. SPECIALIST REVIEW (if enabled):
     Same loop as step 3, but with architect-challenger reviewing ARCHITECTURE.md
     against BUSINESS.md + DESIGN.md + user context.

Light D&F (brownfield, or user requests "light"/"quick"):
  Same BLT sequence, but agents draft with fewer questioning rounds using existing
  codebase and vault context. The agents STILL produce the files. You do NOT write
  them yourself. Light means "fewer rounds", not "bypass agents".
  Specialist review still applies if dnf.specialist_review is enabled.

Post-D&F: Three-step backlog creation with structural gates and adversarial review.
  1. Spawn Sr PM to create the backlog from all three D&F documents.
     The Sr PM reads all three documents and creates self-contained stories
     with all context embedded.
  2. Run structural gates (MANDATORY before Anchor submission):
     pvg rtm check    -- Verify all tagged D&F requirements have covering stories
     pvg lint          -- Check for artifact collisions (duplicate PRODUCES)
     Both must pass. If either fails, re-spawn Sr PM with the failure output.
     These are deterministic -- if they fail, the Anchor WILL reject for the
     same reason. Running them first saves a full Anchor round-trip.
  3. Spawn Anchor to adversarially review the backlog.
     The Anchor checks for gaps: missing walking skeletons, horizontal layers,
     missing integration stories, dependency cycles, INVEST violations.
     The Anchor returns APPROVED or REJECTED -- no conditional pass.
     If REJECTED: relay the gaps to the Sr PM, re-spawn to fix, re-submit to Anchor.
     (The Sr PM applies the Feedback Generalization Protocol: for each issue,
     identify the general rule, sweep the entire backlog, fix all violations.)
     Execution MUST NOT begin until the Anchor returns APPROVED.

### Specialist Review Loop (dispatcher procedure)

When dnf.specialist_review is enabled, run this after each BLT document:

  1. Check setting: pvg settings dnf.specialist_review
     If false: skip to next BLT step.

  2. Read max iterations: pvg settings dnf.max_iterations (default 3)
     Set iteration = 1.

  3. Spawn the challenger with:
     - Document content (e.g. BUSINESS.md)
     - Upstream documents (e.g. user context for BA, BUSINESS.md for Designer)
     - "This is iteration N of max_iter"
     - If iteration > 1: include previous FEEDBACK_FOR_CREATOR

  4. Parse challenger output for REVIEW_RESULT:

     APPROVED -> exit loop, proceed to next BLT step.

     REJECTED ->
       If iteration < max_iter:
         Re-spawn creator (BA/Designer/Architect) with:
           "SPECIALIST REVIEW FEEDBACK (iteration N/max_iter):
            [paste FEEDBACK_FOR_CREATOR content from challenger]"
         Wait for creator to revise the document.
         Increment iteration. Go to step 3.
       Else (max iterations exhausted):
         Relay to user via AskUserQuestion:
           "Specialist review: [challenger] rejected [document] after
            [max_iter] iterations. Remaining issues:
            [ISSUES from last rejection]
            Options: (a) provide guidance for another attempt,
            (b) proceed without resolving these issues"
         If user chooses (a): re-spawn creator with user guidance, retry once more.
         If user chooses (b): proceed to next BLT step.

  Challenger-to-creator mapping:
    ba-challenger        -> business-analyst (BUSINESS.md)
    designer-challenger  -> designer (DESIGN.md)
    architect-challenger -> architect (ARCHITECTURE.md)

When D&F is not needed (skip entirely):
  - User explicitly says they do not want D&F
  - The task is a simple bug fix, refactor, or well-defined small feature
  - All three D&F documents already exist and the user is asking for execution

## Related

- [[paivot-graph]] -- Plugin that reads this note at session start
- [[Vault as runtime not reference]] -- Why this content lives in the vault
- [[Vault Knowledge Skill]] -- How to interact with the vault
- [[Pre-Compact Checklist]] -- Companion checklist before compaction
- [[Stop Capture Checklist]] -- Companion checklist before stopping
- [[D&F Sequential With Alignment]] -- Why sequential over parallel
- [[Subagent question relay via orchestrator]] -- The structural pattern
- [[Subagents do not follow advisory instructions]] -- Why advisory does not work

## Changelog

- %s: Seeded from paivot-graph plugin (initial version)
`, today, today)

	writeNote(vaultDir, filepath.Join("conventions", "Session Operating Mode.md"), content, force, counters)
}

func seedPreCompactChecklist(vaultDir, today string, force bool, counters *Counters) {
	content := fmt.Sprintf(`---
type: convention
scope: system
project: paivot-graph
stack: [claude-code, obsidian]
domain: dev-tools-workflow
status: active
created: %s
---

# Pre-Compact Checklist

Context compaction is imminent. Save anything worth remembering NOW.

## 1. DECISIONS made this session

Record any decisions with rationale and alternatives considered:
  vlt vault="Claude" create name="<Decision Title>" path="_inbox/<Decision Title>.md" content="..." silent

Include frontmatter: type: decision, project: <project>, status: active, confidence: high, created: <YYYY-MM-DD>
Include sections: Decision, Rationale, Alternatives considered.

## 2. PATTERNS discovered

Record reusable solutions:
  vlt vault="Claude" create name="<Pattern Name>" path="_inbox/<Pattern Name>.md" content="..." silent

Include frontmatter: type: pattern, project: <project>, stack: [], status: active, created: <YYYY-MM-DD>
Include sections: When to use, Implementation.

## 3. DEBUG INSIGHTS

Record problems solved:
  vlt vault="Claude" create name="<Bug Title>" path="_inbox/<Bug Title>.md" content="..." silent

Include frontmatter: type: debug, project: <project>, status: active, created: <YYYY-MM-DD>
Include sections: Symptoms, Root cause, Fix.

## 4. PROJECT UPDATES

  vlt vault="Claude" append file="<Project>" content="## Session update (<YYYY-MM-DD>)\n- <what was accomplished>"

Do this NOW -- after compaction, the details will be lost.

## Changelog

- %s: Seeded from paivot-graph plugin (initial version)
`, today, today)

	writeNote(vaultDir, filepath.Join("conventions", "Pre-Compact Checklist.md"), content, force, counters)
}

func seedStopCaptureChecklist(vaultDir, today string, force bool, counters *Counters) {
	content := fmt.Sprintf(`---
type: convention
scope: system
project: paivot-graph
stack: [claude-code, obsidian]
domain: dev-tools-workflow
status: active
created: %s
---

# Stop Capture Checklist

Before ending this session, confirm you have considered each of these:

- [ ] Did you capture any DECISIONS made this session? (chose X over Y, established a convention)
- [ ] Did you capture any PATTERNS discovered? (reusable solutions, idioms, workflows)
- [ ] Did you capture any DEBUG INSIGHTS? (non-obvious bugs, sharp edges, environment issues)
- [ ] Did you update the PROJECT INDEX NOTE with what was accomplished?

If none of the above apply (e.g., quick fix, trivial session), that is fine -- but confirm it was considered, not forgotten.

Use vlt to create notes: vlt vault="Claude" create name="<Title>" path="_inbox/<Title>.md" content="..." silent

## Changelog

- %s: Seeded from paivot-graph plugin (initial version)
`, today, today)

	writeNote(vaultDir, filepath.Join("conventions", "Stop Capture Checklist.md"), content, force, counters)
}
