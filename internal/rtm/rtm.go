// Package rtm implements Requirement Traceability Matrix verification.
// It reads D&F documents, extracts tagged requirements and section headings,
// then checks that each has a covering story in the nd backlog. On a
// machinery-first project it also turns every oracle stable id (machine
// transition rows and formal decision rows alike) into a requirement, with
// exact whole-token coverage, scoped per milestone, per oracle, or per epic
// when asked.
package rtm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/design"
	"github.com/paivot-ai/pvg/internal/settings"
)

// Requirement represents a tagged item or section heading from a D&F document.
type Requirement struct {
	Tag       string `json:"tag"`                  // e.g. "[NEW]", "[EXPANDED]", "[ORACLE]"
	Text      string `json:"text"`                 // the requirement text (the stable id for [ORACLE])
	Source    string `json:"source"`               // which D&F doc or oracle it came from
	Line      int    `json:"line"`                 // line number in the source (0 for oracles)
	Covered   bool   `json:"covered"`              // whether a story references it
	CoveredBy string `json:"covered_by,omitempty"` // story ID that covers it
	// CoveredByClosed marks coverage attributed to a closed story: the
	// requirement was delivered, not merely planned. Closed stories count as
	// coverage so an incremental RTM after a milestone closes stays green.
	CoveredByClosed bool `json:"covered_by_closed,omitempty"`
}

// Scope records the filters a scoped run applied, so a report says what it
// measured. Nil when the run was unscoped.
type Scope struct {
	Milestone      string   `json:"milestone,omitempty"`       // "M1"
	MilestoneTitle string   `json:"milestone_title,omitempty"` // "Trust substrate (walking skeleton)"
	Oracles        []string `json:"oracles,omitempty"`         // oracle files selected (design-relative)
	Epic           string   `json:"epic,omitempty"`            // story set restricted to this epic's subtree
}

// OracleCoverage is the per-oracle breakdown of [ORACLE] requirements.
type OracleCoverage struct {
	Oracle    string   `json:"oracle"` // design-relative path
	Total     int      `json:"total"`
	Covered   int      `json:"covered"`
	Uncovered []string `json:"uncovered,omitempty"` // stable ids with no covering story
}

// RTMResult holds the full traceability check output.
type RTMResult struct {
	Requirements  []Requirement    `json:"requirements"`
	Total         int              `json:"total"`
	Covered       int              `json:"covered"`
	Uncovered     int              `json:"uncovered"`
	Stories       int              `json:"stories_checked"`
	StoriesClosed int              `json:"stories_closed"`
	Passed        bool             `json:"passed"`
	Scope         *Scope           `json:"scope,omitempty"`
	ByOracle      []OracleCoverage `json:"by_oracle,omitempty"`
}

// Options narrows a coverage run. Milestone and Oracles filter the [ORACLE]
// requirement set (the D&F-document requirements are dropped when either is
// set, because a scoped run measures design coverage); Epic filters the
// covering story set to one epic's subtree. The two axes are orthogonal:
// "which ids must be covered" versus "which stories may cover them".
type Options struct {
	Milestone string   // BUILD.md milestone marker id ("M1"): ids the block names
	Oracles   []string // oracle selectors (name, stem, base name, or design-relative path)
	Epic      string   // nd epic id: only stories in its subtree (parent chain) cover
}

// tagPattern matches lines with [NEW], [EXPANDED], [CRITICAL], [REQUIRED], [CHANGED].
var tagPattern = regexp.MustCompile(`\[(NEW|EXPANDED|CRITICAL|REQUIRED|CHANGED)\]`)

// CheckCoverage reads D&F docs from projectRoot, extracts requirements,
// and checks them against stories in the nd vault (unscoped).
func CheckCoverage(projectRoot, vaultDir string) (RTMResult, error) {
	return CheckCoverageWithOptions(projectRoot, vaultDir, Options{})
}

