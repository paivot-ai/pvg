package lint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBacklog writes the given issue files into a fresh vault and loads it.
func buildBacklog(t *testing.T, issues map[string]string) *Backlog {
	t.Helper()
	vault := t.TempDir()
	for name, content := range issues {
		writeIssue(t, vault, name, content)
	}
	b, err := loadBacklog(vault)
	if err != nil {
		t.Fatalf("loadBacklog: %v", err)
	}
	return b
}

func findingsFor(findings []Finding, check string) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func countSeverity(findings []Finding, severity string) int {
	n := 0
	for _, f := range findings {
		if f.Severity == severity {
			n++
		}
	}
	return n
}

func TestParseBacklogIssueFile(t *testing.T) {
	vault := t.TempDir()
	writeIssue(t, vault, "PROJ-abcd.md", `---
id: PROJ-abcd
title: "Implement auth flow"
status: in_progress
priority: 1
type: task
labels: [walking-skeleton, delivered]
parent: PROJ-epic
blocked_by: [PROJ-up01, PROJ-up02]
was_blocked_by: [PROJ-old1]
---

## Description

MANDATORY SKILLS TO REVIEW: nd

PRODUCES:
- src/auth.ts -> generateToken()

CONSUMES:
- PROJ-up01: src/config.ts -> getSecret()
    spec: getSecret(key: string): string
- PROJ-up02: src/db.ts -> query()
- (existing): src/utils.ts -> hash()
`)

	issue, err := parseBacklogIssueFile(filepath.Join(vault, "issues", "PROJ-abcd.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if issue.ID != "PROJ-abcd" {
		t.Errorf("ID = %q", issue.ID)
	}
	if issue.Title != "Implement auth flow" {
		t.Errorf("Title = %q", issue.Title)
	}
	if issue.Status != "in_progress" || issue.Type != "task" || issue.Parent != "PROJ-epic" {
		t.Errorf("scalar fields: %+v", issue)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "walking-skeleton" {
		t.Errorf("Labels = %v", issue.Labels)
	}
	if len(issue.BlockedBy) != 2 || len(issue.WasBlockedBy) != 1 {
		t.Errorf("deps: blocked_by=%v was=%v", issue.BlockedBy, issue.WasBlockedBy)
	}
	if len(issue.Produces) != 1 {
		t.Fatalf("Produces = %v", issue.Produces)
	}
	if len(issue.Consumes) != 3 {
		t.Fatalf("Consumes = %v", issue.Consumes)
	}
	if !issue.Consumes[0].HasSignature {
		t.Error("first CONSUMES entry should have a signature")
	}
	if issue.Consumes[1].HasSignature {
		t.Error("second CONSUMES entry should not have a signature")
	}
	if issue.Consumes[0].Ref != "PROJ-up01" || issue.Consumes[2].Ref != "" {
		t.Errorf("refs: %+v", issue.Consumes)
	}
	if !strings.Contains(issue.Body, "MANDATORY SKILLS") {
		t.Error("body lost content")
	}
}

func TestDetectPrefix(t *testing.T) {
	t.Run("from nd config", func(t *testing.T) {
		vault := t.TempDir()
		if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("version: \"1\"\nprefix: TIX\ncreated_by: test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeIssue(t, vault, "OTHER-aaaa.md", "---\nid: OTHER-aaaa\nstatus: open\ntype: task\n---\nbody\n")
		b, err := loadBacklog(vault)
		if err != nil {
			t.Fatal(err)
		}
		if b.prefix != "TIX" {
			t.Errorf("prefix = %q, want TIX", b.prefix)
		}
	})

	t.Run("fallback to most common issue prefix", func(t *testing.T) {
		b := buildBacklog(t, map[string]string{
			"PROJ-a.md": "---\nid: PROJ-a\nstatus: open\ntype: task\n---\n",
			"PROJ-b.md": "---\nid: PROJ-b\nstatus: open\ntype: task\n---\n",
			"ZZ-c.md":   "---\nid: ZZ-c\nstatus: open\ntype: task\n---\n",
		})
		if b.prefix != "PROJ" {
			t.Errorf("prefix = %q, want PROJ", b.prefix)
		}
	})
}

func TestCheckWalkingSkeleton(t *testing.T) {
	skeletonBody := `
@spec annotations required. DLP integration wired. Rate limiting enforced.
Audit log entries written. Config registry entries added. Error handling established.
`
	tests := []struct {
		name        string
		issues      map[string]string
		gates       []qualityGate
		wantErrors  int
		wantReviews int
	}{
		{
			name: "milestone epic without skeleton is an error",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\n---\n",
			},
			gates:      qualityGates(nil),
			wantErrors: 1,
		},
		{
			name: "skeleton with all default patterns is clean",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [walking-skeleton]\n---\n" + skeletonBody,
			},
			gates: qualityGates(nil),
		},
		{
			name: "skeleton missing all patterns yields one review per pattern",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [walking-skeleton]\n---\nnothing here\n",
			},
			gates:       qualityGates(nil),
			wantReviews: len(defaultQualityGatePatterns),
		},
		{
			name: "project quality gates from settings are appended",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [walking-skeleton]\n---\n" + skeletonBody,
			},
			gates:       qualityGates(map[string]string{"lint.quality_gates": `telemetry.?span|feature.?flag`}),
			wantReviews: 2,
		},
		{
			name: "non-milestone epic is ignored",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
			},
			gates: qualityGates(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, tt.issues)
			findings := checkWalkingSkeleton(b, scope{}, tt.gates)
			if got := countSeverity(findings, SeverityError); got != tt.wantErrors {
				t.Errorf("errors = %d, want %d: %+v", got, tt.wantErrors, findings)
			}
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
		})
	}
}

