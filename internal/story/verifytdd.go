package story

import (
	"fmt"
	"strings"
)

// VerifyTDDOptions configures the hard-TDD tamper guard. The guard enforces the
// structural lock at the heart of hard-TDD: test files may only be edited in a
// RED commit (where the failing tests are authored) or in a commit explicitly
// authorized to touch tests during GREEN. Any other commit that edits a test
// file is weakening tests to pass implementation and is a violation.
type VerifyTDDOptions struct {
	// Range is an explicit "<base>..<tip>" git range. When set it takes
	// precedence over Base.
	Range string
	// Base is a ref (branch/sha) to compute merge-base(Base, HEAD)..HEAD when
	// Range is empty -- typically the epic branch or origin/main.
	Base string
	// TestGlobs are path substrings that identify test files. Empty uses the
	// multi-language default set.
	TestGlobs []string
	// RedMarker is the commit-message token marking a RED (tests-authored)
	// commit. Empty uses "tdd-red".
	RedMarker string
	// AuthzMarker is the commit-message token authorizing a test edit during
	// GREEN. Empty uses "[test-edit-authorized]".
	AuthzMarker string
}

// VerifyTDDResult is the structured outcome of a guard run.
type VerifyTDDResult struct {
	Range       string         `json:"range"`
	Commits     int            `json:"commits_checked"`
	MergesSkip  int            `json:"merge_commits_skipped"`
	RedMarker   string         `json:"red_marker"`
	AuthzMarker string         `json:"authz_marker"`
	Violations  []TDDViolation `json:"violations"`
}