// CheckCoverageWithOptions is CheckCoverage with milestone, oracle, and epic
// scoping.
func CheckCoverageWithOptions(projectRoot, vaultDir string, opts Options) (RTMResult, error) {
	var result RTMResult
	scoped := opts.Milestone != "" || len(opts.Oracles) > 0

	// Extract requirements from D&F docs (unscoped runs only: a milestone or
	// oracle scope measures design coverage, where the narrative documents
	// have no per-milestone shape to filter on).
	if !scoped {
		dfDocs := []string{"BUSINESS.md", "DESIGN.md", "ARCHITECTURE.md"}
		for _, doc := range dfDocs {
			path := filepath.Join(projectRoot, doc)
			reqs, err := extractRequirements(path, doc)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return result, fmt.Errorf("extract from %s: %w", doc, err)
			}
			result.Requirements = append(result.Requirements, reqs...)
		}
	}

	// The design substrate contributes deterministic requirements: every
	// oracle stable id is a transition or decision row the backlog must
	// cover. Coverage for these is exact whole-token matching, not the
	// keyword heuristic. The substrate is strictly user-opt-in via
	// design.machinery (default off); machinery artifacts alone never pull
	// oracle ids into the RTM.
	sett := settings.LoadFile(filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml"))
	cfg, applies, reason := design.Applies(projectRoot, design.MachinerySetting(sett))
	if scoped && !applies {
		return result, fmt.Errorf("--milestone and --oracle scope the design oracles, but the design substrate does not apply (%s)", reason)
	}
	var oracles []design.Oracle
	if applies {
		var derr error
		oracles, derr = design.LoadOracles(projectRoot, cfg)
		if derr != nil {
			return result, fmt.Errorf("read design oracles: %w", derr)
		}
	}

	inScope, scope, err := selectOracleIDs(projectRoot, cfg, oracles, opts)
	if err != nil {
		return result, err
	}
	if opts.Epic != "" {
		if scope == nil {
			scope = &Scope{}
		}
		scope.Epic = opts.Epic
	}
	result.Scope = scope

	byOracle := map[string]*OracleCoverage{}
	var oracleOrder []string
	for _, o := range oracles {
		for _, id := range o.IDs {
			if inScope != nil && !inScope[id] {
				continue
			}
			result.Requirements = append(result.Requirements, Requirement{
				Tag:    "[ORACLE]",
				Text:   id,
				Source: filepath.ToSlash(filepath.Join(cfg.Dir, o.Rel)),
			})
			cov, ok := byOracle[o.Rel]
			if !ok {
				cov = &OracleCoverage{Oracle: o.Rel}
				byOracle[o.Rel] = cov
				oracleOrder = append(oracleOrder, o.Rel)
			}
			cov.Total++
		}
	}

	result.Total = len(result.Requirements)

	// Load every story (closed ones included: a delivered requirement is
	// covered, and an incremental RTM after a milestone closes must not
	// report the closed milestone's ids as uncovered).
	stories, err := loadStories(vaultDir)
	if err != nil {
		return result, fmt.Errorf("load stories: %w", err)
	}
	if opts.Epic != "" {
		stories = subtree(stories, opts.Epic)
		if len(stories) == 0 {
			return result, fmt.Errorf("epic %s has no issues in its subtree (check the id and the parent links)", opts.Epic)
		}
	}
	result.Stories = len(stories)
	for _, s := range stories {
		if s.closed() {
			result.StoriesClosed++
		}
	}

	if result.Total == 0 {
		result.Passed = true
		return result, nil
	}

	// Check coverage: for each requirement, search for it in story bodies.
	// A non-closed covering story is attributed first (the live owner),
	// then a closed one (delivered).
	for i := range result.Requirements {
		req := &result.Requirements[i]
		var hit *storyRecord
		if req.Tag == "[ORACLE]" {
			hit = findStoryWithToken(req.Text, stories)
		} else {
			hit = findCoveringStory(req.Text, stories)
		}
		if hit != nil {
			req.Covered = true
			req.CoveredBy = hit.ID
			req.CoveredByClosed = hit.closed()
			result.Covered++
		}
		if req.Tag == "[ORACLE]" {
			rel := strings.TrimPrefix(req.Source, filepath.ToSlash(cfg.Dir)+"/")
			if cov, ok := byOracle[rel]; ok {
				if req.Covered {
					cov.Covered++
				} else {
					cov.Uncovered = append(cov.Uncovered, req.Text)
				}
			}
		}
	}

	for _, rel := range oracleOrder {
		result.ByOracle = append(result.ByOracle, *byOracle[rel])
	}

	result.Uncovered = result.Total - result.Covered
	result.Passed = result.Uncovered == 0
	return result, nil
}

