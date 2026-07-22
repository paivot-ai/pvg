package loop

import "testing"

func TestEvaluateStop_NotActive(t *testing.T) {
	d := EvaluateStop(StopConfig{Active: false})
	if !d.Allow {
		t.Error("expected allow when not active")
	}
	if d.RemoveState {
		t.Error("should not remove state when not active")
	}
}

func TestEvaluateStop_MaxIterations(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:        true,
		Iteration:     49,
		MaxIterations: 50,
		Ready:         5, // actionable work exists
	})
	if !d.Allow {
		t.Error("expected allow at max iterations")
	}
	if !d.RemoveState {
		t.Error("expected remove state at max iterations")
	}
	if d.Reason != "Max iterations reached" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_UnlimitedIterations(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      999,
		MaxIterations:  0, // unlimited
		MaxConsecWaits: 3,
		Ready:          1,
	})
	if d.Allow {
		t.Error("expected block with unlimited iterations and ready work")
	}
}

func TestEvaluateStop_AllComplete(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:        true,
		Iteration:     5,
		MaxIterations: 50,
	})
	if !d.Allow {
		t.Error("expected allow when all complete")
	}
	if !d.RemoveState {
		t.Error("expected remove state when all complete")
	}
	if d.Reason != "All work complete" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_AllBlocked(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:        true,
		Iteration:     3,
		MaxIterations: 50,
		Blocked:       4,
	})
	if !d.Allow {
		t.Error("expected allow when all blocked")
	}
	if !d.RemoveState {
		t.Error("expected remove state when all blocked")
	}
	if d.Reason != "No actionable work remains" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_CustomWorkflowStatesRemain(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:        true,
		Iteration:     3,
		MaxIterations: 50,
		Other:         2,
	})
	if !d.Allow {
		t.Error("expected allow when only non-dispatcher workflow states remain")
	}
	if d.RemoveState {
		t.Error("expected state to be preserved while custom workflow states remain")
	}
	if d.Reason != "Non-dispatcher workflow states remain" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_ActionableReady(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      2,
		MaxIterations:  50,
		MaxConsecWaits: 3,
		Ready:          3,
	})
	if d.Allow {
		t.Error("expected block with ready work")
	}
	if d.NewConsecWaits != 1 {
		t.Errorf("expected consec waits=1, got %d", d.NewConsecWaits)
	}
	if d.Reason != "Actionable work remains" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_ActionableDelivered(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      2,
		MaxIterations:  50,
		MaxConsecWaits: 3,
		Delivered:      2,
	})
	if d.Allow {
		t.Error("expected block with delivered work pending PM review")
	}
	if d.Reason != "Delivered stories await PM review" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_DeliveredOnly(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      2,
		MaxIterations:  50,
		MaxConsecWaits: 3,
		Ready:          0,
		InProgress:     0,
		Delivered:      2,
		Blocked:        0,
	})
	if d.Allow {
		t.Error("expected block with only delivered work")
	}
	if d.Reason != "Delivered stories await PM review" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_ActionableMixed(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      2,
		MaxIterations:  50,
		MaxConsecWaits: 3,
		Ready:          1,
		Delivered:      1,
		InProgress:     2,
		Blocked:        1,
	})
	if d.Allow {
		t.Error("expected block with mixed actionable work")
	}
	if d.NewConsecWaits != 1 {
		t.Errorf("expected consec waits=1, got %d", d.NewConsecWaits)
	}
}

func TestEvaluateStop_WaitLike_FirstWait(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      5,
		MaxIterations:  50,
		ConsecWaits:    0,
		MaxConsecWaits: 3,
		WaitIterations: 0,
		InProgress:     2,
	})
	if d.Allow {
		t.Error("expected block on first wait")
	}
	if d.NewConsecWaits != 1 {
		t.Errorf("expected consec waits=1, got %d", d.NewConsecWaits)
	}
	if d.NewWaitIters != 1 {
		t.Errorf("expected wait iters=1, got %d", d.NewWaitIters)
	}
}

