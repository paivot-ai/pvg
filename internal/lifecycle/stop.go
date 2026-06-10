package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/settings"
	"github.com/paivot-ai/pvg/internal/vaultcfg"
)

// Stop outputs a knowledge capture reminder when Claude tries to stop.
// If an execution loop is active, it evaluates whether to continue or allow exit.
// Reads the Stop Capture Checklist from the vault or uses a static fallback.
func Stop() error {
	cwd, _ := os.Getwd()

	// Loop check: if active, handle loop logic and return early
	if loop.IsActiveFrom(cwd) {
		return checkLoop(cwd)
	}

	v, err := vaultcfg.OpenVault()
	if err == nil {
		result, rerr := v.Read("Stop Capture Checklist", "")
		if rerr == nil && result.Content != "" {
			fmt.Println("[VAULT] Stop capture check (from vault):")
			fmt.Println()
			fmt.Println(result.Content)
			outputTwoTierReminder()
			return nil
		}
	}

	// Try direct file read
	vaultDir, derr := vaultcfg.VaultDir()
	if derr == nil {
		path := filepath.Join(vaultDir, "conventions", "Stop Capture Checklist.md")
		data, ferr := os.ReadFile(path)
		if ferr == nil && len(data) > 0 {
			fmt.Println("[VAULT] Stop capture check (from vault):")
			fmt.Println()
			fmt.Println(string(data))
			outputTwoTierReminder()
			return nil
		}
	}

	// Static fallback
	fmt.Print(staticStopChecklist())
	outputTwoTierReminder()
	return nil
}

func outputTwoTierReminder() {
	cwd, _ := os.Getwd()
	knowledgeDir := filepath.Join(cwd, ".vault", "knowledge")
	if info, err := os.Stat(knowledgeDir); err == nil && info.IsDir() {
		fmt.Print(`
[VAULT] Remember: save to the right tier.
  - Universal insights -> global vault (_inbox/)
  - Project-specific insights -> .vault/knowledge/ (local)
`)
	}
}

func staticStopChecklist() string {
	return `[VAULT] Stop capture check:

Before ending this session, confirm you have considered each of these:

- [ ] Did you capture any DECISIONS made this session?
- [ ] Did you capture any PATTERNS discovered?
- [ ] Did you capture any DEBUG INSIGHTS?
- [ ] Did you update the PROJECT INDEX NOTE?
- [ ] Did you capture project-specific knowledge to .vault/knowledge/?

If none apply (trivial session), that is fine -- but confirm it was considered.

Use: vlt vault="Claude" create name="<Title>" path="_inbox/<Title>.md" content="..." silent
`
}

// checkLoop evaluates whether the execution loop should continue or allow exit.
// On block: updates state and emits continuation JSON to stdout.
// On allow: logs reason, removes state if needed.
func checkLoop(cwd string) error {
	state, root, err := loop.ReadStateRoot(cwd)
	if err != nil {
		// State disappeared -- fail open, allow exit
		fmt.Fprintln(os.Stderr, "[LOOP] Could not read loop state, allowing exit")
		return nil
	}

	// Query nd for work counts -- fail open on error
	wc, err := loop.QueryWorkCounts(root, state.Mode, state.TargetEpic)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[LOOP] Could not query nd: %v -- allowing exit with loop state preserved\n", err)
		return nil
	}

	// Check if the target epic branch still exists locally (unmerged).
	// If all nd items are closed but the branch is still around, the
	// completion gate (e2e + Anchor + merge to main + retro) hasn't run.
	epicPending := loop.EpicBranchExists(root, state.TargetEpic)

	cfg := loop.StopConfig{
		Active:           state.Active,
		Mode:             state.Mode,
		TargetEpic:       state.TargetEpic,
		PersistState:     isLoopPersistEnabled(root),
		Iteration:        state.Iteration,
		MaxIterations:    state.MaxIterations,
		ConsecWaits:      state.ConsecutiveWaits,
		MaxConsecWaits:   state.MaxConsecutiveWaits,
		WaitIterations:   state.WaitIterations,
		Ready:            wc.Ready,
		Delivered:        wc.Delivered,
		InProgress:       wc.InProgress,
		Blocked:          wc.Blocked,
		Other:            wc.Other,
		EpicPendingMerge: epicPending,
	}

	decision := loop.EvaluateStop(cfg)

	if decision.Allow {
		fmt.Fprintf(os.Stderr, "[LOOP] %s\n", decision.Reason)
		if decision.RemoveState {
			_ = loop.RemoveState(root)
		} else {
			// Escape valve: keep state active but update counters (reset ConsecWaits).
			// Background agent completions will resume the loop in a new session.
			state.Iteration = decision.NewIteration
			state.ConsecutiveWaits = decision.NewConsecWaits
			state.WaitIterations = decision.NewWaitIters
			_ = loop.WriteState(root, state)
		}
		return nil
	}

	// Block exit: update state and emit continuation JSON
	state.Iteration = decision.NewIteration
	state.ConsecutiveWaits = decision.NewConsecWaits
	state.WaitIterations = decision.NewWaitIters
	if err := loop.WriteState(root, state); err != nil {
		fmt.Fprintf(os.Stderr, "[LOOP] Could not update state: %v -- allowing exit\n", err)
		return nil
	}

	// Build and emit continuation. Claude Code's Stop hook contract only
	// recognizes top-level "decision" and "reason" -- the full continuation
	// prompt must be the reason or it never reaches the model.
	maxIterStr := "unlimited"
	if state.MaxIterations > 0 {
		maxIterStr = strconv.Itoa(state.MaxIterations)
	}
	prompt := BuildContinuationPrompt(state, &decision, maxIterStr, &wc)

	continuation := map[string]any{
		"decision": "block",
		"reason":   prompt,
	}

	data, err := json.Marshal(continuation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[LOOP] Could not marshal continuation: %v\n", err)
		return nil
	}

	fmt.Println(string(data))
	return nil
}