// selectOracleIDs resolves the [ORACLE] id filter from the options. A nil
// set means "every id"; an empty non-nil set means "nothing" (a milestone
// that names no oracle, which the report then shows as zero requirements).
func selectOracleIDs(projectRoot string, cfg design.Config, oracles []design.Oracle, opts Options) (map[string]bool, *Scope, error) {
	if opts.Milestone == "" && len(opts.Oracles) == 0 {
		return nil, nil, nil
	}
	scope := &Scope{}
	set := map[string]bool{}
	selected := map[string]bool{}

	if opts.Milestone != "" {
		milestones, err := design.Milestones(projectRoot, cfg)
		if err != nil {
			return nil, nil, err
		}
		m, err := design.FindMilestone(milestones, opts.Milestone)
		if err != nil {
			return nil, nil, err
		}
		scope.Milestone = m.ID
		scope.MilestoneTitle = m.Title
		ids, named := design.MilestoneOracleScope(m, oracles)
		for _, id := range ids {
			set[id] = true
		}
		for _, rel := range named {
			selected[rel] = true
		}
	}

	for _, sel := range opts.Oracles {
		matched := false
		for _, o := range oracles {
			if o.MatchesSelector(sel) {
				matched = true
				selected[o.Rel] = true
				for _, id := range o.IDs {
					set[id] = true
				}
			}
		}
		if !matched {
			names := make([]string, 0, len(oracles))
			for _, o := range oracles {
				names = append(names, o.Rel)
			}
			return nil, nil, fmt.Errorf("--oracle %q matches no committed oracle (have: %s)", sel, strings.Join(names, ", "))
		}
	}

	for rel := range selected {
		scope.Oracles = append(scope.Oracles, rel)
	}
	sort.Strings(scope.Oracles)
	return set, scope, nil
}

// extractRequirements parses a D&F document for tagged lines and section headings.
func extractRequirements(path, sourceName string) ([]Requirement, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var reqs []Requirement
	scanner := bufio.NewScanner(f)
	lineNum := 0
	inFrontmatter := false
	frontmatterDone := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip YAML frontmatter if present
		if !frontmatterDone {
			if line == "---" {
				if inFrontmatter {
					frontmatterDone = true
				} else {
					inFrontmatter = true
				}
				continue
			}
			if inFrontmatter {
				continue
			}
			frontmatterDone = true // no frontmatter
		}

		trimmed := strings.TrimSpace(line)

		// Check for tagged items: [NEW], [EXPANDED], etc.
		if locs := tagPattern.FindStringIndex(trimmed); locs != nil {
			tag := trimmed[locs[0]:locs[1]]
			text := strings.TrimSpace(trimmed)
			// Strip leading markdown list markers
			text = strings.TrimLeft(text, "- *")
			text = strings.TrimSpace(text)
			if text != "" {
				reqs = append(reqs, Requirement{
					Tag:    tag,
					Text:   text,
					Source: sourceName,
					Line:   lineNum,
				})
			}
		}
	}

	return reqs, scanner.Err()
}

// storyRecord is one nd issue as the RTM sees it.
type storyRecord struct {
	ID     string
	Parent string
	Status string
	Body   string
}

func (s storyRecord) closed() bool { return strings.EqualFold(s.Status, "closed") }

// LoadStoryBodies reads all non-closed issue files and returns a map of
// ID -> body text plus the count. Shared with story sync-oracle, which maps
// a design revision onto the stories still open to rework.
func LoadStoryBodies(vaultDir string) (map[string]string, int, error) {
	stories, err := loadStories(vaultDir)
	if err != nil {
		return nil, 0, err
	}
	bodies := make(map[string]string)
	for _, s := range stories {
		if s.closed() {
			continue
		}
		bodies[s.ID] = s.Body
	}
	return bodies, len(bodies), nil
}

// loadStories reads every issue file (any status), sorted by id.
func loadStories(vaultDir string) ([]storyRecord, error) {
	issuesDir := filepath.Join(vaultDir, "issues")
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var stories []storyRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(issuesDir, entry.Name())
		rec, err := parseIssue(path)
		if err != nil {
			continue
		}
		if rec.ID == "" {
			rec.ID = strings.TrimSuffix(entry.Name(), ".md")
		}
		stories = append(stories, rec)
	}
	sort.Slice(stories, func(i, j int) bool { return stories[i].ID < stories[j].ID })
	return stories, nil
}