// TDDViolation records a single non-RED, non-authorized commit that edited a
// test file.
type TDDViolation struct {
	Commit  string   `json:"commit"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
}

// defaultTestGlobs identifies test files across the languages Paivot projects
// commonly use. Matching is by path substring -- deliberately broad, since a
// false "this is a test file" only widens the guard's protection.
var defaultTestGlobs = []string{
	"_test.go", "_test.exs", "_test.py", "test_",
	".test.ts", ".test.tsx", ".test.js", ".spec.ts", ".spec.js",
	"_spec.rb", "Test.java", "Tests.cs",
	"/test/", "/tests/", "/spec/", "/__tests__/",
}

func (o VerifyTDDOptions) withDefaults() VerifyTDDOptions {
	if len(o.TestGlobs) == 0 {
		o.TestGlobs = defaultTestGlobs
	}
	if o.RedMarker == "" {
		o.RedMarker = "tdd-red"
	}
	if o.AuthzMarker == "" {
		o.AuthzMarker = "[test-edit-authorized]"
	}
	return o
}

// VerifyTDD resolves the commit range, skips merge commits, and reports every
// non-merge commit that edited a test file without a RED or test-edit-authorized
// marker. It FAILS LOUDLY (returns an error) when the range cannot be resolved,
// rather than inspecting nothing and reporting success -- a silent pass is the
// dangerous failure mode this guard exists to prevent.
func VerifyTDD(projectRoot string, opts VerifyTDDOptions) (VerifyTDDResult, error) {
	opts = opts.withDefaults()

	rng, err := resolveTDDRange(projectRoot, opts)
	if err != nil {
		return VerifyTDDResult{}, err
	}

	result := VerifyTDDResult{
		Range:       rng,
		RedMarker:   opts.RedMarker,
		AuthzMarker: opts.AuthzMarker,
	}

	// Merge commits are skipped: a merge carries no per-commit markers of its
	// own, and its non-merge constituents (which DO carry markers) are checked
	// directly. This is what makes the guard correct on a merge-to-main range.
	if merges, mErr := gitLines(projectRoot, "rev-list", "--merges", rng); mErr == nil {
		result.MergesSkip = len(merges)
	}

	commits, err := gitLines(projectRoot, "rev-list", "--no-merges", rng)
	if err != nil {
		return result, fmt.Errorf("enumerate commits in %s: %w", rng, err)
	}
	result.Commits = len(commits)

	for _, sha := range commits {
		body, err := gitOutput(projectRoot, "show", "-s", "--format=%B", sha)
		if err != nil {
			return result, fmt.Errorf("read commit %s: %w", sha, err)
		}
		// A RED commit authors tests; an authorized commit may touch tests
		// during GREEN. Either marker exempts the commit.
		if strings.Contains(body, opts.RedMarker) || strings.Contains(body, opts.AuthzMarker) {
			continue
		}

		changes, err := gitNameStatus(projectRoot, sha)
		if err != nil {
			return result, fmt.Errorf("list files for %s: %w", sha, err)
		}
		var touched []string
		for _, ch := range changes {
			// A newly ADDED test file is an allowed GREEN addition: it cannot
			// weaken an existing RED test (which still runs and must pass), so
			// the hard-TDD rule is "add new tests freely, never touch a RED
			// test". Only edits and deletes touch the frozen RED set. Renames
			// are disabled (--no-renames below), so a renamed RED test surfaces
			// its delete side here and is still caught.
			if ch.status == "A" {
				continue
			}
			if matchesAnyGlob(ch.path, opts.TestGlobs) {
				touched = append(touched, ch.path)
			}
		}
		if len(touched) > 0 {
			result.Violations = append(result.Violations, TDDViolation{
				Commit:  shortSHA(sha),
				Subject: firstLine(body),
				Files:   touched,
			})
		}
	}

	return result, nil
}

// resolveTDDRange determines the commit range to inspect, failing loudly when
// it cannot. Precedence: explicit Range, then merge-base(Base, HEAD)..HEAD.
func resolveTDDRange(projectRoot string, opts VerifyTDDOptions) (string, error) {
	if opts.Range != "" {
		if err := gitRangeResolves(projectRoot, opts.Range); err != nil {
			return "", fmt.Errorf("--range %q does not resolve in this checkout: %w", opts.Range, err)
		}
		return opts.Range, nil
	}
	if opts.Base == "" {
		return "", fmt.Errorf("cannot resolve a commit range: pass --range <base>..HEAD, " +
			"or --base <epic-branch-or-ref> (or set $TDD_BASE) to merge-base against HEAD. " +
			"Refusing to inspect nothing and report success")
	}
	mb, err := gitOutput(projectRoot, "merge-base", opts.Base, "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot merge-base HEAD against base %q (is it fetched?): %w", opts.Base, err)
	}
	mb = strings.TrimSpace(mb)
	if mb == "" {
		return "", fmt.Errorf("empty merge-base between %q and HEAD", opts.Base)
	}
	return mb + "..HEAD", nil
}

func matchesAnyGlob(path string, globs []string) bool {
	if path == "" {
		return false
	}
	for _, g := range globs {
		if strings.Contains(path, g) {
			return true
		}
	}
	return false
}

// gitRangeResolves returns nil when the range is valid (even if it selects zero
// commits) and an error when an endpoint cannot be resolved -- e.g. HEAD~1..HEAD
// in a single-commit worktree checkout.
func gitRangeResolves(projectRoot, rng string) error {
	_, err := gitOutput(projectRoot, "rev-list", "-n", "1", rng)
	return err
}

func gitOutput(projectRoot string, args ...string) (string, error) {
	full := append([]string{"-C", projectRoot}, args...)
	out, err := execCommand("git", full...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func gitLines(projectRoot string, args ...string) ([]string, error) {
	out, err := gitOutput(projectRoot, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, nil
}

// fileChange pairs a file path with its single-character git change status
// (A=added, M=modified, D=deleted).
type fileChange struct {
	status string
	path   string
}

// gitNameStatus returns the per-file change status for a single commit. It uses
// --no-renames so a rename never collapses into one R row: the old path shows as
// a delete (a removal from the frozen RED set, which the guard must catch) and
// the new path as a pure add (which a genuinely new test file also is). Paths
// are tab-delimited from the status, so spaces in paths survive.
func gitNameStatus(projectRoot, sha string) ([]fileChange, error) {
	lines, err := gitLines(projectRoot, "show", "--no-renames", "--name-status", "--format=", sha)
	if err != nil {
		return nil, err
	}
	var changes []fileChange
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		status := strings.TrimSpace(parts[0])
		if status == "" {
			continue
		}
		changes = append(changes, fileChange{status: status[:1], path: parts[1]})
	}
	return changes, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// FormatText renders a human-readable guard summary.
func (r VerifyTDDResult) FormatText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[hard-tdd] range %s: checked %d commit(s), skipped %d merge(s)\n",
		r.Range, r.Commits, r.MergesSkip)
	if len(r.Violations) == 0 {
		fmt.Fprintf(&b, "[hard-tdd] PASS: no unauthorized test edits\n")
		return b.String()
	}
	fmt.Fprintf(&b, "[hard-tdd] FAIL: %d commit(s) edited tests without a %q or %q marker:\n",
		len(r.Violations), r.RedMarker, r.AuthzMarker)
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "  %s %s\n", v.Commit, v.Subject)
		for _, f := range v.Files {
			fmt.Fprintf(&b, "      %s\n", f)
		}
	}
	return b.String()
}
