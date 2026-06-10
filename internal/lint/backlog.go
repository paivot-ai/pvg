// Backlog structure checks for `pvg lint --backlog`.
//
// These extend the PRODUCES artifact-collision check with the full set of
// mechanical backlog-quality gates referenced by the Sr PM and Anchor agent
// prompts. Each check is a small pure function over an in-memory backlog
// loaded once from the nd vault's issues directory (read-only, no nd
// subprocesses), so a 100-issue backlog lints in well under a second.
package lint

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/paivot-ai/pvg/internal/settings"
)

// Severity levels for backlog findings. Only error-severity findings fail
// the lint gate; review findings are judgment flags for the Sr PM.
const (
	SeverityError  = "error"
	SeverityReview = "review"
)

// Finding is a single backlog lint result. The JSON shape is part of the
// agent-prompt contract: {"check","severity","issue_id","message"}.
type Finding struct {
	Check    string `json:"check"`
	Severity string `json:"severity"`
	IssueID  string `json:"issue_id"`
	Message  string `json:"message"`
}

// BacklogOptions configures a backlog lint run.
type BacklogOptions struct {
	VaultDir    string // nd vault directory (contains issues/ and .nd.yaml)
	ProjectRoot string // git root, for settings and paths-exist disk checks
	EpicID      string // optional: scope story-level checks to this epic's children
}

// BacklogResult holds all findings from a backlog lint run.
type BacklogResult struct {
	Findings []Finding `json:"findings"`
	Issues   int       `json:"issues_scanned"`
	Errors   int       `json:"errors"`
	Reviews  int       `json:"reviews"`
}

// ConsumesEntry is one "- " entry inside a CONSUMES: block, plus whether it
// carries an indented signature line (spec:/fields:/endpoint:/event:/schema:/source:).
type ConsumesEntry struct {
	Raw          string
	Ref          string // upstream issue ID; "" for placeholder entries like "(existing)"
	HasSignature bool
}

// BacklogIssue is the fully-parsed view of one nd issue file.
type BacklogIssue struct {
	ID           string
	Title        string
	Status       string
	Type         string
	Parent       string
	Labels       []string
	BlockedBy    []string
	WasBlockedBy []string
	Body         string
	Produces     []string
	Consumes     []ConsumesEntry
}

// Backlog is the loaded issue set plus the detected project ID prefix.
type Backlog struct {
	issues  map[string]*BacklogIssue
	ordered []*BacklogIssue // sorted by ID for deterministic output
	prefix  string
}

// checkRank fixes the grouping order of checks in output.
var checkRank = map[string]int{
	"produces-collision":   0,
	"walking-skeleton":     1,
	"capstone":             2,
	"mandatory-skills":     3,
	"consumes-signature":   4,
	"consumes-produces":    5,
	"stale-refs":           6,
	"external-integration": 7,
	"atomicity":            8,
	"vertical-slice":       9,
	"dep-cycles":           10,
	"release-gate":         11,
	"paths-exist":          12,
}

// scope restricts story-level and epic-level checks when --epic is given.
// nil sets mean "everything".
type scope struct {
	epicIDs  map[string]bool
	storyIDs map[string]bool
}

func (s scope) epicInScope(id string) bool {
	return s.epicIDs == nil || s.epicIDs[id]
}

func (s scope) storyInScope(id string) bool {
	return s.storyIDs == nil || s.storyIDs[id]
}

