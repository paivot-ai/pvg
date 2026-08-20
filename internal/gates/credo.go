package gates

// Elixir complexity via credo.
//
// lizard has no Elixir support, so an Elixir file handed to it is silently
// measured as zero functions: a silent pass, the one outcome the gate must
// never produce. Elixir files therefore take a separate path: credo's
// CyclomaticComplexity check when `mix` and a credo dependency are present
// (credo reports every function above its configured max_complexity, 9 by
// default, which sits below the gate's warn band), or an explicit SKIP note
// naming the bypass otherwise.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// elixirExts are the extensions credo measures.
var elixirExts = map[string]bool{".ex": true, ".exs": true}

// isElixir reports whether the file is Elixir source.
func isElixir(path string) bool {
	return elixirExts[strings.ToLower(filepath.Ext(path))]
}

// splitElixir partitions files into Elixir and everything else.
func splitElixir(files []string) (elixir, other []string) {
	for _, f := range files {
		if isElixir(f) {
			elixir = append(elixir, f)
		} else {
			other = append(other, f)
		}
	}
	return elixir, other
}

// statPath is a seam for tests.
var statPath = os.Stat

// credoAvailable reports whether the cwd is a mix project with credo: mix
// on PATH and either deps/credo or a .credo.exs in the working directory.
func credoAvailable() bool {
	if _, err := lookPath("mix"); err != nil {
		return false
	}
	for _, marker := range []string{filepath.Join("deps", "credo"), ".credo.exs"} {
		if _, err := statPath(marker); err == nil {
			return true
		}
	}
	return false
}

// credoSkipNote is the explicit bypass when credo cannot run.
func credoSkipNote(n int) string {
	return "complexity: " + strconv.Itoa(n) + " Elixir file(s) not measured -- lizard has no Elixir support; add credo to mix.exs (deps/credo) so `mix credo` measures cyclomatic complexity, or run the project's verification workflow (e.g. /phx:verify) and record its result"
}

// credoIssue is one entry of `mix credo list --format json`.
type credoIssue struct {
	Check    string `json:"check"`
	Message  string `json:"message"`
	Filename string `json:"filename"`
	LineNo   int    `json:"line_no"`
	Trigger  string `json:"trigger"`
}

type credoReport struct {
	Issues []credoIssue `json:"issues"`
}

// credoCCRe extracts the complexity from credo's message:
// "Function body is too complex (CC is 12, max is 9)."
var credoCCRe = regexp.MustCompile(`CC is (\d+)`)

// credoHits runs credo's cyclomatic-complexity check over the files.
func credoHits(files []string) ([]complexityHit, error) {
	args := []string{"credo", "list", "--format", "json", "--only", "CyclomaticComplexity", "--strict"}
	args = append(args, files...)
	out, err := runTool("mix", args...)
	if err != nil {
		return nil, err
	}
	return parseCredoJSON(out)
}

// parseCredoJSON converts credo's JSON issue list into complexity hits.
// Output before the JSON object (compile noise) is skipped.
func parseCredoJSON(out string) ([]complexityHit, error) {
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, nil
	}
	var report credoReport
	if err := json.Unmarshal([]byte(out[start:]), &report); err != nil {
		return nil, err
	}
	var hits []complexityHit
	for _, issue := range report.Issues {
		if !strings.HasSuffix(issue.Check, "CyclomaticComplexity") {
			continue
		}
		m := credoCCRe.FindStringSubmatch(issue.Message)
		if m == nil {
			continue
		}
		ccn, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		hits = append(hits, complexityHit{
			Path:   issue.Filename,
			Symbol: issue.Trigger,
			Line:   issue.LineNo,
			CCN:    ccn,
		})
	}
	return hits, nil
}
