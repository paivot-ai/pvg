package gates

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstallHint(t *testing.T) {
	cases := map[string]string{
		"lizard":  "pip install lizard",
		"jscpd":   "npm install -g jscpd",
		"gocyclo": "go install github.com/fzipp/gocyclo/cmd/gocyclo@latest",
		"radon":   "pip install radon",
		"unknown": "",
	}
	for name, want := range cases {
		if got := InstallHint(name); got != want {
			t.Errorf("InstallHint(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAnalyzers_RecommendedSet(t *testing.T) {
	rec := map[string]bool{}
	for _, a := range Analyzers {
		if a.Recommended {
			rec[a.Name] = true
		}
	}
	if !rec["lizard"] || !rec["jscpd"] {
		t.Fatalf("lizard and jscpd must be recommended, got %v", rec)
	}
	if rec["gocyclo"] || rec["radon"] {
		t.Fatalf("fallbacks must not be recommended, got %v", rec)
	}
	// The radon row must carry the apt caveat note.
	for _, a := range Analyzers {
		if a.Name == "radon" && !strings.Contains(a.Note, "apt install python3-radon") {
			t.Errorf("radon note must mention the apt caveat, got %q", a.Note)
		}
	}
}

func TestMissingRecommended(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()

	// Only lizard present -> jscpd missing.
	lookPath = func(name string) (string, error) {
		if name == "lizard" {
			return "/usr/bin/lizard", nil
		}
		return "", exec.ErrNotFound
	}
	missing := MissingRecommended()
	if len(missing) != 1 || missing[0].Name != "jscpd" {
		t.Fatalf("expected only jscpd missing, got %+v", missing)
	}

	// Both present -> none missing.
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if m := MissingRecommended(); len(m) != 0 {
		t.Fatalf("expected nothing missing, got %+v", m)
	}

	// Both absent -> both missing, fallbacks never reported.
	lookPath = func(name string) (string, error) { return "", exec.ErrNotFound }
	missing = MissingRecommended()
	names := map[string]bool{}
	for _, a := range missing {
		names[a.Name] = true
	}
	if len(missing) != 2 || !names["lizard"] || !names["jscpd"] {
		t.Fatalf("expected lizard and jscpd missing, got %+v", missing)
	}
}

func TestSkipMessages_ContainInstallHints(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()
	lookPath = func(name string) (string, error) { return "", exec.ErrNotFound }

	// Complexity over a Go + Python scope: lizard hint plus both fallbacks.
	_, skip := runComplexity([]string{"a.go", "b.py"}, "block", 15, 30)
	for _, want := range []string{
		"complexity: lizard not found",
		"pip install lizard",
		"gocyclo for Go",
		"go install github.com/fzipp/gocyclo/cmd/gocyclo@latest",
		"radon for Python",
		"pip install radon",
	} {
		if !strings.Contains(skip, want) {
			t.Errorf("complexity skip missing %q in %q", want, skip)
		}
	}

	// Duplication: jscpd hint.
	_, dskip := runDuplication([]string{"."}, "block", 10, 50)
	for _, want := range []string{"duplication: jscpd not found", "npm install -g jscpd"} {
		if !strings.Contains(dskip, want) {
			t.Errorf("duplication skip missing %q in %q", want, dskip)
		}
	}
}
