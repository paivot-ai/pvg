package verify

// Mock scan for integration and e2e tests.
//
// Paivot's testing rule: unit tests may mock, integration and e2e tests may
// not (they exercise real dependencies or they prove nothing). The Anchor
// used to grep for a Python/JS-centric vocabulary by hand; this makes the
// scan deterministic and multi-language, Elixir included (Mox, Mimic, meck,
// Bypass), so the milestone review and the epic gate can run it as a
// command and fail on any hit.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MockHit is one mock-vocabulary occurrence in an integration or e2e test.
type MockHit struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Pattern string `json:"pattern"` // the vocabulary family matched
	Context string `json:"context"` // the line content (trimmed)
}

// MockResult is the output of a mock scan.
type MockResult struct {
	Passed       bool      `json:"passed"`
	FilesScanned int       `json:"files_scanned"`
	Hits         []MockHit `json:"hits"`
}

// mockPattern is one vocabulary family: the regex and the family label.
type mockPattern struct {
	re    *regexp.Regexp
	label string
}

// mockPatterns covers the mocking vocabulary of the stacks Paivot projects
// use. Each pattern is scoped to an identifier or call shape so ordinary
// words ("mock data" in prose) do not trip it; the test-path filter below
// keeps unit tests out of the scan entirely.
var mockPatterns = []mockPattern{
	// Elixir
	{regexp.MustCompile(`\bMox\b`), "elixir: Mox"},
	{regexp.MustCompile(`\bMimic\b`), "elixir: Mimic"},
	{regexp.MustCompile(`:meck\b`), "elixir: meck"},
	{regexp.MustCompile(`\bdefmock\b`), "elixir: defmock"},
	{regexp.MustCompile(`\b(?:expect|stub|stub_with|verify_on_exit!|set_mox_global|set_mox_private)\(`), "elixir: Mox expect/stub"},
	{regexp.MustCompile(`\bBypass\.open\b`), "elixir: Bypass"},
	// Python
	{regexp.MustCompile(`\bunittest\.mock\b|\bfrom\s+mock\s+import\b|\bimport\s+mock\b`), "python: unittest.mock"},
	{regexp.MustCompile(`\bMagicMock\b|\bAsyncMock\b`), "python: MagicMock"},
	{regexp.MustCompile(`@(?:mock\.)?patch\b|\bmock\.patch\b|\bwith\s+patch\(`), "python: patch"},
	{regexp.MustCompile(`\bmonkeypatch\b|\bmocker\b|\bpytest_mock\b`), "python: monkeypatch/mocker"},
	{regexp.MustCompile(`\bresponses\.activate\b|\brequests_mock\b|\bhttpretty\b`), "python: HTTP mocking"},
	// JavaScript / TypeScript
	{regexp.MustCompile(`\bjest\.(?:fn|mock|spyOn|doMock)\(`), "js: jest mock"},
	{regexp.MustCompile(`\bvi\.(?:fn|mock|spyOn|doMock)\(`), "js: vitest mock"},
	{regexp.MustCompile(`\bsinon\b`), "js: sinon"},
	{regexp.MustCompile(`\bnock\(`), "js: nock"},
	{regexp.MustCompile(`\bsetupServer\(|\bfrom\s+['"]msw`), "js: msw"},
	// Go
	{regexp.MustCompile(`github\.com/golang/mock|go\.uber\.org/mock|github\.com/stretchr/testify/mock|github\.com/vektra/mockery`), "go: gomock/testify mock/mockery"},
	// Ruby
	{regexp.MustCompile(`\binstance_double\(|\bclass_double\(|\bdouble\(`), "ruby: RSpec double"},
	{regexp.MustCompile(`\ballow\(.*\)\.to\s+receive\b|\bexpect\(.*\)\.to\s+receive\b`), "ruby: RSpec receive"},
	{regexp.MustCompile(`\bWebMock\b|\bVCR\.use_cassette\b`), "ruby: WebMock/VCR"},
	// Java / Kotlin
	{regexp.MustCompile(`\bMockito\b|@Mock\b|@MockBean\b|\bmockk\(|\bWireMock\b`), "jvm: Mockito/mockk/WireMock"},
}

// integrationDirs are the path segments that mark a test as integration or
// e2e (case-insensitive).
var integrationDirs = map[string]bool{
	"integration": true, "integrations": true,
	"e2e": true, "end-to-end": true, "end_to_end": true,
	"acceptance": true, "system": true, "features": true,
}

// integrationFileRe marks a test file as integration/e2e by name.
var integrationFileRe = regexp.MustCompile(`(?i)(integration|e2e|end[-_]to[-_]end|acceptance)`)

// IsIntegrationTestPath reports whether path names an integration or e2e
// test: a test file under an integration/e2e directory, or a test file
// whose name carries the integration/e2e marker.
func IsIntegrationTestPath(path string) bool {
	if !isTestFile(path) && !isTestPathSubstring(path) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(path)), "/") {
		if integrationDirs[strings.ToLower(part)] {
			return true
		}
	}
	return integrationFileRe.MatchString(filepath.Base(path))
}

// isTestPathSubstring catches the test-file shapes isTestFile's name
// patterns do not (Elixir _test.exs, Go e2e files outside test dirs).
func isTestPathSubstring(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(base, "_test.exs") || strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, "_spec.rb") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

// CheckMocks scans the given paths (files or directories; "." when empty)
// for mock vocabulary inside integration and e2e test files.
func CheckMocks(paths []string) (*MockResult, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	result := &MockResult{Passed: true}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("cannot access %s: %w", p, err)
		}
		if !info.IsDir() {
			if IsIntegrationTestPath(p) {
				scanMockFile(p, result)
			}
			continue
		}
		err = filepath.Walk(p, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				if skipDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !sourceExtensions[filepath.Ext(path)] || !IsIntegrationTestPath(path) {
				return nil
			}
			scanMockFile(path, result)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", p, err)
		}
	}
	result.Passed = len(result.Hits) == 0
	return result, nil
}

func scanMockFile(path string, result *MockResult) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	result.FilesScanned++
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, p := range mockPatterns {
			if p.re.MatchString(line) {
				result.Hits = append(result.Hits, MockHit{
					File:    path,
					Line:    lineNum,
					Pattern: p.label,
					Context: truncate(strings.TrimSpace(line), 120),
				})
				break
			}
		}
	}
}

// FormatMocksText renders a mock scan report.
func FormatMocksText(r *MockResult) string {
	var sb strings.Builder
	if r.Passed {
		fmt.Fprintf(&sb, "MOCK CHECK: PASSED (%d integration/e2e test files scanned, 0 mock usages)\n", r.FilesScanned)
		if r.FilesScanned == 0 {
			sb.WriteString("  No integration or e2e test files found under the scanned paths -- pair this with --check-e2e; an empty scan proves nothing.\n")
		}
		return sb.String()
	}
	fmt.Fprintf(&sb, "MOCK CHECK: FAILED (%d mock usages in %d integration/e2e test files scanned)\n", len(r.Hits), r.FilesScanned)
	for _, h := range r.Hits {
		fmt.Fprintf(&sb, "  %s:%d [%s] %s\n", h.File, h.Line, h.Pattern, h.Context)
	}
	sb.WriteString("  Integration and e2e tests must exercise real dependencies; move mocks to unit tests or replace them with the real service.\n")
	return sb.String()
}