func TestEvaluateStop_WaitLike_SecondWait(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      6,
		MaxIterations:  50,
		ConsecWaits:    1,
		MaxConsecWaits: 3,
		WaitIterations: 1,
		InProgress:     2,
	})
	if d.Allow {
		t.Error("expected block on second wait")
	}
	if d.NewConsecWaits != 2 {
		t.Errorf("expected consec waits=2, got %d", d.NewConsecWaits)
	}
	if d.NewWaitIters != 2 {
		t.Errorf("expected wait iters=2, got %d", d.NewWaitIters)
	}
}

func TestEvaluateStop_WaitLike_ThresholdReached(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      7,
		MaxIterations:  50,
		ConsecWaits:    2,
		MaxConsecWaits: 3,
		WaitIterations: 2,
		PersistState:   true,
		InProgress:     2,
	})
	if !d.Allow {
		t.Error("expected allow at wait threshold")
	}
	if d.RemoveState {
		t.Error("expected state preserved at wait threshold (background agents resume)")
	}
	if d.NewConsecWaits != 0 {
		t.Errorf("expected consec waits reset to 0, got %d", d.NewConsecWaits)
	}
	if d.Reason != "No progress after consecutive wait iterations" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_WaitLike_ThresholdReachedWithoutPersistence(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      7,
		MaxIterations:  50,
		ConsecWaits:    2,
		MaxConsecWaits: 3,
		WaitIterations: 2,
		PersistState:   false,
		InProgress:     2,
	})
	if !d.Allow {
		t.Error("expected allow at wait threshold")
	}
	if !d.RemoveState {
		t.Error("expected state removal when persistence is disabled")
	}
}

func TestEvaluateStop_ConsecWaitsAccumulate_AcrossStates(t *testing.T) {
	// First: wait (in-progress only)
	d1 := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      5,
		MaxIterations:  50,
		ConsecWaits:    0,
		MaxConsecWaits: 3,
		InProgress:     2,
	})
	if d1.NewConsecWaits != 1 {
		t.Fatalf("setup: expected consec waits=1, got %d", d1.NewConsecWaits)
	}

	// Then: actionable work appears -- should continue accumulating
	d2 := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      d1.NewIteration,
		MaxIterations:  50,
		ConsecWaits:    d1.NewConsecWaits,
		MaxConsecWaits: 3,
		WaitIterations: d1.NewWaitIters,
		Ready:          1,
		InProgress:     1,
	})
	if d2.NewConsecWaits != 2 {
		t.Errorf("expected consec waits=2 (accumulated), got %d", d2.NewConsecWaits)
	}

	// Third: still actionable -- should hit threshold and allow exit
	d3 := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      d2.NewIteration,
		MaxIterations:  50,
		ConsecWaits:    d2.NewConsecWaits,
		MaxConsecWaits: 3,
		WaitIterations: d2.NewWaitIters,
		Ready:          1,
		InProgress:     1,
	})
	if !d3.Allow {
		t.Error("expected allow after reaching threshold")
	}
	if d3.NewConsecWaits != 0 {
		t.Errorf("expected consec waits reset to 0, got %d", d3.NewConsecWaits)
	}
}

func TestEvaluateStop_ActionableThreshold_AllowsExit(t *testing.T) {
	// Simulate dispatcher at capacity: ready work exists but can't progress
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      10,
		MaxIterations:  50,
		ConsecWaits:    2,
		MaxConsecWaits: 3,
		WaitIterations: 2,
		PersistState:   true,
		Ready:          3,
		Delivered:      2,
	})
	if !d.Allow {
		t.Error("expected allow when actionable threshold reached")
	}
	if d.RemoveState {
		t.Error("expected state preserved (background agents resume)")
	}
	if d.NewConsecWaits != 0 {
		t.Errorf("expected consec waits reset to 0, got %d", d.NewConsecWaits)
	}
	if d.Reason != "No progress after consecutive wait iterations" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_ActionableThreshold_RemovesStateWithoutPersistence(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      10,
		MaxIterations:  50,
		ConsecWaits:    2,
		MaxConsecWaits: 3,
		WaitIterations: 2,
		PersistState:   false,
		Ready:          3,
		Delivered:      2,
	})
	if !d.Allow {
		t.Error("expected allow when actionable threshold reached")
	}
	if !d.RemoveState {
		t.Error("expected state removal when persistence is disabled")
	}
}