func TestQualityGates_InvalidRegexFallsBackToLiteral(t *testing.T) {
	gates := qualityGates(map[string]string{"lint.quality_gates": `((broken`})
	last := gates[len(gates)-1]
	if !last.re.MatchString("uses ((broken literally") {
		t.Error("invalid pattern should match as a literal")
	}
}

func TestCheckCapstone(t *testing.T) {
	tests := []struct {
		name        string
		issues      map[string]string
		wantErrors  int
		wantReviews int
	}{
		{
			name: "epic with zero children is review",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
			},
			wantReviews: 1,
		},
		{
			name: "epic with children but no capstone is error",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\n---\n",
			},
			wantErrors: 1,
		},
		{
			name: "two capstones is error",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [capstone]\n---\n",
				"PROJ-c2.md": "---\nid: PROJ-c2\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [capstone]\n---\n",
			},
			wantErrors: 1,
		},
		{
			name: "capstone missing blocked_by edges yields one error per sibling",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\n---\n",
				"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: task\nparent: PROJ-e1\n---\n",
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [capstone]\nblocked_by: [PROJ-s1]\n---\n",
			},
			wantErrors: 1, // missing edge to PROJ-s2 only
		},
		{
			name: "was_blocked_by satisfies the edge requirement",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: closed\ntype: task\nparent: PROJ-e1\n---\n",
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: open\ntype: task\nparent: PROJ-e1\nlabels: [capstone]\nwas_blocked_by: [PROJ-s1]\n---\n",
			},
		},
		{
			name: "closed epic is skipped",
			issues: map[string]string{
				"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: closed\ntype: epic\n---\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, tt.issues)
			findings := checkCapstone(b, scope{})
			if got := countSeverity(findings, SeverityError); got != tt.wantErrors {
				t.Errorf("errors = %d, want %d: %+v", got, tt.wantErrors, findings)
			}
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
		})
	}
}

func TestCheckMandatorySkills(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\nMANDATORY SKILLS TO REVIEW: nd\n",
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: task\n---\nno skills section\n",
		"PROJ-b1.md": "---\nid: PROJ-b1\nstatus: open\ntype: bug\n---\nno skills section\n",
		"PROJ-s3.md": "---\nid: PROJ-s3\nstatus: closed\ntype: task\n---\nno skills section\n",
		"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\nno skills section\n",
	})

	findings := checkMandatorySkills(b, scope{})
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want 2 (story + bug, skip closed + epic)", findings)
	}
	for _, f := range findings {
		if f.Severity != SeverityError {
			t.Errorf("severity = %s, want error", f.Severity)
		}
		if f.IssueID != "PROJ-s2" && f.IssueID != "PROJ-b1" {
			t.Errorf("unexpected issue flagged: %s", f.IssueID)
		}
	}
}

