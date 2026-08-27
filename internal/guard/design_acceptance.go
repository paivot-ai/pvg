package guard

// Milestone acceptance is the ONE governed exception to the read-only design
// tree, and it is deliberately two writes wide.
//
// machinery's Ga-accept gate holds milestone closure to committed evidence:
// a build-plan milestone marked "Status: closed" must carry
// <design>/acceptance/M<n>.yaml whose verdict is ACCEPTED, naming the commit
// the review ran on and every oracle id its DoD cites. In Paivot the Anchor's
// milestone (seal) review WRITES that evidence and the seal-gate coordinator
// performs the closure act. Both writes land inside the tree the G10 rule
// closes, so without a carve-out the discipline cannot be executed at all;
// with a carve-out shaped by intent instead of by path, the rule would leak.
//
// So the exception is path-anchored and content-anchored, never role-trusting:
//
//   - the acceptance evidence is exactly <design>/acceptance/M<n>.yaml, flat,
//     one file per milestone (the only shape machinery reads), written with
//     the Write or Edit tool. Anything else under acceptance/ stays blocked,
//     including a second file for the same milestone, a README, a nested
//     directory, or a .yml spelling.
//   - the closure marker is a change confined to milestone "Status:" LINES of
//     a plan-bearing document (<design>/BUILD.md, or a shard under
//     <design>/BUILD/), verified against the edit's own before/after text.
//     One other changed line and the write is blocked.
//
// Bash writes to both paths stay blocked: a shell command carries no
// verifiable before/after, and `rm`, `sed -i`, `cp` over evidence are exactly
// what the rule exists to stop. Committing the files is unaffected, since git
// is not a write utility this parser tracks.
//
// The exception is NOT gated on the loop's current decision. A pending seal is
// not observable cheaply from a PreToolUse hook, and the flat epic model runs
// the same acceptance act at an epic completion gate where no seal is pending.
// Timing is held by the prompts and, definitively, by Ga itself: evidence that
// lands at the wrong moment fails the gate on its own contents.

import (
	"os"
	"regexp"
	"strings"
)

const designTreeAnchor = "paivot-graph:anchor"

var (
	// acceptanceEvidenceRe matches the one acceptance path shape machinery
	// reads, relative to the design directory.
	acceptanceEvidenceRe = regexp.MustCompile(`^acceptance/M\d+\.yaml$`)

	// milestoneStatusLineRe matches a line that is NOTHING BUT a milestone
	// status declaration. The decorations mirror machinery's own status
	// parser ("Status: closed", "- Status: closed", "**Status:** closed");
	// the full-line anchoring is stricter on purpose, so a line that also
	// says something else is not a status line and does not pass.
	milestoneStatusLineRe = regexp.MustCompile(`(?i)^[ \t]*(?:[-*][ \t]+|\d+\.[ \t]+)?\*{0,2}Status:\*{0,2}[ \t]*[A-Za-z][A-Za-z-]*[ \t]*\*{0,2}[ \t]*$`)
)

// designWriteIntent is what the guard knows about one attempted write: the
// tool that carries it, the path relative to the design directory, and the
// before/after text when the tool provides it. Bash writes carry no text.
type designWriteIntent struct {
	tool      string
	rel       string
	abs       string
	oldString string
	newString string
	content   string
}

// acceptanceCarveOut decides whether one write is a sanctioned milestone
// acceptance act. who is the role attempting it: the Anchor may write the
// evidence file only; the seal-gate coordinator may also land the closure
// marker. The second return value is the reason a NEAR MISS was refused (a
// write that aimed at the carve-out and fell outside it), so the block
// message can say which rule was missed instead of repeating the general one.
func acceptanceCarveOut(w designWriteIntent, coordinator bool) (bool, string) {
	switch {
	case isAcceptanceEvidencePath(w.rel):
		if w.tool != "Write" && w.tool != "Edit" {
			return false, "acceptance evidence is authored with the Write or Edit tool, never through a shell command: a shell write carries no reviewable before and after, and deleting or rewriting evidence in place is what this rule exists to stop."
		}
		return true, ""
	case underAcceptanceDir(w.rel):
		return false, "the acceptance directory holds exactly one file per milestone, named acceptance/M<n>.yaml (M3.yaml, not M3.yml, not M3-round2.yaml, not a subdirectory). git history is the record of prior attempts."
	case coordinator && isPlanDocumentPath(w.rel):
		ok, reason := statusOnlyChange(w)
		return ok, reason
	case isPlanDocumentPath(w.rel):
		return false, "the milestone Status: line is the seal-gate coordinator's closure act; a review writes its verdict to acceptance/M<n>.yaml and nothing else."
	}
	return false, ""
}