// CheckBacklog runs all backlog structure checks plus the artifact-collision
// check and returns the aggregated findings.
func CheckBacklog(opts BacklogOptions) (BacklogResult, error) {
	b, err := loadBacklog(opts.VaultDir)
	if err != nil {
		return BacklogResult{}, err
	}

	sc, err := buildScope(b, opts.EpicID)
	if err != nil {
		return BacklogResult{}, err
	}

	settingsPath := filepath.Join(opts.ProjectRoot, ".vault", "knowledge", ".settings.yaml")
	sett := settings.LoadFile(settingsPath)

	collisions, err := CheckArtifactCollisions(opts.VaultDir)
	if err != nil {
		return BacklogResult{}, err
	}

	var findings []Finding
	findings = append(findings, collisionFindings(collisions, sc)...)
	findings = append(findings, checkWalkingSkeleton(b, sc, qualityGates(sett))...)
	findings = append(findings, checkCapstone(b, sc)...)
	findings = append(findings, checkMandatorySkills(b, sc)...)
	findings = append(findings, checkConsumesSignature(b, sc)...)
	findings = append(findings, checkConsumesProduces(b, sc)...)
	findings = append(findings, checkStaleRefs(b, sc)...)
	findings = append(findings, checkExternalIntegration(b, sc)...)
	findings = append(findings, checkAtomicity(b, sc)...)
	findings = append(findings, checkVerticalSlice(b, sc)...)
	// Graph-level checks stay global even under --epic: a cycle or a
	// mis-pointed release gate breaks dispatch regardless of which epic
	// is being fixed.
	findings = append(findings, checkDepCycles(b)...)
	findings = append(findings, checkReleaseGate(b)...)

	if brownfieldEnabled(opts.ProjectRoot, sett) {
		findings = append(findings, checkPathsExist(b, sc, opts.ProjectRoot)...)
	}

	sortFindings(findings)

	result := BacklogResult{Findings: findings, Issues: len(b.ordered)}
	for _, f := range findings {
		switch f.Severity {
		case SeverityError:
			result.Errors++
		case SeverityReview:
			result.Reviews++
		}
	}
	return result, nil
}

// FormatBacklogText renders findings grouped by check ID with a summary line.
func FormatBacklogText(r BacklogResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[LINT] backlog: scanned %d issues\n", r.Issues)
	current := ""
	for _, f := range r.Findings {
		if f.Check != current {
			current = f.Check
			fmt.Fprintf(&b, "\n%s:\n", current)
		}
		fmt.Fprintf(&b, "  [%s] %s: %s\n", f.Severity, f.IssueID, f.Message)
	}
	if len(r.Findings) > 0 {
		b.WriteString("\n")
	}
	verdict := "PASSED"
	if r.Errors > 0 {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "[LINT] %s: %d error(s), %d review finding(s)\n", verdict, r.Errors, r.Reviews)
	return b.String()
}

// FormatBacklogJSON renders the findings as a JSON array (never null).
func FormatBacklogJSON(r BacklogResult) (string, error) {
	findings := r.Findings
	if findings == nil {
		findings = []Finding{}
	}
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// --- loading -----------------------------------------------------------

// loadBacklog reads every issues/*.md file in the vault into memory.
func loadBacklog(vaultDir string) (*Backlog, error) {
	b := &Backlog{issues: make(map[string]*BacklogIssue)}

	issuesDir := filepath.Join(vaultDir, "issues")
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		if os.IsNotExist(err) {
			b.prefix = ndConfigPrefix(vaultDir)
			return b, nil
		}
		return nil, fmt.Errorf("read issues dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		issue, err := parseBacklogIssueFile(filepath.Join(issuesDir, entry.Name()))
		if err != nil {
			continue // skip unparseable files, consistent with CheckArtifactCollisions
		}
		b.issues[issue.ID] = issue
	}

	b.ordered = make([]*BacklogIssue, 0, len(b.issues))
	for _, issue := range b.issues {
		b.ordered = append(b.ordered, issue)
	}
	sort.Slice(b.ordered, func(i, j int) bool { return b.ordered[i].ID < b.ordered[j].ID })

	b.prefix = detectPrefix(vaultDir, b)
	return b, nil
}

// parseBacklogIssueFile parses one nd issue markdown file (frontmatter + body).
func parseBacklogIssueFile(path string) (*BacklogIssue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	issue := &BacklogIssue{}

	bodyStart := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				bodyStart = i + 1
				break
			}
			parseBacklogFrontmatterLine(issue, lines[i])
		}
	}

	issue.Body = strings.Join(lines[bodyStart:], "\n")
	issue.Produces, issue.Consumes = parseBodyBlocks(issue.Body)

	if issue.ID == "" {
		issue.ID = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return issue, nil
}

func parseBacklogFrontmatterLine(issue *BacklogIssue, line string) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])

	switch key {
	case "id":
		issue.ID = unquoteYAML(value)
	case "title":
		issue.Title = unquoteYAML(value)
	case "status":
		issue.Status = unquoteYAML(value)
	case "type":
		issue.Type = unquoteYAML(value)
	case "parent":
		issue.Parent = unquoteYAML(value)
	case "labels":
		issue.Labels = parseInlineList(value)
	case "blocked_by":
		issue.BlockedBy = parseInlineList(value)
	case "was_blocked_by":
		issue.WasBlockedBy = parseInlineList(value)
	}
}