func TestCheckConsumesSignature(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-s1.md": `---
id: PROJ-s1
status: open
type: task
---
CONSUMES:
- PROJ-up: src/auth.ts -> generateToken()
    spec: generateToken(userId: string): string
- PROJ-up: src/db.ts -> query()
- (none -- leaf story)
`,
	})

	findings := checkConsumesSignature(b, scope{})
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly 1 (bare entry)", findings)
	}
	if findings[0].Severity != SeverityError || !strings.Contains(findings[0].Message, "src/db.ts") {
		t.Errorf("unexpected finding: %+v", findings[0])
	}
}

func TestCheckConsumesSignature_AllKeysAccepted(t *testing.T) {
	keys := []string{"spec", "fields", "endpoint", "event", "schema", "source"}
	for _, key := range keys {
		b := buildBacklog(t, map[string]string{
			"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\nCONSUMES:\n- PROJ-up: artifact\n    " + key + ": something\n",
		})
		if findings := checkConsumesSignature(b, scope{}); len(findings) != 0 {
			t.Errorf("key %q should satisfy the signature check: %+v", key, findings)
		}
	}
}

func TestCheckConsumesProduces(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-up.md": "---\nid: PROJ-up\nstatus: open\ntype: task\n---\nPRODUCES:\n- src/auth.ts -> generateToken()\n",
		"PROJ-np.md": "---\nid: PROJ-np\nstatus: open\ntype: task\n---\nno produces here\n",
		"PROJ-s1.md": `---
id: PROJ-s1
status: open
type: task
---
CONSUMES:
- PROJ-up: src/auth.ts -> generateToken()
    spec: generateToken(): string
- PROJ-np: something vague
    spec: vague()
- PROJ-gone: src/missing.ts
    spec: missing()
`,
	})

	findings := checkConsumesProduces(b, scope{})
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want 2", findings)
	}
	messages := findings[0].Message + " | " + findings[1].Message
	if !strings.Contains(messages, "PROJ-gone") || !strings.Contains(messages, "PROJ-np") {
		t.Errorf("expected dangling + no-produces findings, got: %s", messages)
	}
}

func TestCheckStaleRefs(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-aaaa.md": "---\nid: PROJ-aaaa\nstatus: open\ntype: task\n---\nresolves fine\n",
		"PROJ-s1.md": `---
id: PROJ-s1
status: open
type: task
---
Good ref: PROJ-aaaa. Bad ref: PROJ-zzzz.
Placeholders: STORY-A and EPIC-AUTH-FLOW and im-01-some-slug.
Not placeholders: walking-skeleton label, e2e-tests, STORY-ABC.
`,
	})

	findings := checkStaleRefs(b, scope{})
	if len(findings) != 4 {
		t.Fatalf("findings = %+v, want 4 (PROJ-zzzz + 3 placeholders)", findings)
	}
	var tokens []string
	for _, f := range findings {
		if f.Severity != SeverityError {
			t.Errorf("severity = %s, want error", f.Severity)
		}
		tokens = append(tokens, f.Message)
	}
	joined := strings.Join(tokens, " | ")
	for _, want := range []string{"PROJ-zzzz", "STORY-A", "EPIC-AUTH-FLOW", "im-01-some-slug"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing finding for %s in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "walking-skeleton") || strings.Contains(joined, "e2e-tests") {
		t.Errorf("conservative matching violated: %s", joined)
	}
}

func TestCheckStaleRefs_ChildIDsResolve(t *testing.T) {
	b := buildBacklog(t, map[string]string{
		"PROJ-aaaa.md":   "---\nid: PROJ-aaaa\nstatus: open\ntype: task\n---\n",
		"PROJ-aaaa.1.md": "---\nid: PROJ-aaaa.1\nstatus: open\ntype: task\n---\n",
		"PROJ-s1.md":     "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\nSub-task PROJ-aaaa.1 exists; PROJ-aaaa.9 does not.\n",
	})

	findings := checkStaleRefs(b, scope{})
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "PROJ-aaaa.9") {
		t.Fatalf("findings = %+v, want 1 for PROJ-aaaa.9", findings)
	}
}

func TestCheckExternalIntegration(t *testing.T) {
	fullyStructured := `---
id: PROJ-s1
status: open
type: task
labels: [external-integration]
---
Integrate Stripe payments.
AC: manual verification against the real endpoint required.
Blocked by config sub-task: API key provisioned in Stripe console.
`
	tests := []struct {
		name        string
		content     string
		wantErrors  int
		wantReviews int
	}{
		{name: "fully structured story is clean", content: fullyStructured},
		{
			name: "missing label and ACs",
			content: `---
id: PROJ-s1
status: open
type: task
---
Integrate Stripe payments.
`,
			wantErrors:  2, // missing label + missing real-endpoint AC
			wantReviews: 1, // missing config sub-task refs
		},
		{
			name: "non-external story is ignored",
			content: `---
id: PROJ-s1
status: open
type: task
---
Plain internal refactoring story.
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, map[string]string{"PROJ-s1.md": tt.content})
			findings := checkExternalIntegration(b, scope{})
			if got := countSeverity(findings, SeverityError); got != tt.wantErrors {
				t.Errorf("errors = %d, want %d: %+v", got, tt.wantErrors, findings)
			}
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
		})
	}
}

