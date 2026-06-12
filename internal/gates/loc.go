package gates

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// countNonBlankLines returns the number of non-blank lines in a file. Blank
// (whitespace-only) lines do not count toward the LOC threshold.
func countNonBlankLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	n := 0
	scanner := bufio.NewScanner(f)
	// Allow long lines (minified/generated content) without erroring.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// runLOC is the built-in file-size metric. It counts non-blank lines per file
// and emits a finding when loc >= max. Severity is "block" only when mode is
// "block"; otherwise "warn". This metric needs no external tool, so it never
// produces a Skipped note.
func runLOC(files []string, mode string, max int) []Finding {
	var findings []Finding
	for _, file := range files {
		loc, err := countNonBlankLines(file)
		if err != nil {
			continue // unreadable file -- not our concern, skip silently
		}
		if loc >= max {
			findings = append(findings, Finding{
				Metric:    "file_loc",
				Path:      file,
				Value:     loc,
				Threshold: max,
				Severity:  severityFor(mode),
				Detail:    fmt.Sprintf("%d non-blank lines", loc),
			})
		}
	}
	return findings
}
