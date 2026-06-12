package gates

import (
	"path/filepath"
	"strings"
)

// parseExcludes splits a comma-separated exclude setting into trimmed,
// non-empty patterns. Each pattern is either a directory/path substring
// (e.g. "vendor/", "migrations/") or a basename glob (e.g. "*.pb.go",
// "*.min.*").
func parseExcludes(raw string) []string {
	var pats []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			pats = append(pats, p)
		}
	}
	return pats
}

// isExcluded reports whether a file path matches any exclude pattern.
//
// Matching rules:
//   - A pattern containing "/" (e.g. "vendor/", "migrations/") is treated as a
//     path substring: it matches if it appears anywhere in the slash-normalized
//     path. This catches both top-level and nested directories.
//   - A pattern without "/" containing a glob metacharacter (* ? [) is matched
//     against the file's basename via filepath.Match (e.g. "*.pb.go").
//   - A plain pattern without "/" and without metacharacters matches if it
//     equals the basename or appears as a path substring (defensive default).
func isExcluded(path string, patterns []string) bool {
	norm := filepath.ToSlash(path)
	base := filepath.Base(norm)
	for _, pat := range patterns {
		if strings.Contains(pat, "/") {
			if strings.Contains(norm, pat) {
				return true
			}
			continue
		}
		if strings.ContainsAny(pat, "*?[") {
			if ok, err := filepath.Match(pat, base); err == nil && ok {
				return true
			}
			continue
		}
		if base == pat || strings.Contains(norm, pat) {
			return true
		}
	}
	return false
}

// applyExcludes returns the subset of paths that are not excluded.
func applyExcludes(paths, patterns []string) []string {
	if len(patterns) == 0 {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if !isExcluded(p, patterns) {
			kept = append(kept, p)
		}
	}
	return kept
}
