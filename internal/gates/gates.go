// Package gates provides deterministic metric quality gates on delivered
// code. It computes code metrics (cyclomatic complexity, copy-paste
// duplication, and file size) by shelling out to real analyzers, compares the
// results against configurable thresholds, and returns PASS/FAIL.
//
// gates complements -- it does not replace -- the verify package. Where verify
// scans for stub patterns and thin files, gates measures structural quality.
//
// When an analyzer tool is absent, the corresponding gate is SKIPPED and
// noted in the report; it is never a silent pass. Only "block"-severity
// findings fail the gate (Report.Blocked); "warn" findings are reported but do
// not fail.
package gates

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// execCommand and lookPath are indirected so tests can stub tool execution
// and availability without invoking real binaries.
var (
	execCommand = exec.Command
	lookPath    = exec.LookPath
)

// Finding is a single gate result for one metric on one path/symbol.
type Finding struct {
	Metric    string `json:"metric"`           // "complexity" | "duplication" | "file_loc"
	Path      string `json:"path"`             // file path (or "(total)" for aggregate duplication)
	Symbol    string `json:"symbol,omitempty"` // function/method name (complexity)
	Value     int    `json:"value"`            // measured value
	Threshold int    `json:"threshold"`        // threshold that was crossed
	Severity  string `json:"severity"`         // "warn" | "block"
	Detail    string `json:"detail,omitempty"` // human-readable detail
}

// Report is the aggregated output of a gates run.
type Report struct {
	Findings []Finding `json:"findings"`
	Skipped  []string  `json:"skipped"` // e.g. "complexity: lizard not found"
	Blocked  bool      `json:"blocked"` // true if any finding has Severity=="block"
}

// Run orchestrates all enabled metrics over the given paths, applies the
// configured excludes, and aggregates the findings into a Report. sett is the
// project settings map (keys default via settings.Default when absent).
func Run(paths []string, sett map[string]string) (*Report, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}

	excludes := parseExcludes(setting(sett, "gates.exclude"))

	// Expand directories to a concrete file list for the per-file metrics
	// (complexity falls back per-language; loc is per-file). Duplication runs
	// jscpd directly over the (possibly directory) paths but we still drop
	// excluded entries first.
	files, err := collectFiles(paths)
	if err != nil {
		return nil, err
	}
	files = applyExcludes(files, excludes)
	scopedPaths := applyExcludes(paths, excludes)

	report := &Report{}

	// Complexity
	if mode := setting(sett, "gates.complexity"); mode != "off" {
		warnCC := settingInt(sett, "gates.complexity.warn_cc")
		blockCC := settingInt(sett, "gates.complexity.block_cc")
		findings, skip := runComplexity(files, mode, warnCC, blockCC)
		report.Findings = append(report.Findings, findings...)
		if skip != "" {
			report.Skipped = append(report.Skipped, skip)
		}
	}

	// Duplication
	if mode := setting(sett, "gates.duplication"); mode != "off" {
		maxPct := settingFloat(sett, "gates.duplication.max_pct")
		minLines := settingInt(sett, "gates.duplication.min_lines")
		findings, skip := runDuplication(scopedPaths, mode, maxPct, minLines)
		report.Findings = append(report.Findings, findings...)
		if skip != "" {
			report.Skipped = append(report.Skipped, skip)
		}
	}

	// File size / LOC (built-in, never skipped)
	if mode := setting(sett, "gates.file_loc"); mode != "off" {
		max := settingInt(sett, "gates.file_loc.max")
		report.Findings = append(report.Findings, runLOC(files, mode, max)...)
	}

	sortFindings(report.Findings)
	for _, f := range report.Findings {
		if f.Severity == "block" {
			report.Blocked = true
			break
		}
	}

	return report, nil
}