func TestEvaluateStop_EpicPendingMerge_BlocksExit(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:           true,
		Iteration:        10,
		MaxIterations:    50,
		MaxConsecWaits:   3,
		EpicPendingMerge: true,
		// All nd counts are zero -- stories are all closed
	})
	if d.Allow {
		t.Error("expected block when epic branch is pending merge")
	}
	if d.Reason != "Epic completion gate pending -- merge epic to main before exit" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
	if d.NewConsecWaits != 0 {
		t.Errorf("expected consec waits reset to 0, got %d", d.NewConsecWaits)
	}
}

func TestEvaluateStop_EpicPendingMerge_False_AllowsExit(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:           true,
		Iteration:        10,
		MaxIterations:    50,
		EpicPendingMerge: false,
		// All nd counts are zero
	})
	if !d.Allow {
		t.Error("expected allow when epic branch is NOT pending merge and all work complete")
	}
	if !d.RemoveState {
		t.Error("expected remove state when all complete")
	}
	if d.Reason != "All work complete" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

func TestEvaluateStop_EpicPendingMerge_WithActiveWork_NoEffect(t *testing.T) {
	// EpicPendingMerge should not affect behavior when there's still active work
	d := EvaluateStop(StopConfig{
		Active:           true,
		Iteration:        5,
		MaxIterations:    50,
		MaxConsecWaits:   3,
		EpicPendingMerge: true,
		Ready:            2,
	})
	if d.Allow {
		t.Error("expected block with ready work (regardless of EpicPendingMerge)")
	}
	if d.Reason != "Actionable work remains" {
		t.Errorf("unexpected reason: %s", d.Reason)
	}
}

// Abandonment regression (epic A drained, epic B blocked): with epic-scoped
// counts the drained target epic yields an all-zero tuple no matter what
// sibling epics look like, so the completion gate must BLOCK the stop (state
// preserved) instead of the old backlog-wide path deciding "No actionable
// work remains" and deleting the state with the epic unmerged.
func TestEvaluateStop_DrainedEpicWithBlockedSibling_BlocksOnCompletionGate(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:           true,
		Mode:             "epic",
		TargetEpic:       "PROJ-ea",
		Iteration:        10,
		MaxIterations:    50,
		MaxConsecWaits:   3,
		EpicPendingMerge: true,
		// Epic-scoped counts: all zero -- epic B's blocked story is outside
		// the target subtree and no longer pollutes the tuple.
	})
	if d.Allow {
		t.Fatal("expected block: drained target epic must run its completion gate")
	}
	if d.Reason != "Epic completion gate pending -- merge epic to main before exit" {
		t.Fatalf("expected completion-gate reason, got: %s", d.Reason)
	}
	if d.RemoveState {
		t.Fatal("state must NOT be removed while the completion gate is pending")
	}
}

// ---------------------------------------------------------------------------
// Signature-based consecutive waits
// ---------------------------------------------------------------------------

func TestComputeWorkSignature_Deterministic(t *testing.T) {
	wc := WorkCounts{Ready: 1, Delivered: 2, InProgress: 3}
	a := ComputeWorkSignature(wc, []string{"PROJ-b", "PROJ-a"})
	b := ComputeWorkSignature(wc, []string{"PROJ-a", "PROJ-b"})
	if a != b {
		t.Fatalf("signature must be order-independent: %q vs %q", a, b)
	}
	c := ComputeWorkSignature(WorkCounts{Ready: 2, Delivered: 2, InProgress: 3}, []string{"PROJ-a", "PROJ-b"})
	if a == c {
		t.Fatal("different counts must produce different signatures")
	}
}

// A synchronously progressing loop (signature changes on every stop) must
// never trip the escape valve, no matter how many blocked stops accumulate.
func TestEvaluateStop_ProgressingSignaturesNeverTripValve(t *testing.T) {
	consec := 0
	prev := ""
	for i := 0; i < 10; i++ {
		sig := ComputeWorkSignature(WorkCounts{Ready: 1, InProgress: 10 - i}, nil)
		d := EvaluateStop(StopConfig{
			Active:         true,
			Iteration:      i,
			MaxIterations:  50,
			ConsecWaits:    consec,
			MaxConsecWaits: 3,
			Ready:          1,
			InProgress:     10 - i,
			WorkSignature:  sig,
			PrevSignature:  prev,
		})
		if d.Allow {
			t.Fatalf("stop %d: progressing loop tripped the valve (reason: %s)", i, d.Reason)
		}
		if d.NewConsecWaits != 1 {
			t.Fatalf("stop %d: expected counter reset to 1 on progress, got %d", i, d.NewConsecWaits)
		}
		consec = d.NewConsecWaits
		prev = sig
	}
}

