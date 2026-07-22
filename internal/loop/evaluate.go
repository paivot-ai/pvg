package loop

import (
	"fmt"
	"sort"
	"strings"
)

// ComputeWorkSignature fingerprints the observable work state for the
// signature-based consecutive-wait rule: the stop-count tuple plus the sorted
// set of in_progress story ids. Two consecutive blocked stops with different
// signatures prove forward progress (a story changed bucket or hands), so the
// wait counter resets; identical signatures accumulate toward the escape
// valve. Deterministic: the id set is sorted before joining.
func ComputeWorkSignature(wc WorkCounts, inProgressIDs []string) string {
	ids := append([]string(nil), inProgressIDs...)
	sort.Strings(ids)
	return fmt.Sprintf("r%d.d%d.j%d.i%d.b%d.o%d|%s",
		wc.Ready, wc.Delivered, wc.Rejected, wc.InProgress, wc.Blocked, wc.Other,
		strings.Join(ids, ","))
}

// StopConfig holds all inputs needed for the stop decision.
// This is a value struct -- no I/O, no side effects.
type StopConfig struct {
	Active           bool
	Mode             string
	TargetEpic       string
	PersistState     bool
	Iteration        int
	MaxIterations    int // 0 = unlimited
	ConsecWaits      int
	MaxConsecWaits   int
	WaitIterations   int
	Ready            int
	Delivered        int
	InProgress       int
	Blocked          int
	Other            int
	EpicPendingMerge bool // true when the target epic branch exists but hasn't been merged to main
	// WorkSignature is the current work-state fingerprint (counts tuple plus
	// sorted in_progress story ids; see ComputeWorkSignature). PrevSignature
	// is the fingerprint recorded at the previous blocked stop
	// (State.LastStopSignature). Together they drive the signature-based
	// consecutive-wait rule in EvaluateStop.
	WorkSignature string
	PrevSignature string
}

// StopDecision is the output of EvaluateStop.
type StopDecision struct {
	Allow          bool   // true = allow session exit
	Reason         string // human-readable explanation
	RemoveState    bool   // true = clean up state file on exit
	NewIteration   int    // updated iteration count
	NewConsecWaits int    // updated consecutive wait count
	NewWaitIters   int    // updated total wait iterations
}

// EvaluateStop is a pure function that decides whether to allow session exit
// or block it (continuing the loop). No I/O -- all context comes from cfg.
func EvaluateStop(cfg StopConfig) StopDecision {
	// Not active -- always allow
	if !cfg.Active {
		return StopDecision{
			Allow:  true,
			Reason: "Loop not active",
		}
	}

	nextIter := cfg.Iteration + 1

	// Max iterations reached
	if cfg.MaxIterations > 0 && nextIter >= cfg.MaxIterations {
		return StopDecision{
			Allow:        true,
			Reason:       "Max iterations reached",
			RemoveState:  true,
			NewIteration: nextIter,
		}
	}

	actionable := cfg.Ready + cfg.Delivered
	total := cfg.Ready + cfg.Delivered + cfg.InProgress + cfg.Blocked + cfg.Other

	// All nd items complete but epic branch still exists -- completion gate pending.
	// The dispatcher must run e2e tests, Anchor review, merge to main, and retro
	// before the loop can exit.
	if total == 0 && cfg.EpicPendingMerge {
		return StopDecision{
			Allow:          false,
			Reason:         "Epic completion gate pending -- merge epic to main before exit",
			NewIteration:   nextIter,
			NewConsecWaits: 0, // reset: this is real work, not a wait
			NewWaitIters:   cfg.WaitIterations,
		}
	}

	// All dev work complete (total==0)
	if total == 0 {
		return StopDecision{
			Allow:        true,
			Reason:       "All work complete",
			RemoveState:  true,
			NewIteration: nextIter,
		}
	}

	// No actionable work remaining (only blocked items)
	if actionable == 0 && cfg.InProgress == 0 {
		if cfg.Other > 0 {
			return StopDecision{
				Allow:        true,
				Reason:       "Non-dispatcher workflow states remain",
				RemoveState:  false,
				NewIteration: nextIter,
			}
		}
		return StopDecision{
			Allow:        true,
			Reason:       "No actionable work remains",
			RemoveState:  true,
			NewIteration: nextIter,
		}
	}

	// Work exists but the dispatcher may be at capacity (agents running,
	// concurrency limits reached). Track consecutive waits by WORK-STATE
	// SIGNATURE: when the signature (counts tuple + sorted in_progress story
	// ids) differs from the previous stop's, the loop IS progressing
	// synchronously (merges, PM reviews, the epic gate), so the counter
	// resets to 1 instead of accumulating toward the escape valve -- a
	// synchronously progressing loop must never be halted by the valve,
	// because no background agent exists to resume it.
	//
	// Deliberate history (v1.34.0): both wait-like and actionable stops
	// increment so the valve stays reachable when nothing changes; identical
	// signatures therefore still increment, and after MaxConsecWaits
	// identical stops the valve allows exit while PRESERVING state (see
	// below) so background agent completions can resume the loop. An empty
	// signature (caller could not compute one) is treated as identical --
	// failing toward the valve keeps it reachable.
	newConsec := cfg.ConsecWaits + 1
	if cfg.WorkSignature != "" && cfg.WorkSignature != cfg.PrevSignature {
		newConsec = 1
	}
	newWaitIters := cfg.WaitIterations + 1

	if newConsec >= cfg.MaxConsecWaits {
		return StopDecision{
			Allow:          true,
			Reason:         "No progress after consecutive wait iterations",
			RemoveState:    !cfg.PersistState,
			NewIteration:   nextIter,
			NewConsecWaits: 0, // reset budget for next session
			NewWaitIters:   newWaitIters,
		}
	}

	// Determine reason based on what's pending
	reason := "Actionable work remains"
	if cfg.Delivered > 0 && cfg.Ready == 0 && cfg.InProgress == 0 {
		reason = "Delivered stories await PM review"
	} else if cfg.Ready == 0 && cfg.InProgress > 0 {
		reason = "Waiting for in-progress work to complete"
	}

	return StopDecision{
		Allow:          false,
		Reason:         reason,
		NewIteration:   nextIter,
		NewConsecWaits: newConsec,
		NewWaitIters:   newWaitIters,
	}
}
