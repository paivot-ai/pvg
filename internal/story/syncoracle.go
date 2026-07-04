package story

// sync-oracle: machinery's revision protocol says the oracle stable-id diff
// IS the affected-test list. On a Paivot project the tracker closes the
// loop: this maps that diff onto the nd backlog, so a design change becomes
// a concrete list of stories to reopen, retest, or write.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/design"
	"github.com/paivot-ai/pvg/internal/ndvault"
	"github.com/paivot-ai/pvg/internal/rtm"
)

// SyncEntry is one stable id whose transition changed between base and HEAD.
type SyncEntry struct {
	ID      string   `json:"id"`
	Change  string   `json:"change"` // added | removed | modified
	Oracle  string   `json:"oracle"` // oracle file (design-relative)
	Stories []string `json:"stories"`
}

// SyncReport is the outcome of pvg story sync-oracle.
type SyncReport struct {
	Base      string      `json:"base"`
	Entries   []SyncEntry `json:"entries"`
	Uncovered int         `json:"uncovered"` // changed ids no story references
	Stories   int         `json:"stories_checked"`
}

// syncRefPattern mirrors the git-ref allowlist used elsewhere in pvg: no
// leading '-', no shell or path metacharacters.
var syncRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/~^-]*$`)

// SyncOracle diffs the design oracles between baseRef and the working tree
// and maps every added, removed, or modified stable id onto the non-closed
// stories that reference it.
func SyncOracle(projectRoot, baseRef string) (*SyncReport, error) {
	if !syncRefPattern.MatchString(baseRef) {
		return nil, fmt.Errorf("invalid base ref %q (allowed: letters, digits, and . _ / - ~ ^, not starting with -)", baseRef)
	}
	cfg, managed := design.Load(projectRoot)
	if !managed {
		return nil, fmt.Errorf("project is not machinery-managed (no %s or %s/domain.modelith.yaml); nothing to sync", design.ConfigName, cfg.Dir)
	}

	current := map[string]map[string]string{} // oracle rel -> id -> row
	files, err := design.OracleFiles(projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			return nil, fmt.Errorf("read oracle %s: %w", f, rerr)
		}
		rel, rerr := filepath.Rel(projectRoot, f)
		if rerr != nil {
			return nil, rerr
		}
		current[filepath.ToSlash(rel)] = design.OracleRows(string(data))
	}

	base := map[string]map[string]string{}
	for _, rel := range baseOracleFiles(projectRoot, baseRef, cfg) {
		out, gerr := gitShow(projectRoot, baseRef, rel)
		if gerr != nil {
			continue // absent at base: every current id in it is "added"
		}
		base[rel] = design.OracleRows(out)
	}

	rep := &SyncReport{Base: baseRef}
	oracleSet := map[string]bool{}
	for rel := range current {
		oracleSet[rel] = true
	}
	for rel := range base {
		oracleSet[rel] = true
	}
	oracles := make([]string, 0, len(oracleSet))
	for rel := range oracleSet {
		oracles = append(oracles, rel)
	}
	sort.Strings(oracles)

	for _, rel := range oracles {
		now, then := current[rel], base[rel]
		idSet := map[string]bool{}
		for id := range now {
			idSet[id] = true
		}
		for id := range then {
			idSet[id] = true
		}
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			nowRow, inNow := now[id]
			thenRow, inThen := then[id]
			switch {
			case inNow && !inThen:
				rep.Entries = append(rep.Entries, SyncEntry{ID: id, Change: "added", Oracle: rel})
			case !inNow && inThen:
				rep.Entries = append(rep.Entries, SyncEntry{ID: id, Change: "removed", Oracle: rel})
			case nowRow != thenRow:
				rep.Entries = append(rep.Entries, SyncEntry{ID: id, Change: "modified", Oracle: rel})
			}
		}
	}
	if len(rep.Entries) == 0 {
		return rep, nil
	}

	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve nd vault: %w", err)
	}
	bodies, count, err := rtm.LoadStoryBodies(vaultDir)
	if err != nil {
		return nil, fmt.Errorf("load stories: %w", err)
	}
	rep.Stories = count
	storyIDs := make([]string, 0, len(bodies))
	for id := range bodies {
		storyIDs = append(storyIDs, id)
	}
	sort.Strings(storyIDs)
	for i := range rep.Entries {
		for _, sid := range storyIDs {
			if design.TokenIn(rep.Entries[i].ID, bodies[sid]) {
				rep.Entries[i].Stories = append(rep.Entries[i].Stories, sid)
			}
		}
		if len(rep.Entries[i].Stories) == 0 {
			rep.Uncovered++
		}
	}
	return rep, nil
}

// baseOracleFiles lists the oracle paths present at baseRef (design-relative
// to the project root, slash-separated).
func baseOracleFiles(projectRoot, baseRef string, cfg design.Config) []string {
	dir := filepath.ToSlash(filepath.Join(cfg.Dir, "machines"))
	// #nosec G702 -- baseRef validated against syncRefPattern; no shell involved
	cmd := execCommand("git", "-C", projectRoot, "ls-tree", "-r", "--name-only", baseRef, "--", dir)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".oracle.md") {
			files = append(files, line)
		}
	}
	return files
}

func gitShow(projectRoot, ref, rel string) (string, error) {
	// #nosec G702 -- ref validated against syncRefPattern; rel comes from git ls-tree/filepath.Rel
	cmd := execCommand("git", "-C", projectRoot, "show", ref+":"+rel)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// FormatSyncText renders the report for humans: what changed, and which
// stories carry each changed id (the affected-story list).
func FormatSyncText(r *SyncReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[sync-oracle] base %s: %d changed stable id(s), %d with no covering story (%d stories checked)\n",
		r.Base, len(r.Entries), r.Uncovered, r.Stories)
	if len(r.Entries) == 0 {
		b.WriteString("[sync-oracle] oracles unchanged; no stories affected\n")
		return b.String()
	}
	for _, e := range r.Entries {
		stories := strings.Join(e.Stories, ", ")
		if stories == "" {
			stories = "NO COVERING STORY"
		}
		fmt.Fprintf(&b, "  %-8s %s (%s) -> %s\n", e.Change, e.ID, e.Oracle, stories)
	}
	b.WriteString("[sync-oracle] added/modified ids need their tests re-derived from the oracle rows; removed ids mark tests to retire. Reopen or file the stories above accordingly.\n")
	return b.String()
}

// FormatSyncJSON renders the report as indented JSON.
func FormatSyncJSON(r *SyncReport) (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
