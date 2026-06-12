package gates

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// jscpdReport is the subset of jscpd's JSON report we consume.
// statistics.total.percentage is the overall duplication percentage; each
// clone in duplicates[] carries a duplicated line count.
type jscpdReport struct {
	Statistics struct {
		Total struct {
			Percentage float64 `json:"percentage"`
		} `json:"total"`
	} `json:"statistics"`
	Duplicates []jscpdClone `json:"duplicates"`
}

// jscpdClone is one detected copy-paste clone. jscpd reports the duplicated
// span under "lines" (and sometimes nests start/end under firstFile); we read
// the top-level "lines" count, falling back to fragment line spans.
type jscpdClone struct {
	Lines     int `json:"lines"`
	FirstFile struct {
		Name  string `json:"name"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	} `json:"firstFile"`
}

// duplicatedLines returns the clone's duplicated line count, preferring the
// explicit "lines" field and falling back to (end - start) when absent.
func (c jscpdClone) duplicatedLines() int {
	if c.Lines > 0 {
		return c.Lines
	}
	if c.FirstFile.End > c.FirstFile.Start {
		return c.FirstFile.End - c.FirstFile.Start
	}
	return 0
}

// runDuplication orchestrates the duplication metric using jscpd. It runs
// jscpd to a JSON report in a temp dir, reads jscpd-report.json, and emits a
// finding when total percentage >= maxPct OR any clone has >= minLines
// duplicated lines. If jscpd is absent it returns a Skipped note.
//
// Returns (findings, skipNote). skipNote is "" when jscpd ran.
func runDuplication(paths []string, mode string, maxPct float64, minLines int) ([]Finding, string) {
	if len(paths) == 0 {
		return nil, ""
	}
	if _, err := lookPath("jscpd"); err != nil {
		return nil, fmt.Sprintf("duplication: jscpd not found (%s)", InstallHint("jscpd"))
	}

	tmpDir, err := os.MkdirTemp("", "pvg-jscpd-")
	if err != nil {
		return nil, fmt.Sprintf("duplication: cannot create temp dir (%v)", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	args := append([]string{"--silent", "--reporters", "json", "--output", tmpDir}, paths...)
	// jscpd exits non-zero when --threshold is exceeded; we ignore its exit
	// code and judge purely from the report we read back.
	_, _ = runTool("jscpd", args...)

	report, err := readJscpdReport(filepath.Join(tmpDir, "jscpd-report.json"))
	if err != nil {
		return nil, fmt.Sprintf("duplication: jscpd report unreadable (%v)", err)
	}

	return duplicationFindings(report, mode, maxPct, minLines), ""
}

func readJscpdReport(path string) (*jscpdReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseJscpdReport(data)
}

func parseJscpdReport(data []byte) (*jscpdReport, error) {
	var report jscpdReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

// duplicationFindings applies the pct-OR-min_lines logic. A finding fires when
// the total duplication percentage >= maxPct, OR any single clone has
// >= minLines duplicated lines.
func duplicationFindings(report *jscpdReport, mode string, maxPct float64, minLines int) []Finding {
	if report == nil {
		return nil
	}
	severity := severityFor(mode)
	var findings []Finding

	pct := report.Statistics.Total.Percentage
	if pct >= maxPct {
		findings = append(findings, Finding{
			Metric:    "duplication",
			Path:      "(total)",
			Value:     int(pct + 0.5),
			Threshold: int(maxPct + 0.5),
			Severity:  severity,
			Detail:    fmt.Sprintf("%.1f%% duplicated (max %.0f%%)", pct, maxPct),
		})
	}

	for _, clone := range report.Duplicates {
		dl := clone.duplicatedLines()
		if dl >= minLines {
			path := clone.FirstFile.Name
			if path == "" {
				path = "(clone)"
			}
			findings = append(findings, Finding{
				Metric:    "duplication",
				Path:      path,
				Value:     dl,
				Threshold: minLines,
				Severity:  severity,
				Detail:    fmt.Sprintf("%d duplicated lines (min %d)", dl, minLines),
			})
		}
	}

	return findings
}