func TestCheckAtomicity(t *testing.T) {
	numbered := func(n int) string {
		var sb strings.Builder
		sb.WriteString("## Acceptance Criteria\n")
		for i := 1; i <= n; i++ {
			sb.WriteString("1. criterion\n")
		}
		return sb.String()
	}

	tests := []struct {
		name        string
		title       string
		body        string
		wantReviews int
	}{
		{name: "clean title and AC count", title: "Implement login", body: numbered(12)},
		{name: "title with ' and '", title: "Create and delete sessions", body: numbered(1), wantReviews: 1},
		{name: "title with ' / '", title: "Login / logout flows", body: numbered(1), wantReviews: 1},
		{name: "more than 12 numbered AC", title: "Implement login", body: numbered(13), wantReviews: 1},
		{name: "numbered lines outside AC section ignored", title: "Implement login", body: "## Notes\n" + strings.Repeat("1. note\n", 20)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, map[string]string{
				"PROJ-s1.md": "---\nid: PROJ-s1\ntitle: \"" + tt.title + "\"\nstatus: open\ntype: task\n---\n" + tt.body,
			})
			findings := checkAtomicity(b, scope{})
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
		})
	}
}

func TestCheckVerticalSlice(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		body        string
		wantReviews int
	}{
		{name: "observable outcome present", title: "User login", body: "endpoint returns a session token; user can retry"},
		{name: "horizontal title", title: "Build the persistence layer", body: "stores rows", wantReviews: 1},
		{name: "no observable outcome", title: "User login", body: "wire things internally", wantReviews: 1},
		{name: "both violations", title: "Build auth module", body: "internal scaffolding only", wantReviews: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, map[string]string{
				"PROJ-s1.md": "---\nid: PROJ-s1\ntitle: \"" + tt.title + "\"\nstatus: open\ntype: task\n---\n" + tt.body + "\n",
			})
			findings := checkVerticalSlice(b, scope{})
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
		})
	}
}

func TestCheckDuplicateSections(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		status       string
		wantFindings int
		wantContains string
	}{
		{
			name:   "single occurrence of each canonical heading is clean",
			body:   "## Description\ntext\n\n## Acceptance Criteria\n1. ok\n\n## Notes\nn\n\n## History\nh\n\n## Links\nl\n\n## Comments\nc\n\n## Design\nd\n",
			status: "open",
		},
		{
			name:         "duplicate acceptance criteria heading flagged",
			body:         "## Description\ntext\n\n## Acceptance Criteria\n1. real AC\n\n## Acceptance Criteria\nauthored duplicate\n",
			status:       "open",
			wantFindings: 1,
			wantContains: `"## Acceptance Criteria" appears 2 times`,
		},
		{
			name:         "multiple distinct duplicated headings each flagged",
			body:         "## Description\na\n## Description\nb\n## Notes\nc\n## Notes\nd\n",
			status:       "open",
			wantFindings: 2,
		},
		{
			name:   "non-canonical duplicate heading is not flagged",
			body:   "## Implementation\na\n## Implementation\nb\n",
			status: "open",
		},
		{
			name:   "deeper heading level does not collide with canonical section",
			body:   "## Description\ntext\n### Description\nnested authored heading\n",
			status: "open",
		},
		{
			name:   "heading with trailing text is not a canonical match",
			body:   "## Description\ntext\n## Description of the rollout\nmore\n",
			status: "open",
		},
		{
			name:   "closed issue is skipped",
			body:   "## Description\na\n## Description\nb\n",
			status: "closed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, map[string]string{
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: " + tt.status + "\ntype: task\n---\n" + tt.body,
			})
			findings := checkDuplicateSections(b, scope{})
			if len(findings) != tt.wantFindings {
				t.Fatalf("findings = %+v, want %d", findings, tt.wantFindings)
			}
			for _, f := range findings {
				if f.Severity != SeverityReview {
					t.Errorf("severity = %s, want review", f.Severity)
				}
				if f.IssueID != "PROJ-s1" || !strings.Contains(f.Message, "PROJ-s1") {
					t.Errorf("finding must name the issue ID: %+v", f)
				}
				if !strings.Contains(f.Message, "nd-managed section headings") {
					t.Errorf("message missing guidance: %s", f.Message)
				}
			}
			if tt.wantContains != "" && !strings.Contains(findings[0].Message, tt.wantContains) {
				t.Errorf("message = %q, want it to contain %q", findings[0].Message, tt.wantContains)
			}
		})
	}
}

