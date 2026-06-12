package gates

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// complexityHit is one function's cyclomatic complexity, tool-agnostic.
type complexityHit struct {
	Path   string
	Symbol string
	Line   int
	CCN    int
}

// runComplexity orchestrates the complexity metric. It prefers lizard
// (multi-language); when lizard is absent it falls back per-language to
// gocyclo (.go) and radon (.py). If no complexity tool is available for the
// files in scope, it returns a Skipped note and no findings.
//
// Returns (findings, skipNote). skipNote is "" when a tool ran.
func runComplexity(files []string, mode string, warnCC, blockCC int) ([]Finding, string) {
	if len(files) == 0 {
		return nil, ""
	}

	// Prefer lizard across all files.
	if _, err := lookPath("lizard"); err == nil {
		hits, err := lizardHits(files)
		if err != nil {
			return nil, fmt.Sprintf("complexity: lizard failed (%v)", err)
		}
		return complexityFindings(hits, mode, warnCC, blockCC), ""
	}

	// Fall back per-language.
	goFiles := filterExt(files, ".go")
	pyFiles := filterExt(files, ".py")

	var hits []complexityHit
	ran := false

	if len(goFiles) > 0 {
		if _, err := lookPath("gocyclo"); err == nil {
			h, err := gocycloHits(goFiles)
			if err != nil {
				return nil, fmt.Sprintf("complexity: gocyclo failed (%v)", err)
			}
			hits = append(hits, h...)
			ran = true
		}
	}

	if len(pyFiles) > 0 {
		if _, err := lookPath("radon"); err == nil {
			h, err := radonHits(pyFiles)
			if err != nil {
				return nil, fmt.Sprintf("complexity: radon failed (%v)", err)
			}
			hits = append(hits, h...)
			ran = true
		}
	}

	if !ran {
		// Lead with the recommended multi-language tool and its install hint,
		// then point at the single-language fallbacks that would cover the
		// files actually in scope.
		note := fmt.Sprintf("complexity: lizard not found (%s)", InstallHint("lizard"))
		var fallbacks []string
		if len(goFiles) > 0 {
			fallbacks = append(fallbacks, fmt.Sprintf("gocyclo for Go (%s)", InstallHint("gocyclo")))
		}
		if len(pyFiles) > 0 {
			fallbacks = append(fallbacks, fmt.Sprintf("radon for Python (%s)", InstallHint("radon")))
		}
		if len(fallbacks) > 0 {
			note += " -- or " + strings.Join(fallbacks, ", ")
		}
		return nil, note
	}

	return complexityFindings(hits, mode, warnCC, blockCC), ""
}

// complexityFindings applies the warn/block bands to complexity hits. In warn
// mode no block finding is ever produced, even above blockCC.
func complexityFindings(hits []complexityHit, mode string, warnCC, blockCC int) []Finding {
	var findings []Finding
	for _, h := range hits {
		switch {
		case mode == "block" && h.CCN >= blockCC:
			findings = append(findings, complexityFinding(h, blockCC, "block"))
		case h.CCN >= warnCC:
			findings = append(findings, complexityFinding(h, warnCC, "warn"))
		}
	}
	return findings
}

func complexityFinding(h complexityHit, threshold int, severity string) Finding {
	detail := fmt.Sprintf("CCN %d", h.CCN)
	if h.Line > 0 {
		detail = fmt.Sprintf("CCN %d at line %d", h.CCN, h.Line)
	}
	return Finding{
		Metric:    "complexity",
		Path:      h.Path,
		Symbol:    h.Symbol,
		Value:     h.CCN,
		Threshold: threshold,
		Severity:  severity,
		Detail:    detail,
	}
}

// lizardHits parses `lizard --csv <files>`. The CSV columns are:
// NLOC,CCN,token,param,length,location,file,function,long_name,start,end
// (no header row). We read CCN (col 1), function (col 7), file (col 6),
// start line (col 9).
func lizardHits(files []string) ([]complexityHit, error) {
	out, err := runTool("lizard", append([]string{"--csv"}, files...)...)
	if err != nil {
		return nil, err
	}
	return parseLizardCSV(out)
}

func parseLizardCSV(out string) ([]complexityHit, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1 // tolerate ragged rows
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var hits []complexityHit
	for _, rec := range records {
		if len(rec) < 8 {
			continue
		}
		ccn, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue // header or malformed row
		}
		line := 0
		if len(rec) >= 10 {
			line, _ = strconv.Atoi(strings.TrimSpace(rec[9]))
		}
		hits = append(hits, complexityHit{
			Path:   strings.TrimSpace(rec[6]),
			Symbol: strings.TrimSpace(rec[7]),
			Line:   line,
			CCN:    ccn,
		})
	}
	return hits, nil
}

// gocycloHits parses `gocyclo <files>`. Each line is:
// "N pkg.Func file:line" where N is the cyclomatic complexity.
func gocycloHits(files []string) ([]complexityHit, error) {
	out, err := runTool("gocyclo", files...)
	if err != nil {
		return nil, err
	}
	return parseGocyclo(out), nil
}

func parseGocyclo(out string) []complexityHit {
	var hits []complexityHit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ccn, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		symbol := fields[1] + " " + fields[2] // "pkg.Func"
		// gocyclo prints "N pkg func file:line"; fields[1]=pkg, fields[2]=func.
		// Be defensive: the last field is file:line.
		loc := fields[len(fields)-1]
		path := loc
		lineNum := 0
		if idx := strings.LastIndex(loc, ":"); idx > 0 {
			path = loc[:idx]
			lineNum, _ = strconv.Atoi(loc[idx+1:])
		}
		hits = append(hits, complexityHit{
			Path:   path,
			Symbol: strings.TrimSpace(symbol),
			Line:   lineNum,
			CCN:    ccn,
		})
	}
	return hits
}

// radonHits parses `radon cc -j <files>`. The JSON maps file path to a list of
// blocks, each with a "complexity" int, "name", and "lineno".
func radonHits(files []string) ([]complexityHit, error) {
	out, err := runTool("radon", append([]string{"cc", "-j"}, files...)...)
	if err != nil {
		return nil, err
	}
	return parseRadonJSON(out)
}

type radonBlock struct {
	Name       string `json:"name"`
	Classname  string `json:"classname"`
	Complexity int    `json:"complexity"`
	LineNo     int    `json:"lineno"`
}

func parseRadonJSON(out string) ([]complexityHit, error) {
	var report map[string][]radonBlock
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		return nil, err
	}
	var hits []complexityHit
	for path, blocks := range report {
		for _, b := range blocks {
			name := b.Name
			if b.Classname != "" {
				name = b.Classname + "." + b.Name
			}
			hits = append(hits, complexityHit{
				Path:   path,
				Symbol: name,
				Line:   b.LineNo,
				CCN:    b.Complexity,
			})
		}
	}
	return hits, nil
}

// filterExt returns files whose extension equals ext (case-insensitive).
func filterExt(files []string, ext string) []string {
	var out []string
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f), ext) {
			out = append(out, f)
		}
	}
	return out
}
