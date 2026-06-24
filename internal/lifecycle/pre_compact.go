package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/vaultcfg"
)

// PreCompact outputs a knowledge capture reminder before context compaction.
// Reads the Pre-Compact Checklist from the vault or uses a static fallback.
func PreCompact() error {
	v, err := vaultcfg.OpenVault()
	if err == nil {
		result, rerr := v.Read("Pre-Compact Checklist", "")
		if rerr == nil && result.Content != "" {
			fmt.Println("[VAULT] Context compaction imminent -- capture knowledge now.")
			fmt.Println()
			fmt.Println(result.Content)
			outputTwoTierGuidance()
			outputDispatcherReminder()
			return nil
		}
	}

	// Try direct file read
	vaultDir, derr := vaultcfg.VaultDir()
	if derr == nil {
		path := filepath.Join(vaultDir, "conventions", "Pre-Compact Checklist.md")
		data, ferr := os.ReadFile(path)
		if ferr == nil && len(data) > 0 {
			fmt.Println("[VAULT] Context compaction imminent -- capture knowledge now.")
			fmt.Println()
			fmt.Println(string(data))
			outputTwoTierGuidance()
			outputDispatcherReminder()
			return nil
		}
	}

	// Static fallback
	fmt.Print(staticPreCompact())
	outputTwoTierGuidance()
	outputDispatcherReminder()
	return nil
}

func outputTwoTierGuidance() {
	cwd, _ := os.Getwd()
	knowledgeDir := filepath.Join(cwd, ".vault", "knowledge")
	if info, err := os.Stat(knowledgeDir); err == nil && info.IsDir() {
		fmt.Print(`
[VAULT] Where to save knowledge:
  - Universal insights (applicable to ANY project) -> global vault _inbox/
      vlt vault="Claude" create name="<Title>" path="_inbox/<Title>.md" content="..." silent
  - Project-specific insights (only relevant HERE) -> .vault/knowledge/ locally
      Use vlt against the local vault, for example:
      vlt vault=".vault" create name="<Title>" path="knowledge/decisions/<Title>.md" content="..." silent
`)
	}
}

// outputDispatcherReminder checks if dispatcher mode is active and, if so,
// emits a prominent rules block before compaction. This is defense-in-depth:
// the UserPromptSubmit hook also re-injects a reminder on every prompt, but
// putting the rules right before compaction maximizes the chance that the
// compaction summary preserves them.
func outputDispatcherReminder() {
	cwd, _ := os.Getwd()
	if cwd == "" {
		return
	}
	state, err := dispatcher.ReadState(cwd)
	if err != nil || !state.Enabled {
		return
	}
	fmt.Print(staticDispatcherReminder())
	fmt.Print(staticLoopRecoveryReminder())
}

// staticLoopRecoveryReminder returns the post-compaction loop recovery instructions.
func staticLoopRecoveryReminder() string {
	return `
[LOOP RECOVERY -- RUN IMMEDIATELY AFTER COMPACTION]
After context compaction, you have lost track of running agents and worktrees.
Before touching git or spawning new agents, run:

  pvg loop recover

This command:
1. Removes ONLY Paivot-owned orphan worktrees (under .claude/worktrees/) and
   their Paivot branches; worktrees created by other tools (e.g. .codex-worktrees/)
   or at external paths are preserved, never touched
2. Resets orphaned in-progress stories to open (delivered stories preserved)
3. Outputs a recovery summary showing what is ready, delivered, and needs attention

DO NOT manually inspect or merge branches after compaction. DO NOT reconstruct
state from the git tree. pvg loop recover is the ONLY safe way to resume.
`
}

// staticDispatcherReminder returns the dispatcher rules block for pre-compact injection.
func staticDispatcherReminder() string {
	return `
[DISPATCHER MODE -- SURVIVES COMPACTION]
You are operating in DISPATCHER MODE. This is NON-NEGOTIABLE after compaction:
- You are a COORDINATOR. You spawn agents. You relay questions. You summarize outputs.
- You NEVER write BUSINESS.md, DESIGN.md, or ARCHITECTURE.md yourself, and you NEVER mutate nd directly from the coordinator.
- Source code and tests must also be produced by the appropriate agent rather than by you.
- You NEVER skip agents to "save time".
- If you catch yourself about to write a file that an agent should produce, STOP and spawn the agent.
This rule persists across compaction boundaries.
`
}

func staticPreCompact() string {
	return `[VAULT] Context compaction imminent -- capture knowledge now.

Before this context is compacted, save anything worth remembering:

1. DECISIONS made this session (with rationale and alternatives considered):
   vlt vault="Claude" create name="<Decision Title>" path="_inbox/<Decision Title>.md" content="..." silent

2. PATTERNS discovered (reusable solutions):
   vlt vault="Claude" create name="<Pattern Name>" path="_inbox/<Pattern Name>.md" content="..." silent

3. DEBUG INSIGHTS (problems solved):
   vlt vault="Claude" create name="<Bug Title>" path="_inbox/<Bug Title>.md" content="..." silent

4. PROJECT UPDATES (progress, state changes):
   vlt vault="Claude" append file="<Project>" content="## Session update (<date>)\n- <what was accomplished>"

All notes must have frontmatter: type, project, status, created.

Do this NOW -- after compaction, the details will be lost.
`
}
