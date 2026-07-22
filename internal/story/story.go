package story

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/paivot-ai/pvg/internal/guard"
	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/ndvault"
)

var execCommand = exec.Command

type TransitionOptions struct {
	Reason    string
	Feedback  string
	NextStory string
	// SkipDesign waives the approve-red design preconditions (machinery RED
	// exit gate); the reason is recorded in the story contract so the waiver
	// leaves a trace.
	SkipDesign       bool
	SkipDesignReason string
}

type DeliveryReport struct {
	StoryID string
	Passed  int
	Failed  int
	Checks  []CheckResult
}

type CheckResult struct {
	Name    string
	Passed  bool
	Message string
}

type issueDocument struct {
	Labels []string `json:"labels"`
	Status string   `json:"status"`
	Parent string   `json:"parent"`
}

func Transition(projectRoot, action, storyID string, opts TransitionOptions) (string, error) {
	vaultDir, err := ndvault.Ensure(projectRoot)
	if err != nil {
		return "", fmt.Errorf("ensure nd vault: %w", err)
	}

	shown, err := outputND(projectRoot, "show", storyID)
	if err != nil {
		return "", fmt.Errorf("story not found: %s", storyID)
	}

	switch action {
	case "claim":
		// Claiming at dispatch closes the duplicate-dispatch window: the
		// story leaves the ready queue the moment the dispatcher decides to
		// spawn a developer, not when the developer eventually mutates nd.
		// nd claim is atomic under the vault lock (assignee + in_progress in
		// one step) and fails non-zero when another agent already holds the
		// claim or the story has open blockers -- the dispatcher treats that
		// failure as "skip this story". The agent name is deterministic
		// (dev-<story-id>) so a re-claim after approve-red or a session
		// restart is an idempotent no-op instead of a steal.
		if err := runND(projectRoot, "claim", storyID, "--agent", "dev-"+storyID); err != nil {
			return "", err
		}
	case "approve-red":
		// Hard-TDD RED approval: tests are validated, story returns to the
		// ready queue for the GREEN implementation phase. The red-approved
		// label is the phase boundary the loop reads. Deliberately NOT a
		// close: a RED story has tests only, no implementation.
		//
		// On a machinery-managed project, approval first passes the
		// deterministic RED exit gate: the design check is green and every
		// oracle stable id the story references is carried by a test.
		designNote := ""
		if opts.SkipDesign {
			reason := strings.TrimSpace(opts.SkipDesignReason)
			if reason == "" {
				return "", fmt.Errorf("--skip-design requires a reason (it is recorded in the story contract)")
			}
			designNote = "Design RED gate SKIPPED by explicit waiver: " + reason + "."
		} else {
			note, gerr := designRedGate(projectRoot, storyID, string(shown))
			if gerr != nil {
				return "", gerr
			}
			if note != "" {
				designNote = "Design RED gate: " + note + "."
			}
		}
		if err := runND(projectRoot, "update", storyID, "--status=open"); err != nil {
			return "", err
		}
		_ = runND(projectRoot, "labels", "rm", storyID, "delivered")
		_ = runND(projectRoot, "labels", "rm", storyID, "rejected")
		if err := runND(projectRoot, "labels", "add", storyID, "red-approved"); err != nil {
			return "", err
		}
		evidence := fmt.Sprintf("RED tests approved via pvg story approve-red on %s.", today())
		if designNote != "" {
			evidence += " " + designNote
		}
		if err := appendContract(projectRoot, storyID, "red-approved",
			evidence,
			"[ ] GREEN developer must implement against the approved RED tests without modifying them.",
		); err != nil {
			return "", err
		}
	case "deliver":
		if err := runND(projectRoot, "update", storyID, "--status=in_progress"); err != nil {
			return "", err
		}
		_ = runND(projectRoot, "labels", "rm", storyID, "rejected")
		_ = runND(projectRoot, "labels", "rm", storyID, "accepted")
		if err := runND(projectRoot, "labels", "add", storyID, "delivered"); err != nil {
			return "", err
		}
		if err := appendContract(projectRoot, storyID, "delivered",
			fmt.Sprintf("Transitioned via pvg story deliver on %s.", today()),
			"[ ] Developer evidence block must remain authoritative above this contract.",
		); err != nil {
			return "", err
		}
	case "accept":
		if opts.NextStory != "" {
			if _, err := outputND(projectRoot, "show", opts.NextStory); err != nil {
				return "", fmt.Errorf("next story not found: %s", opts.NextStory)
			}
		}
		if err := runND(projectRoot, "close", storyID, "--reason="+defaultString(opts.Reason, "Accepted via pvg story accept")); err != nil {
			return "", err
		}
		if err := runND(projectRoot, "labels", "add", storyID, "accepted"); err != nil {
			return "", err
		}
		_ = runND(projectRoot, "labels", "rm", storyID, "delivered")
		_ = runND(projectRoot, "labels", "rm", storyID, "rejected")
		if err := appendContract(projectRoot, storyID, "accepted",
			fmt.Sprintf("PM closeout applied via pvg story accept on %s.", today()),
			"[x] Story closed after accepted label was applied.",
		); err != nil {
			return "", err
		}
		if opts.NextStory != "" {
			// Same atomic claim as the "claim" action: if another agent got
			// there first, surface the warning instead of double-dispatching.
			if err := runND(projectRoot, "claim", opts.NextStory, "--agent", "dev-"+opts.NextStory); err != nil {
				if runDoctorErr := runND(projectRoot, "doctor", "--fix"); runDoctorErr != nil {
					return "", fmt.Errorf("accepted %s but could not start next story %s: %v (doctor also failed: %v)", storyID, opts.NextStory, err, runDoctorErr)
				}
				return fmt.Sprintf("OK: accept %s using shared nd vault %s (warning: accepted story closed successfully, but could not start next story %s: %v)", storyID, vaultDir, opts.NextStory, err), nil
			}
		}
	case "reject":
		// Deterministic rejection counter, read BEFORE any label mutation:
		// the count of prior rejections comes from the counter labels plus
		// the append-only rejected contract blocks (see rejectionCount).
		priorRejections := rejectionCount(projectRoot, storyID)
		// Release instead of a bare status update: rejection must clear the
		// developer's claim (assignee) as well as return the story to open,
		// or the next claim would look like a steal. --force releases the
		// claim regardless of which agent identity runs the PM side. nd
		// release routes the open transition through the same FSM validation
		// as `nd update --status open`, so no separate fallback is needed.
		if err := runND(projectRoot, "release", storyID, "--force"); err != nil {
			return "", err
		}
		_ = runND(projectRoot, "labels", "rm", storyID, "delivered")
		_ = runND(projectRoot, "labels", "rm", storyID, "accepted")
		if err := runND(projectRoot, "labels", "add", storyID, "rejected"); err != nil {
			return "", err
		}
		// First rejection keeps plain "rejected"; subsequent rejections also
		// set the counter label rejected-x2, then rejected-x3 (replacing the
		// previous counter label). The counter caps at loop.RejectionCap: at
		// the cap `pvg loop next` escalates to the user instead of
		// dispatching more rework, so higher labels never occur.
		if count := min(priorRejections+1, loop.RejectionCap); count >= 2 {
			if count > 2 {
				_ = runND(projectRoot, "labels", "rm", storyID, fmt.Sprintf("rejected-x%d", count-1))
			}
			if err := runND(projectRoot, "labels", "add", storyID, fmt.Sprintf("rejected-x%d", count)); err != nil {
				return "", err
			}
		}
		if strings.TrimSpace(opts.Feedback) != "" {
			if err := runND(projectRoot, "comments", "add", storyID, opts.Feedback); err != nil {
				return "", err
			}
		}
		if err := appendContract(projectRoot, storyID, "rejected",
			fmt.Sprintf("PM rejection applied via pvg story reject on %s.", today()),
			"[ ] Story requires another developer delivery before it can be accepted.",
		); err != nil {
			return "", err
		}
	case "release":
		// A developer that hits a hard blocker gives the story back: clear
		// the claim (assignee) and return the story to open so the dispatcher
		// can re-queue it. Also used by recovery. Label cleanup is best
		// effort -- a story released mid-flight may carry neither label.
		if err := runND(projectRoot, "release", storyID, "--force"); err != nil {
			return "", err
		}
		_ = runND(projectRoot, "labels", "rm", storyID, "delivered")
		_ = runND(projectRoot, "labels", "rm", storyID, "rejected")
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}

	if err := runND(projectRoot, "doctor", "--fix"); err != nil {
		return "", err
	}

	return fmt.Sprintf("OK: %s %s using shared nd vault %s", action, storyID, vaultDir), nil
}