// FormatText returns a human-readable report matching verify's voice.
func FormatText(r *Report) string {
	var sb strings.Builder

	for _, f := range r.Findings {
		tag := "[WARN]"
		if f.Severity == "block" {
			tag = "[BLOCK]"
		}
		line := fmt.Sprintf("%s %s %s", tag, f.Metric, f.Path)
		if f.Symbol != "" {
			line += " " + f.Symbol
		}
		line += fmt.Sprintf(" %d>%d", f.Value, f.Threshold)
		if f.Detail != "" {
			line += fmt.Sprintf("  (%s)", f.Detail)
		}
		sb.WriteString(line + "\n")
	}

	for _, s := range r.Skipped {
		fmt.Fprintf(&sb, "[SKIP] %s\n", s)
	}

	blocks, warns := 0, 0
	for _, f := range r.Findings {
		if f.Severity == "block" {
			blocks++
		} else {
			warns++
		}
	}

	if r.Blocked {
		fmt.Fprintf(&sb, "GATES: FAIL (%d block, %d warn, %d skipped)\n", blocks, warns, len(r.Skipped))
	} else {
		fmt.Fprintf(&sb, "GATES: PASS (%d warn, %d skipped)\n", warns, len(r.Skipped))
	}

	return sb.String()
}

// FormatJSON returns an indented JSON report.
func FormatJSON(r *Report) (string, error) {
	// Normalize nil slices to empty for stable machine consumption.
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Skipped == nil {
		r.Skipped = []string{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// --- helpers ---

// severityFor maps a gate mode to a finding severity. block mode yields
// "block"; everything else yields "warn".
func severityFor(mode string) string {
	if mode == "block" {
		return "block"
	}
	return "warn"
}

// runTool executes a stubbable command and returns its combined stdout. Output
// is returned even on a non-zero exit (some tools, e.g. jscpd, exit non-zero
// while still emitting valid output); the caller decides whether to surface
// the error.
func runTool(name string, args ...string) (string, error) {
	cmd := execCommand(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		// Surface the error but keep any captured stdout for the caller.
		if out != "" {
			return out, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%s: %s", name, msg)
		}
		return out, err
	}
	return out, nil
}

// sourceExtensions is the set of file extensions the per-file metrics scan
// when expanding directories. Mirrors verify's coverage.
var sourceExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".rb": true, ".java": true, ".cs": true,
	".swift": true, ".kt": true, ".ex": true, ".exs": true,
	".c": true, ".cpp": true, ".h": true, ".hpp": true,
}

// alwaysSkipDirs are never descended into during directory expansion.
var alwaysSkipDirs = map[string]bool{
	".git": true, ".svn": true, "node_modules": true, "vendor": true,
	"__pycache__": true, ".vault": true, ".claude": true,
}

// collectFiles expands the given paths into a flat list of source files.
// File paths are kept as-is (if they are source files); directories are walked.
func collectFiles(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("cannot access %s: %w", p, err)
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.Walk(p, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil // skip inaccessible entries
			}
			if fi.IsDir() {
				if alwaysSkipDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if sourceExtensions[filepath.Ext(path)] {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// sortFindings orders findings deterministically: blocks before warns, then by
// metric, path, and symbol.
func sortFindings(findings []Finding) {
	sevRank := func(s string) int {
		if s == "block" {
			return 0
		}
		return 1
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) < sevRank(b.Severity)
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Symbol < b.Symbol
	})
}

// setting returns the configured value for key, falling back to the package's
// built-in default when the map lacks it or holds an empty string.
func setting(sett map[string]string, key string) string {
	if v, ok := sett[key]; ok && v != "" {
		return v
	}
	return defaultGate(key)
}

func settingInt(sett map[string]string, key string) int {
	n, err := strconv.Atoi(setting(sett, key))
	if err != nil {
		n, _ = strconv.Atoi(defaultGate(key))
	}
	return n
}

func settingFloat(sett map[string]string, key string) float64 {
	f, err := strconv.ParseFloat(setting(sett, key), 64)
	if err != nil {
		f, _ = strconv.ParseFloat(defaultGate(key), 64)
	}
	return f
}

// defaultGate holds gates.* defaults locally so this package does not depend
// on the settings package (avoids an import cycle and keeps gates testable in
// isolation). These MUST match settings.defaults.
var gateDefaults = map[string]string{
	"gates.complexity":            "block",
	"gates.complexity.warn_cc":    "15",
	"gates.complexity.block_cc":   "30",
	"gates.duplication":           "block",
	"gates.duplication.max_pct":   "10",
	"gates.duplication.min_lines": "50",
	"gates.file_loc":              "warn",
	"gates.file_loc.max":          "400",
	"gates.exclude":               "vendor/,node_modules/,*.generated.*,*.pb.go,migrations/,*.lock,*.min.*,dist/,build/",
}

func defaultGate(key string) string {
	return gateDefaults[key]
}
