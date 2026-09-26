package lint

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateFormalAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name string
		body string
		want FormalAcceptanceCode
	}{
		{
			name: "concrete checkbox criterion passes",
			body: "## Description\nDetailed requirements.\n\n## Acceptance Criteria\n- [ ] The command fails closed and names the story.\n",
		},
		{
			name: "legacy numbered criterion passes",
			body: "## Description\nDetailed requirements.\n\n## Acceptance Criteria\n1. The command fails closed and names the story.\n",
		},
		{
			name: "missing section fails",
			body: "## Description\nDetailed surrogate requirements and a delivered AC table.\n",
			want: FormalACMissing,
		},
		{
			name: "blank section fails",
			body: "## Description\nDetailed surrogate requirements.\n\n## Acceptance Criteria\n\n## Design\nNone.\n",
			want: FormalACBlank,
		},
		{
			name: "placeholder-only section fails",
			body: "## Acceptance Criteria\n- [ ] TBD\n",
			want: FormalACPlaceholder,
		},
		{
			name: "prose without AC items fails",
			body: "## Acceptance Criteria\nThe implementation should be good.\n",
			want: FormalACItemsMalformed,
		},
		{
			name: "wrong heading level fails",
			body: "### Acceptance Criteria\n- [ ] The command fails closed and names the story.\n",
			want: FormalACHeadingMalformed,
		},
		{
			name: "case-mismatched heading fails",
			body: "## acceptance criteria\n- [ ] The command fails closed and names the story.\n",
			want: FormalACHeadingMalformed,
		},
		{
			name: "duplicate sections fail",
			body: "## Acceptance Criteria\n- [ ] The command fails closed and names the story.\n\n## Acceptance Criteria\n- [ ] Another criterion.\n",
			want: FormalACDuplicate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormalAcceptanceCriteria("PROJ-s1", tt.body)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateFormalAcceptanceCriteria() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateFormalAcceptanceCriteria() = nil, want typed error")
			}
			var typed *FormalAcceptanceError
			if !errors.As(err, &typed) {
				t.Fatalf("ValidateFormalAcceptanceCriteria() = %T, want *FormalAcceptanceError", err)
			}
			if typed.Code != tt.want {
				t.Fatalf("code = %q, want %q (error: %v)", typed.Code, tt.want, err)
			}
			if typed.StoryID != "PROJ-s1" || typed.Section != FormalAcceptanceSection {
				t.Fatalf("diagnostic must name story and section: %+v", typed)
			}
			if typed.Repair == "" {
				t.Fatalf("diagnostic must include a repair: %+v", typed)
			}
		})
	}
}

func TestCheckFormalAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		status       string
		wantFindings int
		wantContains string
	}{
		{
			name:         "blank formal section is an error",
			body:         "## Description\nDetailed requirements and MANDATORY SKILLS: pvg; command returns proof.\n\n## Acceptance Criteria\n",
			status:       "in_progress",
			wantFindings: 1,
			wantContains: "formal acceptance criteria section \"## Acceptance Criteria\" is blank",
		},
		{
			name:   "concrete formal section passes",
			body:   "## Acceptance Criteria\n- [ ] The command fails closed and names the story.\n",
			status: "in_progress",
		},
		{
			name:   "closed historical record is not retroactively linted",
			body:   "## Description\nHistorical record.\n\n## Acceptance Criteria\n",
			status: "closed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buildBacklog(t, map[string]string{
				"PROJ-s1.md": "---\nid: PROJ-s1\ntitle: Formal AC\nstatus: " + tt.status + "\ntype: bug\n---\n" + tt.body + "\n",
			})
			findings := checkFormalAcceptanceCriteria(b, scope{})
			if len(findings) != tt.wantFindings {
				t.Fatalf("findings = %+v, want %d", findings, tt.wantFindings)
			}
			if tt.wantFindings > 0 {
				if findings[0].Severity != SeverityError || findings[0].Check != "acceptance-criteria" {
					t.Fatalf("finding = %+v, want acceptance-criteria error", findings[0])
				}
				if !strings.Contains(findings[0].Message, tt.wantContains) {
					t.Fatalf("message = %q, want %q", findings[0].Message, tt.wantContains)
				}
			}
		})
	}
}