// parseInlineList parses an inline YAML list ("[a, b]") and strips item quotes.
func parseInlineList(value string) []string {
	items := parseYAMLList(value)
	for i := range items {
		items[i] = strings.Trim(items[i], `"'`)
	}
	return items
}

// unquoteYAML strips surrounding quotes from a scalar YAML value, handling
// Go-style escapes produced by nd's %q title serialization.
func unquoteYAML(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, `"'`)
}

// consumesSignatureKeys are the contract-line prefixes that satisfy the
// consumes-signature check.
var consumesSignatureKeys = []string{"spec:", "fields:", "endpoint:", "event:", "schema:", "source:"}

// parseBodyBlocks extracts PRODUCES entries and CONSUMES entries (with
// signature-line tracking) from an issue body.
func parseBodyBlocks(body string) ([]string, []ConsumesEntry) {
	var produces []string
	var consumes []ConsumesEntry

	const (
		modeNone = iota
		modeProduces
		modeConsumes
	)
	mode := modeNone

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		switch {
		case strings.HasPrefix(upper, "PRODUCES:"):
			mode = modeProduces
			continue
		case strings.HasPrefix(upper, "CONSUMES:"):
			mode = modeConsumes
			continue
		}

		if mode == modeNone {
			continue
		}

		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')

		switch {
		case trimmed == "":
			mode = modeNone
		case strings.HasPrefix(trimmed, "-"):
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if entry == "" {
				continue
			}
			if mode == modeProduces {
				produces = append(produces, entry)
			} else {
				consumes = append(consumes, ConsumesEntry{Raw: entry, Ref: extractConsumeRef(entry)})
			}
		case indented:
			// Continuation line of the previous entry. In CONSUMES blocks,
			// an indented signature line satisfies consumes-signature.
			if mode == modeConsumes && len(consumes) > 0 && isSignatureLine(trimmed) {
				consumes[len(consumes)-1].HasSignature = true
			}
		default:
			mode = modeNone
		}
	}

	return produces, consumes
}

func isSignatureLine(trimmed string) bool {
	lower := strings.ToLower(trimmed)
	for _, key := range consumesSignatureKeys {
		if strings.HasPrefix(lower, key) {
			return true
		}
	}
	return false
}

