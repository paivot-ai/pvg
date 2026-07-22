package guard

import (
	"path/filepath"

	"github.com/paivot-ai/pvg/internal/dispatcher"
)

// executionActive reports whether a live Paivot execution session governs the
// project: an execution loop is active or dispatcher mode is enabled.
//
// This is the predicate for the BEHAVIORAL guards (background bash, story
// checkout, worktree cd, worktree-agent checkout, CWD drift). Those guards
// police live coordination sessions; in a plain maintenance session on a
// Paivot-managed repo (settings file present, nothing running) they must stay
// silent -- e.g. backgrounded Bash is perfectly legitimate there.
//
// `pvg loop setup` enables dispatcher mode itself, so a bare /piv-loop run
// satisfies the dispatcher leg; the loop-state leg remains as defense in
// depth for states written by older setups.
func executionActive(projectRoot string) bool {
	if projectRoot == "" {
		return false
	}

	if isLoopActiveFrom(projectRoot) {
		return true
	}

	state, _, err := dispatcher.ReadStateRoot(projectRoot)
	return err == nil && state.Enabled
}

// coordinationActive reports whether Paivot coordination governs the project
// AT ALL: a live execution session (executionActive) or the project carries
// the Paivot settings file (.vault/knowledge/.settings.yaml).
//
// This stronger predicate is reserved for the acceptance-contract checks
// (merge gate, label contract, FSM). Those protect recorded story state --
// e.g. merging an unaccepted story branch -- which must hold on any
// Paivot-managed repo even between sessions. Behavioral guards must use
// executionActive instead.
func coordinationActive(projectRoot string) bool {
	if projectRoot == "" {
		return false
	}

	if executionActive(projectRoot) {
		return true
	}

	_, root, found := findAncestorPath(projectRoot, filepath.Join(".vault", "knowledge", ".settings.yaml"))
	return found && root != ""
}