// isLoopPersistEnabled checks if loop state should persist across sessions.
// Default is true (loop survives session boundaries for background agent flow).
func isLoopPersistEnabled(cwd string) bool {
	path := filepath.Join(cwd, ".vault", "knowledge", ".settings.yaml")
	s := settings.LoadFile(path)
	v, ok := s["loop.persist_across_sessions"]
	if !ok {
		return true // default
	}
	return v != "false"
}

// BuildContinuationPrompt creates the prompt for the next loop iteration.
// Context-aware: minimal prompt when waiting for agents, fuller prompt when
// there is actionable work the dispatcher can act on.
func BuildContinuationPrompt(state *loop.State, decision *loop.StopDecision, maxIterStr string, wc *loop.WorkCounts) string {
	header := fmt.Sprintf(
		"[LOOP] Iteration %d/%s | Ready: %d, Delivered: %d, In-progress: %d, Blocked: %d, Other: %d | %s\n",
		decision.NewIteration, maxIterStr,
		wc.Ready, wc.Delivered, wc.InProgress, wc.Blocked, wc.Other,
		decision.Reason,
	)

	// Always show the current epic context.
	epicCtx := ""
	if state.TargetEpic != "" {
		epicCtx = fmt.Sprintf("Current epic: %s (auto-rotate=%v).\n", state.TargetEpic, state.AutoRotate)
	}

	// Epic completion gate pending: all stories closed but epic branch not merged.
	// Direct the dispatcher to run the full completion gate.
	total := wc.Ready + wc.Delivered + wc.InProgress + wc.Blocked + wc.Other
	if total == 0 && state.TargetEpic != "" {
		prompt := header + epicCtx
		prompt += "\nAll stories in this epic are closed. Run the EPIC COMPLETION GATE now:\n"
		prompt += "1. Run `pvg loop next --json` -- it will return `epic_complete`\n"
		prompt += "2. Step 1: Run the full test suite on epic/" + state.TargetEpic + " (e2e verification gate)\n"
		prompt += "3. Step 2: Spawn Anchor milestone review\n"
		prompt += "4. Step 3: Merge epic/" + state.TargetEpic + " to main (solo_dev) or create PR (team)\n"
		prompt += "5. Step 4: Spawn retro agent\n"
		prompt += "6. After retro: rotate to next epic or allow exit if this was the last epic\n"
		prompt += "\nDo NOT exit without completing the gate. The epic branch must be merged and cleaned up.\n"
		return prompt
	}

	// Wait-like: nothing ready to spawn, agents are running
	if wc.Ready == 0 && wc.InProgress > 0 {
		prompt := header + epicCtx
		if wc.Delivered > 0 {
			prompt += fmt.Sprintf("\n%d delivered stories await PM review -- spawn PM-Acceptor.\n", wc.Delivered)
		} else {
			prompt += "\nBackground agents are working. Wait for completions.\nDo NOT produce explanatory output or spawn new agents.\n"
		}
		return prompt
	}

	// Actionable ready work exists
	prompt := header + epicCtx + "\nContinue. Run `pvg loop next --json` to get the next action.\n"
	if wc.Delivered > 0 {
		prompt += fmt.Sprintf("- PM-Acceptor: %d delivered\n", wc.Delivered)
	}
	if wc.Ready > 0 {
		prompt += fmt.Sprintf("- Developer: %d ready\n", wc.Ready)
	}
	// Protocol-boilerplate refresh: the blocked-stop reason is rendered
	// prominently in the transcript on EVERY iteration ("Stop hook error:"
	// is Claude Code's fixed label for any blocked stop). The dispatcher
	// already carries the full protocol from piv-loop.md and the PreCompact
	// reminder, so re-inject the long reminders periodically instead of
	// every iteration to keep transcript noise down.
	if decision.NewIteration <= 1 || decision.NewIteration%5 == 0 {
		prompt += "\nAll work is scoped to the current epic. Do NOT query nd globally for dispatch.\n"
		prompt += "Concurrency: within current epic only, stack-dependent limits (heavy stacks: 2 dev / 1 PM / 3 total; light stacks: 4 dev / 2 PM / 6 total). Dispatcher-only.\n"
	}

	return prompt
}
