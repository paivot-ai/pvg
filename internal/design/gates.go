package design

// Gate staging for story-granular delivery on a whole-design substrate.
//
// machinery's Gt-tests holds every committed oracle stable id against the
// WHOLE test suite; Paivot approves RED tests one story at a time. Running
// Gt on every RED approval would block the first story until the last one
// exists, so the RED exit gate splits: approve-red runs the design-side
// gates (plus G4 when staged) and checks the story's own cited ids itself,
// and the whole-design Gt runs at the milestone seal (`pvg gates --seal`),
// where it is never weakened.

import (
	"fmt"
	"strings"
)

// GateList parses a comma-separated --gate list into trimmed, lowercased,
// order-preserving entries.
func GateList(list string) []string {
	var out []string
	for _, g := range strings.Split(list, ",") {
		if g = strings.ToLower(strings.TrimSpace(g)); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// GateListHas reports whether the comma-separated gate list names gate.
func GateListHas(list, gate string) bool {
	for _, g := range GateList(list) {
		if g == strings.ToLower(gate) {
			return true
		}
	}
	return false
}

// GateListWithout returns the list with gate removed (order preserved).
func GateListWithout(list, gate string) string {
	var kept []string
	for _, g := range GateList(list) {
		if g != strings.ToLower(gate) {
			kept = append(kept, g)
		}
	}
	return strings.Join(kept, ",")
}

// GateListWith returns the list with the given gates appended when absent.
func GateListWith(list string, gates ...string) string {
	out := GateList(list)
	for _, g := range gates {
		g = strings.ToLower(strings.TrimSpace(g))
		if g != "" && !GateListHas(strings.Join(out, ","), g) {
			out = append(out, g)
		}
	}
	return strings.Join(out, ",")
}

// ImplGates are the gates that only run with --impl (G4-import, Gt-tests).
var ImplGates = []string{"g4", "gt"}

// needsImpl reports whether a gate list names an impl-only gate.
func needsImpl(list string) bool {
	for _, g := range ImplGates {
		if GateListHas(list, g) {
			return true
		}
	}
	return false
}

// RedGateConfig derives the machinery invocation `pvg story approve-red`
// runs, from the staged .machinery.json config and the design.red_gates
// setting. It returns the config and a one-line derivation note for the
// story contract.
//
//   - An explicit design.red_gates list is used verbatim; --impl rides
//     along only when the list names g4 or gt.
//   - Otherwise the staged list minus gt (the whole-design test gate
//     belongs to the seal); --impl stays when g4 remains in the list.
//   - A staged config with no gate list (machinery's default selection)
//     drops --impl: the default selection would run G4 and Gt whenever
//     --impl is present, and both are whole-tree checks.
//
// The per-story half of Gt (every id the story cites carried by a test) is
// checked by approve-red itself, so no RED approval loses it.
func RedGateConfig(cfg Config, redGates string) (Config, string) {
	red := cfg
	if explicit := strings.TrimSpace(redGates); explicit != "" {
		red.Gates = strings.Join(GateList(explicit), ",")
		if !needsImpl(red.Gates) {
			red.Impl = ""
		}
		return red, "design.red_gates=" + red.Gates
	}
	if strings.TrimSpace(cfg.Gates) == "" {
		red.Impl = ""
		return red, "design-side gates only (no staged gate list; impl gates run at pvg gates and the seal)"
	}
	red.Gates = GateListWithout(cfg.Gates, "gt")
	if !needsImpl(red.Gates) {
		red.Impl = ""
	}
	if red.Gates == "" {
		return red, "staged list minus gt leaves no gate (design-side defaults run)"
	}
	return red, "staged gates minus gt (" + red.Gates + ")"
}

// SealConfig derives the whole-design invocation for a milestone seal or
// the final gate: the staged list with gt (and g4) forced in, over the
// configured impl dir. It refuses when no impl dir is configured, because a
// seal without the implementation under check would verify nothing.
func SealConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.Impl) == "" {
		return cfg, fmt.Errorf("seal requires an implementation dir: set \"impl\" in %s (for example \".\") so G4-import and Gt-tests run over the suite", ConfigName)
	}
	seal := cfg
	if strings.TrimSpace(cfg.Gates) != "" {
		seal.Gates = GateListWith(cfg.Gates, "g4", "gt")
	}
	return seal, nil
}