func TestCheckDuplicateSections_EpicScope(t *testing.T) {
	dupBody := "## Description\na\n## Description\nb\n"
	b := buildBacklog(t, map[string]string{
		"PROJ-e1.md": "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n",
		"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\n---\n" + dupBody,
		"PROJ-s2.md": "---\nid: PROJ-s2\nstatus: open\ntype: task\nparent: PROJ-e2\n---\n" + dupBody,
	})

	findings := checkDuplicateSections(b, scope{
		epicIDs:  map[string]bool{"PROJ-e1": true},
		storyIDs: map[string]bool{"PROJ-s1": true},
	})
	if len(findings) != 1 || findings[0].IssueID != "PROJ-s1" {
		t.Fatalf("expected only in-scope PROJ-s1 flagged, got %+v", findings)
	}
}

func TestCheckDepCycles(t *testing.T) {
	t.Run("two-node cycle reported once", func(t *testing.T) {
		b := buildBacklog(t, map[string]string{
			"PROJ-a.md": "---\nid: PROJ-a\nstatus: open\ntype: task\nblocked_by: [PROJ-b]\n---\n",
			"PROJ-b.md": "---\nid: PROJ-b\nstatus: open\ntype: task\nblocked_by: [PROJ-a]\n---\n",
			"PROJ-c.md": "---\nid: PROJ-c\nstatus: open\ntype: task\nblocked_by: [PROJ-a]\n---\n",
		})
		findings := checkDepCycles(b)
		if len(findings) != 1 {
			t.Fatalf("findings = %+v, want exactly 1", findings)
		}
		f := findings[0]
		if f.Severity != SeverityError || f.IssueID != "PROJ-a" {
			t.Errorf("unexpected finding: %+v", f)
		}
		if !strings.Contains(f.Message, "PROJ-a -> PROJ-b -> PROJ-a") {
			t.Errorf("cycle path missing: %s", f.Message)
		}
	})

	t.Run("acyclic graph is clean", func(t *testing.T) {
		b := buildBacklog(t, map[string]string{
			"PROJ-a.md": "---\nid: PROJ-a\nstatus: open\ntype: task\n---\n",
			"PROJ-b.md": "---\nid: PROJ-b\nstatus: open\ntype: task\nblocked_by: [PROJ-a]\n---\n",
			"PROJ-c.md": "---\nid: PROJ-c\nstatus: open\ntype: task\nblocked_by: [PROJ-a, PROJ-b]\n---\n",
		})
		if findings := checkDepCycles(b); len(findings) != 0 {
			t.Errorf("findings = %+v, want none", findings)
		}
	})

	t.Run("was_blocked_by does not create cycles", func(t *testing.T) {
		b := buildBacklog(t, map[string]string{
			"PROJ-a.md": "---\nid: PROJ-a\nstatus: open\ntype: task\nwas_blocked_by: [PROJ-b]\n---\n",
			"PROJ-b.md": "---\nid: PROJ-b\nstatus: open\ntype: task\nblocked_by: [PROJ-a]\n---\n",
		})
		if findings := checkDepCycles(b); len(findings) != 0 {
			t.Errorf("findings = %+v, want none", findings)
		}
	})
}

