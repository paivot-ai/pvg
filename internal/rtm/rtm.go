// Package rtm implements Requirement Traceability Matrix verification.
// It reads D&F documents, extracts tagged requirements and section headings,
// then checks that each has a covering story in the nd backlog.
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
	Tag       string `json:"tag"`                  // e.g. "[NEW]", "[EXPANDED]", "##"
	Text      string `json:"text"`                 // the requirement text
	Source    string `json:"source"`               // which D&F doc it came from
	Line      int    `json:"line"`                 // line number in the source
	Covered   bool   `json:"covered"`              // whether a story references it
	CoveredBy string `json:"covered_by,omitempty"` // story ID that covers it
}

// RTMResult holds the full traceability check output.
type RTMResult struct {
	Requirements []Requirement `json:"requirements"`
	Total        int           `json:"total"`
	Covered      int           `json:"covered"`
	Uncovered    int           `json:"uncovered"`
	Stories      int           `json:"stories_checked"`
	Passed       bool          `json:"passed"`
}

// tagPattern matches lines with [NEW], [EXPANDED], [CRITICAL], [REQUIRED], [CHANGED].
var tagPattern = regexp.MustCompile(`\[(NEW|EXPANDED|CRITICAL|REQUIRED|CHANGED)\]`)

// CheckCoverage reads D&F docs from projectRoot, extracts requirements,
// and checks them against stories in the nd vault.
func CheckCoverage(projectRoot, vaultDir string) (RTMResult, error) {
	var result RTMResult

	// Extract requirements from D&F docs
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

	// The design substrate contributes deterministic requirements: every
	// oracle stable id is a transition the backlog must cover. Coverage for
	// these is exact whole-token matching, not the keyword heuristic. The
	// substrate is strictly user-opt-in via design.machinery (default off);
	// machinery artifacts alone never pull oracle ids into the RTM.
	sett := settings.LoadFile(filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml"))
	if cfg, applies, _ := design.Applies(projectRoot, design.MachinerySetting(sett)); applies {
		ids, derr := design.StableIDs(projectRoot, cfg)
		if derr != nil {
			return result, fmt.Errorf("read design oracles: %w", derr)
		}
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		for _, id := range ordered {
			result.Requirements = append(result.Requirements, Requirement{
				Tag:    "[ORACLE]",
				Text:   id,
				Source: filepath.ToSlash(filepath.Join(cfg.Dir, "machines", ids[id])),
			})
		}
	}

	result.Total = len(result.Requirements)
	if result.Total == 0 {
		result.Passed = true
		return result, nil
	}

	// Load all non-closed story bodies from the vault
	storyBodies, storyCount, err := loadStoryBodies(vaultDir)
	if err != nil {
		return result, fmt.Errorf("load stories: %w", err)
	}
	result.Stories = storyCount

	// Check coverage: for each requirement, search for it in story bodies
	for i := range result.Requirements {
		req := &result.Requirements[i]
		var storyID string
		if req.Tag == "[ORACLE]" {
			storyID = findStoryWithToken(req.Text, storyBodies)
		} else {
			storyID = findCoveringStory(req.Text, storyBodies)
		}
		if storyID != "" {
			req.Covered = true
			req.CoveredBy = storyID
			result.Covered++
		}
	}

	result.Uncovered = result.Total - result.Covered
	result.Passed = result.Uncovered == 0
	return result, nil
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

// LoadStoryBodies reads all non-closed issue files and returns a map of
// ID -> body text plus the count. Shared with story sync-oracle.
func LoadStoryBodies(vaultDir string) (map[string]string, int, error) {
	return loadStoryBodies(vaultDir)
}

// loadStoryBodies reads all non-closed issue files and returns a map of ID -> body text.
func loadStoryBodies(vaultDir string) (map[string]string, int, error) {
	issuesDir := filepath.Join(vaultDir, "issues")
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}

	bodies := make(map[string]string)
	count := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(issuesDir, entry.Name())
		id, status, body, err := parseIssueForBody(path)
		if err != nil {
			continue
		}
		if strings.EqualFold(status, "closed") {
			continue
		}
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), ".md")
		}
		bodies[id] = body
		count++
	}

	return bodies, count, nil
}

// parseIssueForBody reads an nd issue file and returns its ID, status, and body.
func parseIssueForBody(path string) (id, status, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", err
	}

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
					value := strings.TrimSpace(parts[1])
					switch key {
					case "id":
						id = value
					case "status":
						status = value
					}
				}
				continue
			}
			frontmatterDone = true
		}
		bodyLines = append(bodyLines, line)
	}

	return id, status, strings.Join(bodyLines, "\n"), nil
}

// findStoryWithToken returns a story whose body carries the exact token
// (oracle stable ids are opaque; keyword matching would be meaningless).
// Iteration order is sorted for deterministic CoveredBy attribution.
func findStoryWithToken(token string, storyBodies map[string]string) string {
	ids := make([]string, 0, len(storyBodies))
	for id := range storyBodies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if design.TokenIn(token, storyBodies[id]) {
			return id
		}
	}
	return ""
}

// findCoveringStory searches story bodies for a requirement's key phrases.
// Returns the first story ID that contains significant words from the requirement.
func findCoveringStory(reqText string, storyBodies map[string]string) string {
	// Extract significant words (3+ chars, not common tags)
	keywords := extractKeywords(reqText)
	if len(keywords) == 0 {
		return ""
	}

	// A story covers the requirement if it contains at least 60% of keywords
	threshold := max(1, len(keywords)*60/100)

	for storyID, body := range storyBodies {
		bodyLower := strings.ToLower(body)
		hits := 0
		for _, kw := range keywords {
			if strings.Contains(bodyLower, kw) {
				hits++
			}
		}
		if hits >= threshold {
			return storyID
		}
	}
	return ""
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
	fmt.Fprintf(&b, "[RTM] %d requirements extracted, %d covered, %d uncovered (%d stories checked)\n",
		r.Total, r.Covered, r.Uncovered, r.Stories)

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