func Merge(projectRoot, storyID, baseBranch string) (string, error) {
	storyID = strings.TrimSpace(storyID)
	if storyID == "" {
		return "", fmt.Errorf("story id is required")
	}

	doc, err := readIssue(projectRoot, storyID)
	if err != nil {
		return "", err
	}
	if doc.Status != "closed" || !hasLabel(doc.Labels, "accepted") {
		return "", fmt.Errorf("%s is not merge-ready (status=%s, labels=%v)", storyID, doc.Status, doc.Labels)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	storyBranch := "story/" + storyID

	if err := runGit(projectRoot, "fetch", "origin"); err != nil {
		return "", err
	}
	if err := runGit(projectRoot, "checkout", baseBranch); err != nil {
		return "", err
	}
	if err := runGit(projectRoot, "pull", "origin", baseBranch); err != nil {
		return "", err
	}

	mergeTarget, err := resolveMergeTarget(projectRoot, storyBranch)
	if err != nil {
		return "", err
	}
	if err := runGit(projectRoot, "merge", "--no-ff", mergeTarget, "-m", fmt.Sprintf("merge(%s): integrate %s", storyBranch, storyID)); err != nil {
		return "", err
	}
	if err := runGit(projectRoot, "push", "origin", baseBranch); err != nil {
		return "", err
	}

	if refExists(projectRoot, "refs/remotes/origin/"+storyBranch) {
		if err := runGit(projectRoot, "push", "origin", "--delete", storyBranch); err != nil {
			return "", err
		}
	}
	if refExists(projectRoot, "refs/heads/"+storyBranch) {
		if err := runGit(projectRoot, "branch", "-D", storyBranch); err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("OK: merged %s into %s and retired branch %s", storyID, baseBranch, storyBranch), nil
}

func VerifyDelivery(projectRoot, storyID string) (*DeliveryReport, error) {
	if _, err := ndvault.Ensure(projectRoot); err != nil {
		return nil, fmt.Errorf("ensure nd vault: %w", err)
	}

	content, err := issueContent(projectRoot, storyID)
	if err != nil {
		return nil, err
	}

	doc, err := readIssue(projectRoot, storyID)
	if err != nil {
		return nil, err
	}

	report := &DeliveryReport{StoryID: storyID}
	report.add("label:delivered", hasLabel(doc.Labels, "delivered"), "missing 'delivered' label")

	status, eof := validateAuthoritativeContract(content)
	report.add("nd_contract:last_block", status == "delivered", "authoritative contract is not delivered")
	report.add("nd_contract:eof", eof, "authoritative contract is not at EOF")

	report.add("notes:implementation_evidence", regexp.MustCompile(`(?m)^## Implementation Evidence$`).MatchString(content), "missing '## Implementation Evidence' heading")
	report.add("notes:ci_test_results", regexp.MustCompile(`(?m)^### (CI/Test Results|Test Results)$`).MatchString(content), "missing CI/Test Results section")
	report.add("notes:commands_run", regexp.MustCompile(`(?m)^(Commands run:|commands run:)`).MatchString(content), "missing 'Commands run:' list")
	report.add("notes:summary", regexp.MustCompile(`(?m)^Summary:`).MatchString(content), "missing 'Summary:' line")
	report.add("notes:commit_sha", regexp.MustCompile(`(?m)SHA: [0-9a-fA-F]{7,40}`).MatchString(content), "missing commit SHA")
	report.add("proof:ac_items", regexp.MustCompile(`(?m)(^\[x\] AC|^### AC Verification$)`).MatchString(content), "missing AC verification (checklist or table)")

	return report, nil
}

func (r *DeliveryReport) add(name string, ok bool, failMsg string) {
	msg := ""
	if !ok {
		msg = failMsg
		r.Failed++
	} else {
		r.Passed++
	}
	r.Checks = append(r.Checks, CheckResult{Name: name, Passed: ok, Message: msg})
}

func (r *DeliveryReport) FormatText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Verifying delivery proof for %s\n", r.StoryID)
	b.WriteString("---\n")
	for _, check := range r.Checks {
		if check.Passed {
			fmt.Fprintf(&b, "[OK]   %s\n", check.Name)
		} else {
			fmt.Fprintf(&b, "[FAIL] %s -- %s\n", check.Name, check.Message)
		}
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "Passed: %d, Failed: %d\n", r.Passed, r.Failed)
	if r.Failed == 0 {
		b.WriteString("[PASS] Delivery proof looks complete enough for PM review.\n")
	} else {
		b.WriteString("[FAIL] Delivery proof is incomplete; expect PM rejection until fixed.\n")
	}
	return b.String()
}

func (r *DeliveryReport) FormatJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func appendContract(projectRoot, storyID, status, evidence, proof string) error {
	block := fmt.Sprintf(`

## nd_contract
status: %s

### evidence
- %s

### proof
- %s
`, status, evidence, proof)
	return runND(projectRoot, "update", storyID, "--append-notes", block)
}

func outputND(projectRoot string, args ...string) ([]byte, error) {
	ndArgs, err := withVault(projectRoot, args...)
	if err != nil {
		return nil, err
	}
	cmd := execCommand("nd", ndArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("nd %s: %s", strings.Join(ndArgs, " "), msg)
		}
		return nil, fmt.Errorf("nd %s: %w", strings.Join(ndArgs, " "), err)
	}
	return out, nil
}

