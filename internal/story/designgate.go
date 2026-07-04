package story

// The hard-TDD / machinery fusion: on a machinery-managed project, RED
// approval has a deterministic precondition (machinery's RED exit gate)
// instead of resting on PM judgment alone. The lock (verify-tdd) already
// proves tests were not weakened; this proves the locked set was derived
// from a checked spec in the first place.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/design"
	"github.com/paivot-ai/pvg/internal/settings"
)

// designRedGate enforces the RED exit preconditions for approve-red when the
// design substrate applies:
//
//  1. `machinery check` (staged gates, plus --impl when configured) is
//     green: the oracle-spec the tests derive from is itself trustworthy and
//     the test scaffolding respects the architecture.
//  2. Every oracle stable id the story references appears whole-token in a
//     test file: the story's slice of the spec is actually covered before
//     the suite locks.
//
// A story that references no stable ids only needs check green (a note in
// the returned summary says so). Projects where the substrate does not
// apply return ("", nil) and approve-red behaves exactly as before.
func designRedGate(projectRoot, storyID, storyText string) (string, error) {
	sett := settings.LoadFile(filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml"))
	cfg, applies, reason := design.Applies(projectRoot, sett["design.machinery"])
	if !applies {
		return "", nil
	}

	check := design.RunCheck(projectRoot, cfg)
	if !check.Passed {
		return "", fmt.Errorf("approve-red blocked: the design gate is red (%s), so the oracle-spec the RED tests derive from cannot be trusted.\n%s\n%s\nFix the design (or the scaffolding) and retry; --skip-design <reason> records an explicit waiver",
			reason, check.Command, check.Output)
	}

	ids, err := design.StableIDs(projectRoot, cfg)
	if err != nil {
		return "", fmt.Errorf("approve-red blocked: cannot read design oracles: %w", err)
	}
	var referenced []string
	for id := range ids {
		if design.TokenIn(id, storyText) {
			referenced = append(referenced, id)
		}
	}
	sort.Strings(referenced)
	if len(referenced) == 0 {
		return fmt.Sprintf("design gate green (%s); story references no oracle stable ids, id-coverage not applicable", reason), nil
	}

	missing, err := idsMissingFromTests(projectRoot, referenced)
	if err != nil {
		return "", fmt.Errorf("approve-red blocked: cannot scan test files: %w", err)
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("approve-red blocked: story %s references %d oracle stable id(s) but %d have no test carrying the id whole-token: %s.\nA missing id is a missing test; derive it from the oracle row before the suite locks (--skip-design <reason> records an explicit waiver)",
			storyID, len(referenced), len(missing), strings.Join(missing, ", "))
	}
	return fmt.Sprintf("design gate green (%s); %d oracle stable id(s) covered by tests", reason, len(referenced)), nil
}

// idsMissingFromTests scans the project's test files (the verify-tdd glob
// set) and returns the ids that appear in none of them.
func idsMissingFromTests(projectRoot string, ids []string) ([]string, error) {
	remaining := map[string]bool{}
	for _, id := range ids {
		remaining[id] = true
	}
	err := filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == ".vault" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(remaining) == 0 {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(projectRoot, path)
		if rerr != nil {
			return nil
		}
		if !isTestPath(filepath.ToSlash(rel)) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		text := string(data)
		for id := range remaining {
			if design.TokenIn(id, text) {
				delete(remaining, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0, len(remaining))
	for id := range remaining {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	return missing, nil
}

// isTestPath applies the verify-tdd test-glob convention (path substrings).
func isTestPath(rel string) bool {
	withSlash := "/" + rel
	for _, g := range defaultTestGlobs {
		if strings.Contains(withSlash, g) {
			return true
		}
	}
	return false
}