// ndConfigPrefix reads the prefix key from the vault's .nd.yaml.
func ndConfigPrefix(vaultDir string) string {
	data, err := os.ReadFile(filepath.Join(vaultDir, ".nd.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "prefix" {
			return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return ""
}

// detectPrefix returns the project issue-ID prefix: the .nd.yaml prefix when
// present, otherwise the most common prefix among existing issue IDs.
func detectPrefix(vaultDir string, b *Backlog) string {
	if prefix := ndConfigPrefix(vaultDir); prefix != "" {
		return prefix
	}

	counts := make(map[string]int)
	for id := range b.issues {
		if i := strings.Index(id, "-"); i > 0 {
			counts[id[:i]]++
		}
	}
	best, bestCount := "", 0
	for prefix, count := range counts {
		if count > bestCount || (count == bestCount && prefix < best) {
			best, bestCount = prefix, count
		}
	}
	return best
}

func buildScope(b *Backlog, epicID string) (scope, error) {
	if epicID == "" {
		return scope{}, nil
	}
	epic, ok := b.issues[epicID]
	if !ok {
		return scope{}, fmt.Errorf("epic %s not found in backlog", epicID)
	}
	if !isEpic(epic) {
		return scope{}, fmt.Errorf("%s is not an epic (type: %s)", epicID, epic.Type)
	}

	storyIDs := make(map[string]bool)
	for _, child := range childrenOf(b, epicID) {
		storyIDs[child.ID] = true
	}
	return scope{epicIDs: map[string]bool{epicID: true}, storyIDs: storyIDs}, nil
}

// --- shared predicates --------------------------------------------------

func isClosed(i *BacklogIssue) bool { return strings.EqualFold(i.Status, "closed") }
func isEpic(i *BacklogIssue) bool   { return strings.EqualFold(i.Type, "epic") }

// isStory covers the work-item types the Sr PM authors as stories.
// Issues with no type recorded are treated as stories (nd defaults to task).
func isStory(i *BacklogIssue) bool {
	switch strings.ToLower(i.Type) {
	case "task", "feature", "":
		return true
	}
	return false
}

func isStoryOrBug(i *BacklogIssue) bool {
	return isStory(i) || strings.EqualFold(i.Type, "bug")
}

func hasIssueLabel(i *BacklogIssue, label string) bool {
	for _, l := range i.Labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

// blockerSet returns blocked_by plus was_blocked_by: nd archives satisfied
// dependencies into was_blocked_by, and a historical edge still proves the
// ordering existed.
func blockerSet(i *BacklogIssue) map[string]bool {
	set := make(map[string]bool, len(i.BlockedBy)+len(i.WasBlockedBy))
	for _, id := range i.BlockedBy {
		set[id] = true
	}
	for _, id := range i.WasBlockedBy {
		set[id] = true
	}
	return set
}

func childrenOf(b *Backlog, epicID string) []*BacklogIssue {
	var children []*BacklogIssue
	for _, issue := range b.ordered {
		if issue.Parent == epicID {
			children = append(children, issue)
		}
	}
	return children
}

// --- check: produces-collision -------------------------------------------

func collisionFindings(r LintResult, sc scope) []Finding {
	var findings []Finding
	for _, c := range r.Collisions {
		ids := make([]string, 0, len(c.Stories))
		inScope := false
		for _, s := range c.Stories {
			ids = append(ids, s.StoryID)
			if sc.storyInScope(s.StoryID) {
				inScope = true
			}
		}
		if !inScope {
			continue
		}
		sort.Strings(ids)
		findings = append(findings, Finding{
			Check:    "produces-collision",
			Severity: SeverityError,
			IssueID:  ids[0],
			Message: fmt.Sprintf("artifact %q claimed by %s without a dependency chain",
				normalizeArtifact(c.Artifact), strings.Join(ids, ", ")),
		})
	}
	return findings
}

// --- check: walking-skeleton ----------------------------------------------

type qualityGate struct {
	pattern string
	re      *regexp.Regexp
}

// defaultQualityGatePatterns are the generic quality-gate patterns every
// walking skeleton must establish, matched case-insensitively against the
// skeleton story body.
var defaultQualityGatePatterns = []string{
	`@spec`,
	`DLP`,
	`rate.?limit`,
	`audit (log|trail)`,
	`config.?regist`,
	`error.?handling`,
}

// qualityGates returns the default gates plus project-specific patterns from
// the lint.quality_gates settings key (pipe-separated regex patterns).
// Patterns that fail to compile are matched as literals.
func qualityGates(sett map[string]string) []qualityGate {
	patterns := append([]string{}, defaultQualityGatePatterns...)
	for _, p := range strings.Split(sett["lint.quality_gates"], "|") {
		p = strings.TrimSpace(p)
		if p != "" {
			patterns = append(patterns, p)
		}
	}

	gates := make([]qualityGate, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(p))
		}
		gates = append(gates, qualityGate{pattern: p, re: re})
	}
	return gates
}

func checkWalkingSkeleton(b *Backlog, sc scope, gates []qualityGate) []Finding {
	var findings []Finding
	for _, epic := range b.ordered {
		if !isEpic(epic) || !hasIssueLabel(epic, "milestone") || !sc.epicInScope(epic.ID) {
			continue
		}

		var skeletons []*BacklogIssue
		for _, child := range childrenOf(b, epic.ID) {
			if hasIssueLabel(child, "walking-skeleton") {
				skeletons = append(skeletons, child)
			}
		}

		if len(skeletons) == 0 {
			findings = append(findings, Finding{
				Check:    "walking-skeleton",
				Severity: SeverityError,
				IssueID:  epic.ID,
				Message:  "milestone epic has no child story labeled 'walking-skeleton'",
			})
			continue
		}

		for _, skeleton := range skeletons {
			for _, gate := range gates {
				if !gate.re.MatchString(skeleton.Body) {
					findings = append(findings, Finding{
						Check:    "walking-skeleton",
						Severity: SeverityReview,
						IssueID:  skeleton.ID,
						Message:  fmt.Sprintf("walking-skeleton body does not establish quality-gate pattern %q", gate.pattern),
					})
				}
			}
		}
	}
	return findings
}

// --- check: capstone --------------------------------------------------------

func checkCapstone(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, epic := range b.ordered {
		if !isEpic(epic) || isClosed(epic) || !sc.epicInScope(epic.ID) {
			continue
		}

		children := childrenOf(b, epic.ID)
		if len(children) == 0 {
			findings = append(findings, Finding{
				Check:    "capstone",
				Severity: SeverityReview,
				IssueID:  epic.ID,
				Message:  "epic has no child stories (under-decomposed)",
			})
			continue
		}

		var capstones []*BacklogIssue
		for _, child := range children {
			if hasIssueLabel(child, "capstone") {
				capstones = append(capstones, child)
			}
		}

		if len(capstones) != 1 {
			findings = append(findings, Finding{
				Check:    "capstone",
				Severity: SeverityError,
				IssueID:  epic.ID,
				Message:  fmt.Sprintf("epic has %d children labeled 'capstone' (expected exactly 1)", len(capstones)),
			})
			continue
		}

		capstone := capstones[0]
		blockers := blockerSet(capstone)
		for _, sibling := range children {
			if sibling.ID == capstone.ID {
				continue
			}
			if !blockers[sibling.ID] {
				findings = append(findings, Finding{
					Check:    "capstone",
					Severity: SeverityError,
					IssueID:  capstone.ID,
					Message:  fmt.Sprintf("capstone is not blocked_by sibling story %s", sibling.ID),
				})
			}
		}
	}
	return findings
}

// --- check: mandatory-skills -------------------------------------------------

func checkMandatorySkills(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !isStoryOrBug(issue) || !sc.storyInScope(issue.ID) {
			continue
		}
		if !strings.Contains(issue.Body, "MANDATORY SKILLS") {
			findings = append(findings, Finding{
				Check:    "mandatory-skills",
				Severity: SeverityError,
				IssueID:  issue.ID,
				Message:  "story body is missing the MANDATORY SKILLS section",
			})
		}
	}
	return findings
}