func runND(projectRoot string, args ...string) error {
	_, err := outputND(projectRoot, args...)
	return err
}

func withVault(projectRoot string, args ...string) ([]string, error) {
	vaultDir, err := ndvault.Ensure(projectRoot)
	if err != nil {
		return nil, err
	}
	return append([]string{"--vault", vaultDir}, args...), nil
}

func runGit(projectRoot string, args ...string) error {
	cmd := execCommand("git", args...)
	cmd.Dir = projectRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resolveMergeTarget(projectRoot, storyBranch string) (string, error) {
	switch {
	case refExists(projectRoot, "refs/remotes/origin/"+storyBranch):
		return "origin/" + storyBranch, nil
	case refExists(projectRoot, "refs/heads/"+storyBranch):
		return storyBranch, nil
	default:
		return "", fmt.Errorf("could not find local or remote branch %s", storyBranch)
	}
}

func refExists(projectRoot, ref string) bool {
	cmd := execCommand("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = projectRoot
	return cmd.Run() == nil
}

func readIssue(projectRoot, storyID string) (*issueDocument, error) {
	if labels := guard.ReadIssueLabels(projectRoot, storyID); labels != nil {
		return &issueDocument{
			Labels: labels,
			Status: guard.ReadIssueStatus(projectRoot, storyID),
			Parent: guard.ReadIssueParent(projectRoot, storyID),
		}, nil
	}
	return nil, fmt.Errorf("story not found: %s", storyID)
}

func issueContent(projectRoot, storyID string) (string, error) {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return "", err
	}
	path := filepath.Join(vaultDir, "issues", storyID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read issue %s: %w", storyID, err)
	}
	return string(data), nil
}

var contractStatusRe = regexp.MustCompile(`(?m)^status:\s*(\w+)`)

// ndManagedTrailingSections are the bookkeeping sections nd always emits as the
// canonical trailing sections of every issue, AFTER the notes/contract area.
// They are legitimate content and may follow the authoritative delivered
// contract in any order or subset without disqualifying it as "at EOF".
var ndManagedTrailingSections = map[string]bool{
	"## History":  true,
	"## Links":    true,
	"## Comments": true,
}

func validateAuthoritativeContract(content string) (status string, atEOF bool) {
	start := strings.LastIndex(content, "\n## nd_contract\n")
	if strings.HasPrefix(content, "## nd_contract\n") {
		start = 0
	}
	if start == -1 {
		return "", false
	}

	bodyStart := start
	if bodyStart > 0 {
		bodyStart++
	}
	nextHeader := strings.Index(content[bodyStart+len("## nd_contract\n"):], "\n## ")
	end := len(content)
	if nextHeader >= 0 {
		end = bodyStart + len("## nd_contract\n") + nextHeader + 1
	}

	body := content[bodyStart:end]
	if statusMatch := contractStatusRe.FindStringSubmatch(body); len(statusMatch) == 2 {
		status = statusMatch[1]
	}

	// The contract is "at EOF" when it is the literal last block, OR when
	// everything that follows it consists solely of nd-managed bookkeeping
	// sections. Any other "## " section after the contract is unexpected
	// content and keeps the contract from being at EOF.
	atEOF = strings.TrimSpace(content) == strings.TrimSpace(content[:end])
	if !atEOF && onlyNDManagedSectionsFollow(content[end:]) {
		atEOF = true
	}
	return status, atEOF
}

// onlyNDManagedSectionsFollow reports whether every "## " heading in the
// trailing region is one of nd's managed bookkeeping sections. A region with no
// "## " heading at all (e.g. blank lines only) trivially qualifies.
func onlyNDManagedSectionsFollow(trailing string) bool {
	for _, line := range strings.Split(trailing, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		if !ndManagedTrailingSections[strings.TrimRight(line, " \t")] {
			return false
		}
	}
	return true
}

// rejectedContractMarker is the fingerprint appendContract leaves on every
// rejection; the issue body accumulates one per reject, forming an
// append-only record.
const rejectedContractMarker = "## nd_contract\nstatus: rejected"

// rejectionCount returns the number of PM rejections already recorded on a
// story. Counter labels (rejected-x2, rejected-x3) are the primary record;
// because `pvg story deliver` strips the plain rejected label, the FIRST
// rejection's only durable trace after a redelivery is its appended
// nd_contract block, so the append-only contract history backs the count
// whenever it exceeds what the labels say. Deterministic: both sources are
// written exclusively by this package's reject path.
func rejectionCount(projectRoot, storyID string) int {
	count := 0
	if doc, err := readIssue(projectRoot, storyID); err == nil {
		for _, label := range doc.Labels {
			lower := strings.ToLower(label)
			if lower == "rejected" && count < 1 {
				count = 1
				continue
			}
			if rest, ok := strings.CutPrefix(lower, "rejected-x"); ok {
				if n, aerr := strconv.Atoi(rest); aerr == nil && n > count {
					count = n
				}
			}
		}
	}
	if content, err := issueContent(projectRoot, storyID); err == nil {
		if n := strings.Count(content, rejectedContractMarker); n > count {
			count = n
		}
	}
	return count
}

func hasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, target) {
			return true
		}
	}
	return false
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func today() string {
	return time.Now().Format("2006-01-02")
}
