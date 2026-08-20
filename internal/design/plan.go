package design

// The build plan (BUILD.md section 9, machinery's Gb-plan shape) names one
// milestone per bold `**M<n> - title**` marker. Paivot derives one milestone
// epic per marker and scopes oracle coverage per milestone, so pvg reads the
// marker blocks deterministically instead of asking an agent to paraphrase
// them.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PlanFile is the build plan's file name inside the design dir.
const PlanFile = "BUILD.md"

// milestoneMarkerRe matches the bold milestone marker Gb-plan requires:
// "**M1 - Trust substrate (walking skeleton)**", optionally list-numbered.
// Only the marker's leading "M<n>" is captured; the title is free prose.
var milestoneMarkerRe = regexp.MustCompile(`^\s*(?:\d+\.\s*)?\*\*\s*(M\d+)\b`)

// Milestone is one `**M<n>**` block of the build plan: the marker id, the
// title text inside the bold span, and the block's full prose (from the
// marker line up to the next marker or the next section heading).
type Milestone struct {
	ID    string // "M1"
	Title string // "Trust substrate (walking skeleton)"
	Line  int    // 1-based line of the marker
	Text  string // the whole block, marker line included
}

// Milestones parses the build plan at <design>/BUILD.md and returns its
// milestone blocks in order. A design without a build plan returns an error
// naming the missing file.
func Milestones(projectRoot string, cfg Config) ([]Milestone, error) {
	path := filepath.Join(projectRoot, filepath.FromSlash(cfg.Dir), PlanFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read build plan %s: %w", filepath.ToSlash(filepath.Join(cfg.Dir, PlanFile)), err)
	}
	return ParseMilestones(string(data)), nil
}

// ParseMilestones extracts the milestone blocks from build-plan text.
func ParseMilestones(content string) []Milestone {
	lines := strings.Split(content, "\n")
	var (
		out     []Milestone
		current *Milestone
		body    []string
	)
	flush := func() {
		if current != nil {
			current.Text = strings.Join(body, "\n")
			out = append(out, *current)
		}
		current = nil
		body = nil
	}
	for i, line := range lines {
		if m := milestoneMarkerRe.FindStringSubmatch(line); m != nil {
			flush()
			current = &Milestone{ID: m[1], Line: i + 1, Title: markerTitle(line, m[1])}
			body = []string{line}
			continue
		}
		if current != nil && strings.HasPrefix(strings.TrimSpace(line), "## ") {
			flush()
			continue
		}
		if current != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// markerTitle returns the prose inside the bold span after the "M<n>" id
// and its separator ("**M1 - Trust substrate**" -> "Trust substrate").
func markerTitle(line, id string) string {
	start := strings.Index(line, "**")
	if start < 0 {
		return ""
	}
	rest := line[start+2:]
	end := strings.Index(rest, "**")
	if end < 0 {
		return ""
	}
	span := strings.TrimSpace(rest[:end])
	span = strings.TrimSpace(strings.TrimPrefix(span, id))
	span = strings.TrimLeft(span, "-:. ")
	return strings.TrimSpace(span)
}

// FindMilestone returns the milestone whose id matches name
// (case-insensitive, "m1" == "M1"), or an error listing the ids found.
func FindMilestone(milestones []Milestone, name string) (Milestone, error) {
	want := strings.ToUpper(strings.TrimSpace(name))
	ids := make([]string, 0, len(milestones))
	for _, m := range milestones {
		if strings.ToUpper(m.ID) == want {
			return m, nil
		}
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return Milestone{}, fmt.Errorf("milestone %q not found: the build plan carries no `**M<n> - title**` markers", name)
	}
	return Milestone{}, fmt.Errorf("milestone %q not found in the build plan (found: %s)", name, strings.Join(ids, ", "))
}

// MilestoneOracleScope resolves the oracle ids a milestone block puts in
// scope, deterministically and over-inclusively:
//
//  1. every stable id the block cites as a whole token, and
//  2. every id of every oracle the block names (Oracle.NamedIn): a
//     transition oracle by machine name, a formal oracle by an
//     oracle-qualified mention.
//
// Over-inclusion is the conservative direction: a machine whose rows are
// split across layers (the design may defer some rows to a later milestone)
// surfaces those rows as uncovered in the earlier milestone's report, where
// the reviewer adjudicates them against the shard's row list, instead of
// silently dropping them from every scope. The returned oracle list names
// the oracles selected by rule 2 (sorted); ids is the union, sorted.
func MilestoneOracleScope(m Milestone, oracles []Oracle) (ids []string, named []string) {
	set := map[string]bool{}
	for _, o := range oracles {
		if o.NamedIn(m.Text) {
			named = append(named, o.Rel)
			for _, id := range o.IDs {
				set[id] = true
			}
			continue
		}
		for _, id := range o.IDs {
			if TokenIn(id, m.Text) {
				set[id] = true
			}
		}
	}
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sort.Strings(named)
	return ids, named
}