// --- check: consumes-signature -------------------------------------------------

func checkConsumesSignature(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !sc.storyInScope(issue.ID) {
			continue
		}
		for _, entry := range issue.Consumes {
			// Placeholder entries like "(existing)" or "(none -- leaf story)"
			// declare the absence of an upstream contract; they need no signature.
			if strings.HasPrefix(entry.Raw, "(") {
				continue
			}
			if !entry.HasSignature {
				findings = append(findings, Finding{
					Check:    "consumes-signature",
					Severity: SeverityError,
					IssueID:  issue.ID,
					Message: fmt.Sprintf("CONSUMES entry %q has no indented signature line (spec:/fields:/endpoint:/event:/schema:/source:)",
						entry.Raw),
				})
			}
		}
	}
	return findings
}

// --- check: consumes-produces ----------------------------------------------------

func checkConsumesProduces(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !sc.storyInScope(issue.ID) {
			continue
		}
		for _, entry := range issue.Consumes {
			if entry.Ref == "" {
				continue
			}
			upstream, ok := b.issues[entry.Ref]
			if !ok {
				findings = append(findings, Finding{
					Check:    "consumes-produces",
					Severity: SeverityError,
					IssueID:  issue.ID,
					Message:  fmt.Sprintf("CONSUMES references %s, which does not resolve to an existing issue", entry.Ref),
				})
				continue
			}
			if len(upstream.Produces) == 0 {
				findings = append(findings, Finding{
					Check:    "consumes-produces",
					Severity: SeverityError,
					IssueID:  issue.ID,
					Message:  fmt.Sprintf("CONSUMES references %s, which has no PRODUCES block", entry.Ref),
				})
			}
		}
	}
	return findings
}

// --- check: stale-refs ------------------------------------------------------------

// placeholderRefRes match explicit placeholder shapes left over from authoring.
var placeholderRefRes = []*regexp.Regexp{
	regexp.MustCompile(`\bSTORY-[A-Z]\b`),
	regexp.MustCompile(`\bEPIC-[A-Z][A-Z-]*\b`),
	regexp.MustCompile(`\bim-[0-9]{2}-[a-z-]+\b`),
}

