package gates

// Analyzer describes one external code-quality analyzer the gates package can
// shell out to. The registry below is the single source of truth: skip hints,
// `pvg doctor`, and the `pvg setup` nudge all derive their install commands
// and notes from this table so they never drift apart.
type Analyzer struct {
	Name        string // "lizard"
	Purpose     string // "cyclomatic complexity (multi-language)"
	Install     string // "pip install lizard"
	Note        string // "" or "Python only; Ubuntu: apt install python3-radon"
	Recommended bool   // true for the multi-language tools (lizard, jscpd)
}

// Analyzers is the canonical analyzer matrix. lizard and jscpd are the two
// recommended, multi-language tools; gocyclo and radon are single-language
// fallbacks only needed when lizard is skipped.
//
// Validated on Ubuntu: only radon ships in the apt repos (python3-radon).
// lizard (pip) and jscpd (npm) -- the recommended pair -- are NOT packaged for
// apt and must come from pip/npm, which are present on most dev machines.
var Analyzers = []Analyzer{
	{
		Name:        "lizard",
		Purpose:     "cyclomatic complexity (multi-language)",
		Install:     "pip install lizard",
		Recommended: true,
	},
	{
		Name:        "jscpd",
		Purpose:     "duplication detection (multi-language)",
		Install:     "npm install -g jscpd",
		Recommended: true,
	},
	{
		Name:    "gocyclo",
		Purpose: "cyclomatic complexity (Go fallback)",
		Install: "go install github.com/fzipp/gocyclo/cmd/gocyclo@latest",
		Note:    "Go only",
	},
	{
		Name:    "radon",
		Purpose: "cyclomatic complexity (Python fallback)",
		Install: "pip install radon",
		Note:    "Python only; Ubuntu: apt install python3-radon",
	},
}

// MissingRecommended returns the recommended analyzers (lizard, jscpd) that are
// not found on PATH. It uses the package's stubbable lookPath so tests can
// simulate any combination of present/absent tools.
func MissingRecommended() []Analyzer {
	var missing []Analyzer
	for _, a := range Analyzers {
		if !a.Recommended {
			continue
		}
		if _, err := lookPath(a.Name); err != nil {
			missing = append(missing, a)
		}
	}
	return missing
}

// InstallHint returns a short "pip install lizard"-style install command for a
// tool name, or "" when the name is not in the registry.
func InstallHint(name string) string {
	for _, a := range Analyzers {
		if a.Name == name {
			return a.Install
		}
	}
	return ""
}