// isAcceptanceEvidencePath reports whether rel is the exact evidence shape.
func isAcceptanceEvidencePath(rel string) bool {
	return acceptanceEvidenceRe.MatchString(rel)
}

// underAcceptanceDir reports whether rel is anywhere inside acceptance/.
func underAcceptanceDir(rel string) bool {
	return strings.HasPrefix(rel, "acceptance/")
}

// isPlanDocumentPath reports whether rel is a plan-bearing BUILD document:
// the root BUILD.md, or a shard directly under BUILD/. README.md and index.md
// there are human navigation, not plan shards, exactly as machinery reads
// them.
func isPlanDocumentPath(rel string) bool {
	if rel == "BUILD.md" {
		return true
	}
	shard, ok := strings.CutPrefix(rel, "BUILD/")
	if !ok || strings.Contains(shard, "/") || !strings.HasSuffix(strings.ToLower(shard), ".md") {
		return false
	}
	switch strings.ToLower(shard) {
	case "readme.md", "index.md":
		return false
	}
	return true
}

// statusOnlyChange verifies that a write to a plan document changes milestone
// status lines and NOTHING else. An Edit is judged on its own old_string and
// new_string; a Write is judged against the file already on disk. A write
// that touches no status line is not a closure act and is refused, so the
// carve-out cannot be used as a general plan-editing permit.
func statusOnlyChange(w designWriteIntent) (bool, string) {
	var before, after string
	switch w.tool {
	case "Edit":
		before, after = w.oldString, w.newString
	case "Write":
		data, err := os.ReadFile(w.abs)
		if err != nil {
			return false, "a plan document is edited in place, never created or replaced wholesale through this exception; the closure act adds or flips one milestone's Status: line."
		}
		before, after = string(data), w.content
	default:
		return false, "the milestone Status: line is edited with the Edit tool, so the guard can see that the change is confined to that line; a shell command carries no reviewable before and after."
	}
	if !hasStatusLine(before) && !hasStatusLine(after) {
		return false, "this write touches no milestone Status: line, and the plan is otherwise read-only while the substrate applies."
	}
	if stripStatusLines(before) != stripStatusLines(after) {
		return false, "this write changes more than a milestone Status: line. The closure act adds or flips exactly that line; a DoD, a milestone title, or any other plan text is a design revision, which runs through machinery's revision protocol and never through a delivery session."
	}
	return true, ""
}

// hasStatusLine reports whether text carries at least one pure status line.
func hasStatusLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if milestoneStatusLineRe.MatchString(line) {
			return true
		}
	}
	return false
}

// stripStatusLines returns text with every pure status line removed, which is
// the part of a closure act that must be identical before and after.
func stripStatusLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if milestoneStatusLineRe.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// acceptanceBlockMsg explains a refused near miss: the general read-only rule
// plus the specific line the attempt fell foul of.
func acceptanceBlockMsg(dir, who, specific string) string {
	return "BLOCKED: the machinery design tree (" + strings.Trim(dir, "/") + "/) is read-only for " + who +
		" while design.machinery applies, and this write is not the milestone acceptance exception.\n" +
		"  - " + specific + "\n" +
		"  - The exception is exactly two writes: the reviewing Anchor writes " + strings.Trim(dir, "/") +
		"/acceptance/M<n>.yaml, and the seal-gate coordinator edits only a milestone's Status: line in the build plan.\n" +
		"  - Everything else in the design tree, sources and generated artifacts alike, changes through machinery's revision\n" +
		"    protocol (the design owner, outside the loop), then `pvg story sync-oracle --base <ref>`."
}