func checkStaleRefs(b *Backlog, sc scope) []Finding {
	// Conservative project-ID shape: configured prefix, dash, lowercase
	// base36 hash, optional ".N" child suffixes (nd's ID grammar).
	var projectRefRe *regexp.Regexp
	if b.prefix != "" {
		projectRefRe = regexp.MustCompile(`\b` + regexp.QuoteMeta(b.prefix) + `-[a-z0-9]+(?:\.[0-9]+)*\b`)
	}

	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !sc.storyInScope(issue.ID) {
			continue
		}

		flagged := make(map[string]string) // token -> message

		if projectRefRe != nil {
			for _, token := range projectRefRe.FindAllString(issue.Body, -1) {
				if _, ok := b.issues[token]; !ok {
					flagged[token] = fmt.Sprintf("reference %s does not resolve to an existing issue", token)
				}
			}
		}

		for _, re := range placeholderRefRes {
			for _, token := range re.FindAllString(issue.Body, -1) {
				if _, ok := b.issues[token]; ok {
					continue
				}
				flagged[token] = fmt.Sprintf("placeholder issue reference %q left over from authoring", token)
			}
		}

		tokens := make([]string, 0, len(flagged))
		for token := range flagged {
			tokens = append(tokens, token)
		}
		sort.Strings(tokens)
		for _, token := range tokens {
			findings = append(findings, Finding{
				Check:    "stale-refs",
				Severity: SeverityError,
				IssueID:  issue.ID,
				Message:  flagged[token],
			})
		}
	}
	return findings
}

// --- check: external-integration -----------------------------------------------------

var (
	externalTriggerRe  = regexp.MustCompile(`(?i)oauth|stripe|paypal|twilio|sendgrid|webhook|third.?party|external (service|api)`)
	realEndpointACRe   = regexp.MustCompile(`(?i)real (endpoint|service|api).*verif|manual.*verif|smoke.test`)
	configSubTaskRefRe = regexp.MustCompile(`(?i)blocked.by.*config|sub.task.*(secret|api.?key|client.?id)|provisioned in .* console`)
)

func checkExternalIntegration(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !isStory(issue) || !sc.storyInScope(issue.ID) {
			continue
		}
		if !externalTriggerRe.MatchString(issue.Body) {
			continue
		}

		if !hasIssueLabel(issue, "external-integration") {
			findings = append(findings, Finding{
				Check:    "external-integration",
				Severity: SeverityError,
				IssueID:  issue.ID,
				Message:  "story integrates with an external service but is missing the 'external-integration' label",
			})
		}
		if !realEndpointACRe.MatchString(issue.Body) {
			findings = append(findings, Finding{
				Check:    "external-integration",
				Severity: SeverityError,
				IssueID:  issue.ID,
				Message:  "external-integration story has no real-endpoint verification AC (manual verification / smoke test)",
			})
		}
		if !configSubTaskRefRe.MatchString(issue.Body) {
			findings = append(findings, Finding{
				Check:    "external-integration",
				Severity: SeverityReview,
				IssueID:  issue.ID,
				Message:  "external-integration story does not reference blocking config sub-tasks (secrets/API keys provisioning)",
			})
		}
	}
	return findings
}

// --- check: atomicity ---------------------------------------------------------------

const maxAcceptanceCriteria = 12

var (
	acHeadingRe   = regexp.MustCompile(`(?i)^#{1,6}\s*acceptance criteria\b`)
	mdHeadingRe   = regexp.MustCompile(`^#{1,6}\s`)
	numberedACRe  = regexp.MustCompile(`^\s*\d+[.)]`)
	bundledTitles = []string{" and ", " / "}
)

func checkAtomicity(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !isStory(issue) || !sc.storyInScope(issue.ID) {
			continue
		}

		for _, marker := range bundledTitles {
			if strings.Contains(issue.Title, marker) {
				findings = append(findings, Finding{
					Check:    "atomicity",
					Severity: SeverityReview,
					IssueID:  issue.ID,
					Message:  fmt.Sprintf("title contains %q, suggesting bundled scope (split into atomic stories)", strings.TrimSpace(marker)),
				})
				break
			}
		}

		if n := countAcceptanceCriteria(issue.Body); n > maxAcceptanceCriteria {
			findings = append(findings, Finding{
				Check:    "atomicity",
				Severity: SeverityReview,
				IssueID:  issue.ID,
				Message:  fmt.Sprintf("story has %d numbered acceptance criteria (more than %d suggests bundled scope)", n, maxAcceptanceCriteria),
			})
		}
	}
	return findings
}

