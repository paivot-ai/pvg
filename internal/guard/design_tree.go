package guard

// The machinery design tree is read-only for delivery agents.
//
// On a machinery-first project the design (domain model, Architecture
// Contract, machines, build plan) is finished before Paivot runs, and every
// later change goes through machinery's revision protocol (the design owner
// runs the conductor, regenerates the oracles, then `pvg story sync-oracle`
// maps the stable-id diff onto stories). A developer that "fixes" a failing
// oracle-derived test by editing the design, or an Sr PM that rewords a
// contract to make a story fit, breaks the chain the whole gate set hangs
// on. The machinery plugin's own hook denies hand-edits to GENERATED
// artifacts; this guard denies the SOURCES too, for every tracked delivery
// agent and for the coordinator while a loop runs. Only an architect agent
// tracked at the writing path, or the coordinator outside a loop (the user
// running a design revision in a dispatcher session), may write there.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/design"
	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/settings"
)

const designTreeArchitect = "paivot-graph:architect"

// checkDesignTreeWrite applies the read-only rule to one write target. The
// second return value reports whether the rule applied at all (the target
// is under the design tree of a project where the substrate applies); when
// it did not, the caller falls through to the ordinary D&F checks.
//
// Milestone acceptance is the one exception, and it is held by path and by
// content rather than by trusting a role with the whole tree: see
// design_acceptance.go.
func checkDesignTreeWrite(projectRoot, root string, state *dispatcher.State, intent designWriteIntent) (Result, bool) {
	target := intent.abs
	if target == "" || isFixturePath(target) {
		return Result{Allowed: true}, false
	}
	cfg, _ := design.Load(root)
	rel, abs, under := designRelPath(root, projectRoot, cfg.Dir, target)
	if !under {
		return Result{Allowed: true}, false
	}
	sett := settings.LoadFile(filepath.Join(root, settingsPath))
	if _, applies, _ := design.Applies(root, design.MachinerySetting(sett)); !applies {
		return Result{Allowed: true}, false
	}
	intent.rel, intent.abs = rel, abs

	if dispatcher.HasActiveAgentTypeAtPath(state, designTreeArchitect, projectRoot) {
		return Result{Allowed: true}, true
	}
	if agentType := trackedAgentAtPath(state, projectRoot); agentType != "" {
		who := "the " + strings.TrimPrefix(agentType, "paivot-graph:") + " agent"
		if agentType == designTreeAnchor {
			if allowed, specific := acceptanceCarveOut(intent, false); allowed {
				return Result{Allowed: true}, true
			} else if specific != "" {
				return Result{Allowed: false, Reason: acceptanceBlockMsg(cfg.Dir, who, specific)}, true
			}
		} else if underAcceptanceDir(intent.rel) {
			return Result{Allowed: false, Reason: acceptanceBlockMsg(cfg.Dir, who,
				"acceptance evidence is written by the reviewing Anchor at the milestone seal gate, never by a delivering role: a role that writes its own acceptance evidence has written its own report card.")}, true
		}
		return Result{Allowed: false, Reason: designTreeBlockMsg(cfg.Dir, who)}, true
	}
	if loop.IsActiveFrom(root) {
		who := "the coordinator while an execution loop is active"
		if allowed, specific := acceptanceCarveOut(intent, true); allowed {
			return Result{Allowed: true}, true
		} else if specific != "" {
			return Result{Allowed: false, Reason: acceptanceBlockMsg(cfg.Dir, who, specific)}, true
		}
		return Result{Allowed: false, Reason: designTreeBlockMsg(cfg.Dir, who)}, true
	}
	return Result{Allowed: true}, true
}

// designRelPath resolves target against the design tree of either the
// orchestrator root or the caller's worktree (a checked-out design tree is
// the design tree wherever the checkout lives), returning the slash path
// relative to that design directory and the absolute path it resolved to.
// Relative targets resolve against the caller's cwd (projectRoot).
func designRelPath(root, projectRoot, dir, target string) (string, string, bool) {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	if dir == "" {
		return "", "", false
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectRoot, target)
	}
	abs = filepath.Clean(abs)
	for _, base := range []string{projectRoot, root} {
		if base == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(base), abs)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == dir {
			return "", abs, true
		}
		if inside, ok := strings.CutPrefix(rel, dir+"/"); ok {
			return inside, abs, true
		}
	}
	return "", "", false
}

// trackedAgentAtPath returns the type of the first non-architect agent
// tracked at cwd (or a parent worktree of it), or "" when none is.
func trackedAgentAtPath(state *dispatcher.State, cwd string) string {
	if state == nil {
		return ""
	}
	cleanCWD := filepath.Clean(cwd)
	for id, agentType := range state.ActiveAgents {
		if agentType == designTreeArchitect {
			continue
		}
		worktree := filepath.Clean(state.AgentWorktrees[id])
		if worktree == "." || worktree == "" {
			continue
		}
		if cleanCWD == worktree || strings.HasPrefix(cleanCWD, worktree+string(filepath.Separator)) {
			return agentType
		}
	}
	return ""
}

func designTreeBlockMsg(dir, who string) string {
	return fmt.Sprintf(
		"BLOCKED: the machinery design tree (%s/) is read-only for %s while design.machinery applies.\n"+
			"The design was completed under machinery governance; delivery agents never edit its sources or its generated artifacts.\n"+
			"  - A test derived from an oracle row that cannot pass is a DESIGN DEFECT: stop and report it (the design changes first, then\n"+
			"    `machinery oracle` regenerates, then tests follow). Never adjust the test, the oracle, or the design to fit.\n"+
			"  - A missing contract is a design revision request to the user, not an edit and not an Architect spawn.\n"+
			"  - Design revisions run through machinery's revision protocol (the design owner, outside the loop), then\n"+
			"    `pvg story sync-oracle --base <ref>` maps the stable-id diff onto the stories to reopen, retest, or write.",
		strings.Trim(dir, "/"), who)
}
