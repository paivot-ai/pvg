package design

import (
	"strings"
	"testing"
)

func TestGateListHelpers(t *testing.T) {
	if got := strings.Join(GateList(" GM , gs,, Gt "), ","); got != "gm,gs,gt" {
		t.Fatalf("GateList: %q", got)
	}
	if !GateListHas("gm,gs,gt", "GT") || GateListHas("gm,gs", "gt") {
		t.Fatal("GateListHas must be case-insensitive and exact")
	}
	if got := GateListWithout("gm,gt,gs", "gt"); got != "gm,gs" {
		t.Fatalf("GateListWithout: %q", got)
	}
	if got := GateListWith("gm,gs", "g4", "gm", "gt"); got != "gm,gs,g4,gt" {
		t.Fatalf("GateListWith must append only what is absent: %q", got)
	}
}

// TestRedGateConfigDropsGtButKeepsEveryOtherGate is the G2 contract: the
// story-level RED approval never runs the whole-design test gate, and it
// never drops anything else. Gt at story granularity would block the FIRST
// story until the LAST one exists.
func TestRedGateConfigDropsGtButKeepsEveryOtherGate(t *testing.T) {
	staged := Config{Dir: "design", Gates: "gm,gs,gp,gi,gn,gc,g2,g3,gx,gk,gb,g4,gt", Impl: "."}
	red, note := RedGateConfig(staged, "")

	if GateListHas(red.Gates, "gt") {
		t.Fatalf("gt must not run at story granularity: %q", red.Gates)
	}
	for _, g := range GateList(staged.Gates) {
		if g == "gt" {
			continue
		}
		if !GateListHas(red.Gates, g) {
			t.Errorf("RED gate list dropped %q; only gt may be removed (got %q)", g, red.Gates)
		}
	}
	if red.Impl != "." {
		t.Errorf("g4 is still staged, so --impl must ride along: %q", red.Impl)
	}
	if !strings.Contains(note, "minus gt") {
		t.Errorf("derivation note must name the staging: %q", note)
	}
}

func TestRedGateConfigImplAndExplicitList(t *testing.T) {
	// g4 gone too: nothing impl-only remains, so --impl is dropped rather
	// than handed to machinery for gates that ignore it.
	red, _ := RedGateConfig(Config{Dir: "design", Gates: "gm,gs,gt", Impl: "."}, "")
	if red.Gates != "gm,gs" || red.Impl != "" {
		t.Fatalf("no impl gate left -> no --impl: gates=%q impl=%q", red.Gates, red.Impl)
	}

	// No staged list at all: machinery's default selection would pull the
	// impl-only gates in whenever --impl is present, so --impl is dropped.
	red, note := RedGateConfig(Config{Dir: "design", Impl: "."}, "")
	if red.Gates != "" || red.Impl != "" {
		t.Fatalf("unstaged config must not run impl gates: %+v", red)
	}
	if !strings.Contains(note, "no staged gate list") {
		t.Errorf("note: %q", note)
	}

	// An explicit design.red_gates wins verbatim, and carries --impl only
	// when it actually names an impl gate.
	red, note = RedGateConfig(Config{Dir: "design", Gates: "gm,gt", Impl: "."}, " G2 , g4 ")
	if red.Gates != "g2,g4" || red.Impl != "." {
		t.Fatalf("explicit list with g4 keeps --impl: %+v", red)
	}
	if !strings.Contains(note, "design.red_gates=g2,g4") {
		t.Errorf("note: %q", note)
	}
	red, _ = RedGateConfig(Config{Dir: "design", Gates: "gm,gt", Impl: "."}, "g2")
	if red.Impl != "" {
		t.Fatalf("explicit list without an impl gate drops --impl: %+v", red)
	}
}

// TestSealConfigForcesTheWholeDesignGate: the seal is where Gt is NOT
// weakened. It forces gt and g4 in and refuses without an impl dir, because
// a seal with nothing under check verifies nothing.
func TestSealConfigForcesTheWholeDesignGate(t *testing.T) {
	seal, err := SealConfig(Config{Dir: "design", Gates: "gm,gs,g4", Impl: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !GateListHas(seal.Gates, "gt") || !GateListHas(seal.Gates, "g4") {
		t.Fatalf("seal must force gt and g4: %q", seal.Gates)
	}
	if seal.Impl != "." {
		t.Fatalf("seal runs over the impl dir: %q", seal.Impl)
	}
	// No duplicate g4.
	if strings.Count(seal.Gates, "g4") != 1 {
		t.Fatalf("g4 duplicated: %q", seal.Gates)
	}

	if _, err := SealConfig(Config{Dir: "design", Gates: "gm", Impl: ""}); err == nil {
		t.Fatal("a seal without an impl dir must refuse, not silently pass")
	}

	// Unstaged config: machinery's default selection already covers gt/g4
	// under --impl, so the list stays empty rather than inventing one.
	seal, err = SealConfig(Config{Dir: "design", Impl: "."})
	if err != nil || seal.Gates != "" {
		t.Fatalf("unstaged seal: %+v err=%v", seal, err)
	}
}