// countAcceptanceCriteria counts numbered lines inside Acceptance Criteria sections.
func countAcceptanceCriteria(body string) int {
	count := 0
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case acHeadingRe.MatchString(line):
			inSection = true
		case inSection && mdHeadingRe.MatchString(line):
			inSection = false
		case inSection && numberedACRe.MatchString(line):
			count++
		}
	}
	return count
}

// --- check: vertical-slice -------------------------------------------------------------

var (
	horizontalTitleRe   = regexp.MustCompile(`(?i)build (the )?\w+ (service|module|layer|component|engine)`)
	observableOutcomeRe = regexp.MustCompile(`(?i)returns|displays|redirects|stores|emits|user (can|sees|submits)`)
)

func checkVerticalSlice(b *Backlog, sc scope) []Finding {
	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !isStory(issue) || !sc.storyInScope(issue.ID) {
			continue
		}

		if horizontalTitleRe.MatchString(issue.Title) {
			findings = append(findings, Finding{
				Check:    "vertical-slice",
				Severity: SeverityReview,
				IssueID:  issue.ID,
				Message:  "title suggests a horizontal layer (build-the-X-service); slice vertically through the stack",
			})
		}
		if !observableOutcomeRe.MatchString(issue.Body) {
			findings = append(findings, Finding{
				Check:    "vertical-slice",
				Severity: SeverityReview,
				IssueID:  issue.ID,
				Message:  "story body has no observable-outcome verb (returns/displays/redirects/stores/emits/user can...)",
			})
		}
	}
	return findings
}

// --- check: dep-cycles -------------------------------------------------------------------

// checkDepCycles detects dependency cycles over blocked_by edges using DFS,
// mirroring nd's graph.DetectCycles semantics but computed in-process so the
// linter has no nd-binary dependency. was_blocked_by edges are excluded:
// removed dependencies cannot deadlock the queue.
func checkDepCycles(b *Backlog) []Finding {
	visited := make(map[string]bool)
	onStack := make(map[string]bool)
	var path []string
	seen := make(map[string]bool)
	var cycles [][]string

	neighbors := func(id string) []string {
		issue := b.issues[id]
		deps := make([]string, 0, len(issue.BlockedBy))
		for _, dep := range issue.BlockedBy {
			if _, ok := b.issues[dep]; ok {
				deps = append(deps, dep)
			}
		}
		sort.Strings(deps)
		return deps
	}

	var dfs func(id string)
	dfs = func(id string) {
		visited[id] = true
		onStack[id] = true
		path = append(path, id)

		for _, next := range neighbors(id) {
			if onStack[next] {
				start := 0
				for i, node := range path {
					if node == next {
						start = i
						break
					}
				}
				cycle := canonicalCycle(path[start:])
				key := strings.Join(cycle, "->")
				if !seen[key] {
					seen[key] = true
					cycles = append(cycles, cycle)
				}
				continue
			}
			if !visited[next] {
				dfs(next)
			}
		}

		path = path[:len(path)-1]
		onStack[id] = false
	}

	for _, issue := range b.ordered {
		if !visited[issue.ID] {
			dfs(issue.ID)
		}
	}

	sort.Slice(cycles, func(i, j int) bool { return cycles[i][0] < cycles[j][0] })

	var findings []Finding
	for _, cycle := range cycles {
		display := append(append([]string{}, cycle...), cycle[0])
		findings = append(findings, Finding{
			Check:    "dep-cycles",
			Severity: SeverityError,
			IssueID:  cycle[0],
			Message:  fmt.Sprintf("dependency cycle: %s (via blocked_by)", strings.Join(display, " -> ")),
		})
	}
	return findings
}

// canonicalCycle rotates a cycle so its lexicographically smallest node
// comes first, making detection order-independent.
func canonicalCycle(cycle []string) []string {
	if len(cycle) == 0 {
		return nil
	}
	minIdx := 0
	for i, node := range cycle {
		if node < cycle[minIdx] {
			minIdx = i
		}
	}
	rotated := make([]string, 0, len(cycle))
	rotated = append(rotated, cycle[minIdx:]...)
	rotated = append(rotated, cycle[:minIdx]...)
	return rotated
}

// --- check: release-gate --------------------------------------------------------------------