func TestCheckReleaseGate(t *testing.T) {
	tests := []struct {
		name        string
		issues      map[string]string
		wantReviews int
		wantSubstr  string
	}{
		{
			name: "no release gate is clean",
			issues: map[string]string{
				"PROJ-s1.md": "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\n",
			},
		},
		{
			name: "two release gates is review",
			issues: map[string]string{
				"PROJ-g1.md": "---\nid: PROJ-g1\nstatus: open\ntype: task\nlabels: [release-gate]\n---\n",
				"PROJ-g2.md": "---\nid: PROJ-g2\nstatus: open\ntype: task\nlabels: [release-gate]\n---\n",
			},
			wantReviews: 1,
			wantSubstr:  "expected at most one",
		},
		{
			name: "single gate not blocked by capstone is review",
			issues: map[string]string{
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: open\ntype: task\nlabels: [capstone]\n---\n",
				"PROJ-g1.md": "---\nid: PROJ-g1\nstatus: open\ntype: task\nlabels: [release-gate]\n---\n",
			},
			wantReviews: 1,
			wantSubstr:  "not blocked_by any capstone",
		},
		{
			name: "single gate blocked by capstone is clean",
			issues: map[string]string{
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: open\ntype: task\nlabels: [capstone]\n---\n",
				"PROJ-g1.md": "---\nid: PROJ-g1\nstatus: open\ntype: task\nlabels: [release-gate]\nblocked_by: [PROJ-c1]\n---\n",
			},
		},
		{
			name: "closed gates are not counted",
			issues: map[string]string{
				"PROJ-c1.md": "---\nid: PROJ-c1\nstatus: closed\ntype: task\nlabels: [capstone]\n---\n",
				"PROJ-g1.md": "---\nid: PROJ-g1\nstatus: closed\ntype: task\nlabels: [release-gate]\n---\n",
				"PROJ-g2.md": "---\nid: PROJ-g2\nstatus: open\ntype: task\nlabels: [release-gate]\nblocked_by: [PROJ-c1]\n---\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, tt.issues)
			findings := checkReleaseGate(b)
			if got := countSeverity(findings, SeverityReview); got != tt.wantReviews {
				t.Errorf("reviews = %d, want %d: %+v", got, tt.wantReviews, findings)
			}
			if tt.wantSubstr != "" && (len(findings) == 0 || !strings.Contains(findings[0].Message, tt.wantSubstr)) {
				t.Errorf("expected message containing %q: %+v", tt.wantSubstr, findings)
			}
		})
	}
}