// subtree returns the epic itself plus every issue whose parent chain
// reaches it (nested epics included).
func subtree(stories []storyRecord, epicID string) []storyRecord {
	parent := make(map[string]string, len(stories))
	for _, s := range stories {
		parent[s.ID] = s.Parent
	}
	inTree := func(id string) bool {
		seen := map[string]bool{}
		for cur := id; cur != "" && !seen[cur]; cur = parent[cur] {
			if cur == epicID {
				return true
			}
			seen[cur] = true
		}
		return false
	}
	var out []storyRecord
	for _, s := range stories {
		if inTree(s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// parseIssue reads an nd issue file into a storyRecord.
func parseIssue(path string) (storyRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return storyRecord{}, err
	}

	var rec storyRecord
	content := string(data)
	inFrontmatter := false
	frontmatterDone := false
	var bodyLines []string

	for _, line := range strings.Split(content, "\n") {
		if !frontmatterDone {
			if line == "---" {
				if inFrontmatter {
					frontmatterDone = true
				} else {
					inFrontmatter = true
				}
				continue
			}
			if inFrontmatter {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
					switch key {
					case "id":
						rec.ID = value
					case "status":
						rec.Status = value
					case "parent":
						rec.Parent = value
					}
				}
				continue
			}
			frontmatterDone = true
		}
		bodyLines = append(bodyLines, line)
	}

	rec.Body = strings.Join(bodyLines, "\n")
	return rec, nil
}

// findStoryWithToken returns the first story whose body carries the exact
// token (oracle stable ids are opaque; keyword matching would be
// meaningless): non-closed stories first, then closed ones, each in sorted
// id order for deterministic attribution.
func findStoryWithToken(token string, stories []storyRecord) *storyRecord {
	var closedHit *storyRecord
	for i := range stories {
		s := &stories[i]
		if !design.TokenIn(token, s.Body) {
			continue
		}
		if !s.closed() {
			return s
		}
		if closedHit == nil {
			closedHit = s
		}
	}
	return closedHit
}

// findCoveringStory searches story bodies for a requirement's key phrases
// and returns the first story (non-closed first) that contains at least 60
// percent of them.
func findCoveringStory(reqText string, stories []storyRecord) *storyRecord {
	keywords := extractKeywords(reqText)
	if len(keywords) == 0 {
		return nil
	}
	threshold := max(1, len(keywords)*60/100)

	var closedHit *storyRecord
	for i := range stories {
		s := &stories[i]
		bodyLower := strings.ToLower(s.Body)
		hits := 0
		for _, kw := range keywords {
			if strings.Contains(bodyLower, kw) {
				hits++
			}
		}
		if hits < threshold {
			continue
		}
		if !s.closed() {
			return s
		}
		if closedHit == nil {
			closedHit = s
		}
	}
	return closedHit
}

// extractKeywords pulls significant words from a requirement line.
func extractKeywords(text string) []string {
	// Remove tags like [NEW], [EXPANDED]
	cleaned := tagPattern.ReplaceAllString(text, "")
	// Remove markdown formatting
	cleaned = strings.NewReplacer(
		"**", "", "*", "", "`", "", "#", "", "-", " ",
	).Replace(cleaned)

	words := strings.Fields(strings.ToLower(cleaned))
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"at": true, "by": true, "with": true, "from": true, "as": true,
		"that": true, "this": true, "it": true, "not": true, "but": true,
		"all": true, "each": true, "every": true, "must": true, "should": true,
		"will": true, "can": true, "new": true, "expanded": true,
		"critical": true, "required": true, "changed": true,
	}

	var keywords []string
	for _, w := range words {
		if len(w) >= 3 && !stopWords[w] {
			keywords = append(keywords, w)
		}
	}
	return keywords
}

// FormatText returns a human-readable RTM report.
func FormatText(r RTMResult) string {
	var b strings.Builder
	if r.Scope != nil {
		var parts []string
		if r.Scope.Milestone != "" {
			title := r.Scope.Milestone
			if r.Scope.MilestoneTitle != "" {
				title += " (" + r.Scope.MilestoneTitle + ")"
			}
			parts = append(parts, "milestone "+title)
		}
		if len(r.Scope.Oracles) > 0 {
			parts = append(parts, "oracles "+strings.Join(r.Scope.Oracles, ", "))
		}
		if r.Scope.Epic != "" {
			parts = append(parts, "stories in epic "+r.Scope.Epic)
		}
		fmt.Fprintf(&b, "[RTM] scope: %s\n", strings.Join(parts, "; "))
	}
	fmt.Fprintf(&b, "[RTM] %d requirements extracted, %d covered, %d uncovered (%d stories checked, %d closed)\n",
		r.Total, r.Covered, r.Uncovered, r.Stories, r.StoriesClosed)
	for _, oc := range r.ByOracle {
		fmt.Fprintf(&b, "  %s: %d/%d covered\n", oc.Oracle, oc.Covered, oc.Total)
	}
	closedCovered := 0
	for _, req := range r.Requirements {
		if req.CoveredByClosed {
			closedCovered++
		}
	}
	if closedCovered > 0 {
		fmt.Fprintf(&b, "  %d requirement(s) covered by closed (delivered) stories\n", closedCovered)
	}

	if r.Passed {
		b.WriteString("[RTM] PASSED: all tagged requirements have covering stories\n")
		return b.String()
	}

	b.WriteString("[RTM] FAILED: uncovered requirements:\n")
	for _, req := range r.Requirements {
		if !req.Covered {
			fmt.Fprintf(&b, "  %s:%d %s %s\n", req.Source, req.Line, req.Tag, req.Text)
		}
	}
	return b.String()
}

// FormatJSON returns the RTM result as indented JSON.
func FormatJSON(r RTMResult) (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