func checkReleaseGate(b *Backlog) []Finding {
	var gates []*BacklogIssue
	for _, issue := range b.ordered {
		if !isClosed(issue) && hasIssueLabel(issue, "release-gate") {
			gates = append(gates, issue)
		}
	}

	switch len(gates) {
	case 0:
		return nil
	case 1:
		gate := gates[0]
		for blocker := range blockerSet(gate) {
			if upstream, ok := b.issues[blocker]; ok && hasIssueLabel(upstream, "capstone") {
				return nil
			}
		}
		return []Finding{{
			Check:    "release-gate",
			Severity: SeverityReview,
			IssueID:  gate.ID,
			Message:  "release-gate story is not blocked_by any capstone-labeled story",
		}}
	default:
		ids := make([]string, len(gates))
		for i, gate := range gates {
			ids[i] = gate.ID
		}
		return []Finding{{
			Check:    "release-gate",
			Severity: SeverityReview,
			IssueID:  ids[0],
			Message:  fmt.Sprintf("found %d stories labeled 'release-gate' (expected at most one): %s", len(ids), strings.Join(ids, ", ")),
		}}
	}
}

// --- check: paths-exist ------------------------------------------------------------------------

const brownfieldCommitThreshold = 50

// filePathTokenRe matches file-path-shaped tokens with source-ish extensions.
var filePathTokenRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*/)+[a-zA-Z_][a-zA-Z0-9_.-]*\.(py|ts|tsx|js|ex|exs|go|rs|rb|java|kt|swift|c|cpp|h|hpp|sql|yml|yaml|json|toml|md)\b`)

// brownfieldEnabled reports whether the paths-exist check applies: the repo
// has more than 50 commits, or lint.brownfield=true in project settings.
func brownfieldEnabled(projectRoot string, sett map[string]string) bool {
	if sett["lint.brownfield"] == "true" {
		return true
	}
	return commitCount(projectRoot) > brownfieldCommitThreshold
}

func commitCount(projectRoot string) int {
	if projectRoot == "" {
		return 0
	}
	out, err := exec.Command("git", "-C", projectRoot, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

func checkPathsExist(b *Backlog, sc scope, projectRoot string) []Finding {
	// A path is legitimate when something builds it: the union of all
	// PRODUCES paths across the backlog. Cross-story CONSUMES legitimately
	// reference paths produced upstream that do not exist on disk yet.
	producedPaths := make(map[string]bool)
	for _, issue := range b.issues {
		for _, entry := range issue.Produces {
			if p := normalizeArtifact(entry); p != "" {
				producedPaths[p] = true
			}
		}
	}

	var findings []Finding
	for _, issue := range b.ordered {
		if isClosed(issue) || !isStory(issue) || !sc.storyInScope(issue.ID) {
			continue
		}

		flagged := make(map[string]bool)
		for _, loc := range filePathTokenRe.FindAllStringIndex(issue.Body, -1) {
			// Skip matches embedded in a longer token (URLs, dotted prefixes,
			// absolute paths): be conservative, only flag clean path tokens.
			if loc[0] > 0 && isPathContextByte(issue.Body[loc[0]-1]) {
				continue
			}
			token := issue.Body[loc[0]:loc[1]]
			if flagged[token] || producedPaths[token] {
				continue
			}
			if _, err := os.Stat(filepath.Join(projectRoot, token)); err == nil {
				continue
			}
			flagged[token] = true
		}

		tokens := make([]string, 0, len(flagged))
		for token := range flagged {
			tokens = append(tokens, token)
		}
		sort.Strings(tokens)
		for _, token := range tokens {
			findings = append(findings, Finding{
				Check:    "paths-exist",
				Severity: SeverityError,
				IssueID:  issue.ID,
				Message:  fmt.Sprintf("path %s does not exist on disk and is not in any PRODUCES block", token),
			})
		}
	}
	return findings
}

func isPathContextByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '_', '.', '/', '-', '@', ':', '~':
		return true
	}
	return false
}

// --- ordering -------------------------------------------------------------------------------------

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if checkRank[findings[i].Check] != checkRank[findings[j].Check] {
			return checkRank[findings[i].Check] < checkRank[findings[j].Check]
		}
		if findings[i].IssueID != findings[j].IssueID {
			return findings[i].IssueID < findings[j].IssueID
		}
		return findings[i].Message < findings[j].Message
	})
}