func TestCheckPathsExist(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "src", "exists.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := buildBacklog(t, map[string]string{
		"PROJ-up.md": "---\nid: PROJ-up\nstatus: open\ntype: task\n---\nPRODUCES:\n- src/upstream.go -> Build()\n",
		"PROJ-s1.md": `---
id: PROJ-s1
status: open
type: task
---
Touch src/exists.go and create src/built.go. Consume src/upstream.go.
Fabricated: src/missing.go. URL: https://example.com/docs/page.md is fine.

PRODUCES:
- src/built.go -> New()
`,
	})

	findings := checkPathsExist(b, scope{}, projectRoot)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly 1 (src/missing.go)", findings)
	}
	f := findings[0]
	if f.Severity != SeverityError || f.IssueID != "PROJ-s1" || !strings.Contains(f.Message, "src/missing.go") {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestBrownfieldEnabled(t *testing.T) {
	t.Run("settings flag enables", func(t *testing.T) {
		if !brownfieldEnabled(t.TempDir(), map[string]string{"lint.brownfield": "true"}) {
			t.Error("expected enabled via settings")
		}
	})
	t.Run("non-git directory with no flag is disabled", func(t *testing.T) {
		if brownfieldEnabled(t.TempDir(), nil) {
			t.Error("expected disabled")
		}
	})
}

func TestCommitCount(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("commit", "--allow-empty", "-m", "one")
	run("commit", "--allow-empty", "-m", "two")

	if got := commitCount(repo); got != 2 {
		t.Errorf("commitCount = %d, want 2", got)
	}
	if got := commitCount(t.TempDir()); got != 0 {
		t.Errorf("commitCount on non-repo = %d, want 0", got)
	}
}

// cleanStory returns a story body that passes all story-level checks.
func cleanStory(id, parent string, extraFrontmatter, extraBody string) string {
	return `---
id: ` + id + `
title: "Story ` + id + `"
status: open
type: task
parent: ` + parent + `
` + extraFrontmatter + `---

## Description
User submits a form and the endpoint returns a confirmation.

MANDATORY SKILLS TO REVIEW: nd

## Acceptance Criteria
1. returns 200 on success
` + extraBody
}

func TestCheckBacklog_EndToEnd(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()

	if err := os.WriteFile(filepath.Join(vault, ".nd.yaml"), []byte("version: \"1\"\nprefix: PROJ\ncreated_by: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeIssue(t, vault, "PROJ-e1.md", `---
id: PROJ-e1
title: "Milestone epic"
status: open
type: epic
labels: [milestone]
---
epic body
`)
	skeletonBody := "\n@spec DLP rate-limit audit log config-registration error-handling\n"
	writeIssue(t, vault, "PROJ-s1.md", cleanStory("PROJ-s1", "PROJ-e1", "labels: [walking-skeleton]\n", skeletonBody))
	writeIssue(t, vault, "PROJ-c1.md", cleanStory("PROJ-c1", "PROJ-e1", "labels: [capstone]\nblocked_by: [PROJ-s1, PROJ-s2]\n", ""))
	// PROJ-s2 is missing its MANDATORY SKILLS section -> 1 error
	writeIssue(t, vault, "PROJ-s2.md", `---
id: PROJ-s2
title: "Story two"
status: open
type: task
parent: PROJ-e1
---
The endpoint returns data.
`)

	result, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("CheckBacklog: %v", err)
	}

	if result.Issues != 4 {
		t.Errorf("Issues = %d, want 4", result.Issues)
	}
	ms := findingsFor(result.Findings, "mandatory-skills")
	if len(ms) != 1 || ms[0].IssueID != "PROJ-s2" {
		t.Errorf("mandatory-skills findings = %+v", ms)
	}
	if result.Errors != countSeverity(result.Findings, SeverityError) {
		t.Errorf("Errors miscounted: %d", result.Errors)
	}
	if len(findingsFor(result.Findings, "walking-skeleton")) != 0 {
		t.Errorf("unexpected walking-skeleton findings: %+v", result.Findings)
	}
	if len(findingsFor(result.Findings, "capstone")) != 0 {
		t.Errorf("unexpected capstone findings: %+v", result.Findings)
	}

	// JSON contract: flat array of {check, severity, issue_id, message}.
	out, err := FormatBacklogJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output not a flat array: %v\n%s", err, out)
	}
	if len(parsed) != len(result.Findings) {
		t.Errorf("JSON has %d findings, want %d", len(parsed), len(result.Findings))
	}
	for _, key := range []string{"check", "severity", "issue_id", "message"} {
		if _, ok := parsed[0][key]; !ok {
			t.Errorf("JSON finding missing key %q: %v", key, parsed[0])
		}
	}

	text := FormatBacklogText(result)
	if !strings.Contains(text, "mandatory-skills:") || !strings.Contains(text, "PROJ-s2") {
		t.Errorf("text output missing grouped finding:\n%s", text)
	}
	if !strings.Contains(text, "error(s)") || !strings.Contains(text, "review finding(s)") {
		t.Errorf("text output missing summary line:\n%s", text)
	}
}

func TestCheckBacklog_EpicScope(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()

	writeIssue(t, vault, "PROJ-e1.md", "---\nid: PROJ-e1\nstatus: open\ntype: epic\n---\n")
	writeIssue(t, vault, "PROJ-e2.md", "---\nid: PROJ-e2\nstatus: open\ntype: epic\n---\n")
	// Both epics under-decomposed; both children missing MANDATORY SKILLS.
	writeIssue(t, vault, "PROJ-s1.md", "---\nid: PROJ-s1\nstatus: open\ntype: task\nparent: PROJ-e1\n---\nreturns data\n")
	writeIssue(t, vault, "PROJ-s2.md", "---\nid: PROJ-s2\nstatus: open\ntype: task\nparent: PROJ-e2\n---\nreturns data\n")

	result, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: projectRoot, EpicID: "PROJ-e1"})
	if err != nil {
		t.Fatalf("CheckBacklog: %v", err)
	}

	for _, f := range result.Findings {
		if f.Check == "mandatory-skills" && f.IssueID != "PROJ-s1" {
			t.Errorf("story-level finding outside epic scope: %+v", f)
		}
		if f.Check == "capstone" && f.IssueID != "PROJ-e1" && f.IssueID != "PROJ-s1" {
			t.Errorf("epic-level finding outside epic scope: %+v", f)
		}
	}
}

func TestCheckBacklog_EpicNotFound(t *testing.T) {
	vault := t.TempDir()
	writeIssue(t, vault, "PROJ-s1.md", "---\nid: PROJ-s1\nstatus: open\ntype: task\n---\n")

	if _, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: t.TempDir(), EpicID: "PROJ-nope"}); err == nil {
		t.Fatal("expected error for unknown epic")
	}
	if _, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: t.TempDir(), EpicID: "PROJ-s1"}); err == nil {
		t.Fatal("expected error for non-epic scope target")
	}
}

func TestCheckBacklog_CollisionFindings(t *testing.T) {
	vault := t.TempDir()
	writeIssue(t, vault, "PROJ-a.md", "---\nid: PROJ-a\nstatus: open\ntype: task\n---\nMANDATORY SKILLS\nreturns ok\nPRODUCES:\n- src/auth.ts -> a()\n")
	writeIssue(t, vault, "PROJ-b.md", "---\nid: PROJ-b\nstatus: open\ntype: task\n---\nMANDATORY SKILLS\nreturns ok\nPRODUCES:\n- src/auth.ts -> b()\n")

	result, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	collisions := findingsFor(result.Findings, "produces-collision")
	if len(collisions) != 1 {
		t.Fatalf("collision findings = %+v, want 1", collisions)
	}
	f := collisions[0]
	if f.Severity != SeverityError || f.IssueID != "PROJ-a" {
		t.Errorf("unexpected collision finding: %+v", f)
	}
	if !strings.Contains(f.Message, "src/auth.ts") || !strings.Contains(f.Message, "PROJ-b") {
		t.Errorf("collision message incomplete: %s", f.Message)
	}
}

func TestCheckBacklog_QualityGatesFromSettings(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()

	settingsDir := filepath.Join(projectRoot, ".vault", "knowledge")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, ".settings.yaml"),
		[]byte("lint.quality_gates: telemetry.?span\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeIssue(t, vault, "PROJ-e1.md", "---\nid: PROJ-e1\nstatus: open\ntype: epic\nlabels: [milestone]\n---\n")
	writeIssue(t, vault, "PROJ-s1.md", `---
id: PROJ-s1
status: open
type: task
parent: PROJ-e1
labels: [walking-skeleton]
---
@spec DLP rate-limit audit log config-registration error-handling
MANDATORY SKILLS -- returns ok
`)

	result, err := CheckBacklog(BacklogOptions{VaultDir: vault, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}

	ws := findingsFor(result.Findings, "walking-skeleton")
	if len(ws) != 1 || !strings.Contains(ws[0].Message, "telemetry.?span") {
		t.Errorf("expected one review for missing project gate, got: %+v", ws)
	}
	if ws[0].Severity != SeverityReview {
		t.Errorf("severity = %s, want review", ws[0].Severity)
	}
}

func TestSortFindings(t *testing.T) {
	findings := []Finding{
		{Check: "paths-exist", IssueID: "B"},
		{Check: "walking-skeleton", IssueID: "Z"},
		{Check: "walking-skeleton", IssueID: "A"},
		{Check: "produces-collision", IssueID: "C"},
	}
	sortFindings(findings)
	wantOrder := []string{"produces-collision", "walking-skeleton", "walking-skeleton", "paths-exist"}
	for i, f := range findings {
		if f.Check != wantOrder[i] {
			t.Fatalf("order[%d] = %s, want %s", i, f.Check, wantOrder[i])
		}
	}
	if findings[1].IssueID != "A" {
		t.Errorf("within-check sort by issue ID failed: %+v", findings)
	}
}

func TestFormatBacklogJSON_EmptyIsArray(t *testing.T) {
	out, err := FormatBacklogJSON(BacklogResult{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty findings JSON = %q, want []", out)
	}
}

func TestFormatBacklogText_Clean(t *testing.T) {
	out := FormatBacklogText(BacklogResult{Issues: 7})
	if !strings.Contains(out, "PASSED") || !strings.Contains(out, "scanned 7 issues") {
		t.Errorf("unexpected clean output: %s", out)
	}
}

func TestFormatBacklogText_Failed(t *testing.T) {
	out := FormatBacklogText(BacklogResult{
		Issues:  2,
		Errors:  1,
		Reviews: 1,
		Findings: []Finding{
			{Check: "capstone", Severity: SeverityError, IssueID: "PROJ-e1", Message: "epic has 0 children labeled 'capstone' (expected exactly 1)"},
			{Check: "atomicity", Severity: SeverityReview, IssueID: "PROJ-s1", Message: "title contains \"and\""},
		},
	})
	for _, want := range []string{"FAILED", "capstone:", "atomicity:", "[error]", "[review]", "PROJ-e1", "1 error(s), 1 review finding(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