// Identical signatures accumulate and trip the valve at MaxConsecutiveWaits,
// preserving state (escape valve semantics unchanged).
func TestEvaluateStop_IdenticalSignaturesTripValveAtMax(t *testing.T) {
	sig := ComputeWorkSignature(WorkCounts{InProgress: 2}, []string{"PROJ-a", "PROJ-b"})
	consec := 0
	for i := 0; i < 2; i++ {
		d := EvaluateStop(StopConfig{
			Active:         true,
			Iteration:      i,
			MaxIterations:  50,
			ConsecWaits:    consec,
			MaxConsecWaits: 3,
			PersistState:   true,
			InProgress:     2,
			WorkSignature:  sig,
			PrevSignature:  sig,
		})
		if d.Allow {
			t.Fatalf("stop %d: valve tripped early", i)
		}
		if d.NewConsecWaits != i+1 {
			t.Fatalf("stop %d: expected counter %d, got %d", i, i+1, d.NewConsecWaits)
		}
		consec = d.NewConsecWaits
	}

	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      2,
		MaxIterations:  50,
		ConsecWaits:    consec,
		MaxConsecWaits: 3,
		PersistState:   true,
		InProgress:     2,
		WorkSignature:  sig,
		PrevSignature:  sig,
	})
	if !d.Allow {
		t.Fatal("expected valve to allow exit at MaxConsecutiveWaits identical stops")
	}
	if d.RemoveState {
		t.Fatal("escape valve must PRESERVE state")
	}
	if d.NewConsecWaits != 0 {
		t.Fatalf("expected counter reset by valve, got %d", d.NewConsecWaits)
	}
}

// A progress event mid-streak resets the budget: identical, identical,
// different, then identical again must not trip the valve on the fourth stop.
func TestEvaluateStop_ProgressMidStreakResetsBudget(t *testing.T) {
	stale := ComputeWorkSignature(WorkCounts{InProgress: 2}, []string{"PROJ-a"})
	fresh := ComputeWorkSignature(WorkCounts{InProgress: 1}, []string{"PROJ-b"})

	consec := 0
	seq := []struct{ sig, prev string }{
		{stale, ""},    // first stop: differs from empty prev -> reset to 1
		{stale, stale}, // identical -> 2
		{fresh, stale}, // progress -> reset to 1
		{fresh, fresh}, // identical -> 2 (valve NOT tripped)
	}
	for i, s := range seq {
		d := EvaluateStop(StopConfig{
			Active:         true,
			Iteration:      i,
			MaxIterations:  50,
			ConsecWaits:    consec,
			MaxConsecWaits: 3,
			InProgress:     2,
			WorkSignature:  s.sig,
			PrevSignature:  s.prev,
		})
		if d.Allow {
			t.Fatalf("stop %d: valve must not trip after mid-streak progress", i)
		}
		consec = d.NewConsecWaits
	}
	if consec != 2 {
		t.Fatalf("expected counter 2 after reset sequence, got %d", consec)
	}
}

func TestEvaluateStop_IterationIncrement(t *testing.T) {
	d := EvaluateStop(StopConfig{
		Active:        true,
		Iteration:     10,
		MaxIterations: 50,
		Ready:         1,
	})
	if d.NewIteration != 11 {
		t.Errorf("expected iteration=11, got %d", d.NewIteration)
	}
}

func TestEvaluateStop_BlockedPlusInProgress(t *testing.T) {
	// Blocked + in-progress but no actionable = wait-like
	d := EvaluateStop(StopConfig{
		Active:         true,
		Iteration:      3,
		MaxIterations:  50,
		ConsecWaits:    0,
		MaxConsecWaits: 3,
		InProgress:     1,
		Blocked:        2,
	})
	if d.Allow {
		t.Error("expected block: in-progress work exists even with blocked items")
	}
	if d.NewConsecWaits != 1 {
		t.Errorf("expected consec waits=1, got %d", d.NewConsecWaits)
	}
}
