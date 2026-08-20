// Package design resolves and drives the machinery design substrate for a
// Paivot-managed project: detection of a machinery-managed repo, the
// project's .machinery.json staging config, the deterministic design gate
// (`machinery check`) that pvg gates and story transitions call, and the
// oracle stable-id parsing that the RTM and hard-TDD checks key on.
//
// Paivot's opinion sits in the `design.machinery` setting, which is strictly
// user-opt-in: `off` (default) disables the substrate everywhere, `on`
// promises it (a missing design then fails loudly), and `auto` is a
// deliberate, explicit choice to re-enable artifact detection (a repo made
// machinery-managed by .machinery.json or design/domain.modelith.yaml).
// Artifact presence alone never enables the substrate.
package design

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/settings"
)

// Overridable for tests.
var (
	execCommand = exec.Command
	lookPath    = exec.LookPath
)

// ConfigName is machinery's project-root marker and staging config.
const ConfigName = ".machinery.json"

// Config mirrors the fields of .machinery.json that pvg needs. Absent file
// with a conventional design/domain.modelith.yaml yields the defaults.
type Config struct {
	Dir   string `json:"design"` // design directory relative to the root
	Gates string `json:"gates"`  // staged --gate list ("" = machinery's default selection)
	Impl  string `json:"impl"`   // implementation dir for G4-import ("" = disabled)
	Hooks *bool  `json:"hooks"`  // machinery's own opt-out; pvg respects it
}

// Load detects whether projectRoot is machinery-managed (a .machinery.json,
// or the conventional design/domain.modelith.yaml) and returns the staging
// config. Mirrors machinery's own detection so the two tools never disagree.
func Load(projectRoot string) (Config, bool) {
	cfg := Config{Dir: "design"}
	raw, err := os.ReadFile(filepath.Join(projectRoot, ConfigName))
	if err != nil {
		if _, serr := os.Stat(filepath.Join(projectRoot, "design", "domain.modelith.yaml")); serr == nil {
			return cfg, true
		}
		return cfg, false
	}
	if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
		// a typo degrades to defaults, loudly at check time, never to "off"
		cfg = Config{Dir: "design"}
		return cfg, true
	}
	if cfg.Hooks != nil && !*cfg.Hooks {
		return cfg, false
	}
	if cfg.Dir == "" {
		cfg.Dir = "design"
	}
	return cfg, true
}

// MachinerySetting resolves the effective design.machinery value from a
// loaded settings map: the user's explicit value when set, otherwise the
// built-in default (off). Every caller of Applies must resolve the setting
// through here so an unset value means off everywhere and artifact presence
// alone never enables the substrate.
func MachinerySetting(sett map[string]string) string {
	if v := strings.TrimSpace(sett["design.machinery"]); v != "" {
		return v
	}
	return settings.Default("design.machinery")
}

// Applies resolves the design.machinery setting ("auto" | "on" | "off")
// against the project. It returns the config, whether the substrate applies,
// and a one-line reason for reports. Callers must pass the effective setting
// (see MachinerySetting): the project default is off, so an unset value
// resolves to off before it reaches here; artifact presence alone never
// enables the substrate. An explicit "auto" is a deliberate user choice to
// apply the substrate exactly when the repo is machinery-managed.
func Applies(projectRoot, setting string) (Config, bool, string) {
	cfg, managed := Load(projectRoot)
	switch strings.TrimSpace(setting) {
	case "off":
		return cfg, false, "design.machinery=off"
	case "on":
		return cfg, true, "design.machinery=on"
	default:
		if managed {
			return cfg, true, "auto: project is machinery-managed"
		}
		return cfg, false, "auto: project is not machinery-managed"
	}
}

// CheckResult is one `machinery check` run, shaped for gate reports.
type CheckResult struct {
	Passed  bool   `json:"passed"`
	Command string `json:"command"`
	Output  string `json:"output"`
	Reason  string `json:"reason,omitempty"` // why the substrate applied
}

// RunCheck executes the deterministic design gate for the project: machinery
// check on the configured design dir with the staged gate list and impl dir.
// A missing machinery binary FAILS rather than skips: an applicable design
// promise cannot be silently waived; `pvg update` installs the binary.
func RunCheck(projectRoot string, cfg Config) CheckResult {
	args := []string{"check", cfg.Dir}
	if cfg.Gates != "" {
		args = append(args, "--gate", cfg.Gates)
	}
	if cfg.Impl != "" {
		args = append(args, "--impl", cfg.Impl)
	}
	res := CheckResult{Command: "machinery " + strings.Join(args, " ")}
	if _, err := lookPath("machinery"); err != nil {
		res.Output = "machinery not found on PATH; the design gate cannot run. Install it: pvg update"
		return res
	}
	cmd := execCommand("machinery", args...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	res.Output = strings.TrimSpace(string(out))
	res.Passed = err == nil
	return res
}

// FormatText renders a CheckResult in pvg gates' report style.
func FormatText(r CheckResult) string {
	var b strings.Builder
	status := "PASS"
	if !r.Passed {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "[design] %s: %s", status, r.Command)
	if r.Reason != "" {
		fmt.Fprintf(&b, " (%s)", r.Reason)
	}
	b.WriteString("\n")
	if !r.Passed && r.Output != "" {
		for _, line := range strings.Split(r.Output, "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return b.String()
}

// --- oracle stable ids ---

// OracleDirs are the design subdirectories that carry generated oracles:
// machines/ holds the per-machine transition oracles, formal/ holds the
// relational decision tables (Policy.oracle.md, Isolation.oracle.md). Both
// carry a "stable id" column and machinery's Gt-tests covers both, so every
// pvg consumer (rtm, hard-tdd-oracle, approve-red, sync-oracle) keys on
// both: a formal oracle row is a requirement exactly like a transition row.
var OracleDirs = []string{"machines", "formal"}

// OracleFiles lists the committed oracles under the design dir (machines/
// and formal/), sorted by path.
func OracleFiles(projectRoot string, cfg Config) ([]string, error) {
	var files []string
	for _, sub := range OracleDirs {
		pattern := filepath.Join(projectRoot, filepath.FromSlash(cfg.Dir), sub, "*.oracle.md")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	return files, nil
}

// OracleDirPrefixes returns the design-relative, slash-separated oracle
// directories ("design/machines", "design/formal") for git path scoping.
func OracleDirPrefixes(cfg Config) []string {
	out := make([]string, 0, len(OracleDirs))
	for _, sub := range OracleDirs {
		out = append(out, filepath.ToSlash(filepath.Join(cfg.Dir, sub)))
	}
	return out
}

// OracleRel returns the oracle's path relative to the design dir, slash
// separated ("machines/Deal.oracle.md", "formal/Policy.oracle.md"). A file
// outside the design dir degrades to its base name.
func OracleRel(projectRoot string, cfg Config, file string) string {
	designRoot := filepath.Join(projectRoot, filepath.FromSlash(cfg.Dir))
	rel, err := filepath.Rel(designRoot, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(file)
	}
	return filepath.ToSlash(rel)
}

// OracleStem returns the oracle file name without its .oracle.md suffix
// ("Deal", "Policy").
func OracleStem(file string) string {
	return strings.TrimSuffix(filepath.Base(file), ".oracle.md")
}

// oracleHeadingRe matches the generated H1: "# Generated <kind> oracle: <name>"
// where <name> may be backticked ("`applicabilityElection`", "policy").
var oracleHeadingRe = regexp.MustCompile("(?m)^#\\s*Generated\\s+(.+?)\\s+oracle:\\s*`?([A-Za-z0-9_.-]+)`?")

// OracleIdentity returns the oracle's declared kind and name from its
// generated heading ("transition", "applicabilityElection"; "authorization",
// "policy"). Absent heading: kind "" and the file stem as the name.
func OracleIdentity(file, content string) (kind, name string) {
	if m := oracleHeadingRe.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	return "", OracleStem(file)
}

// OracleRows parses a generated oracle's Transitions table into stable id ->
// full row text. Rows are keyed by the "stable id" column, located from the
// table header so a format evolution reorders nothing silently.
func OracleRows(content string) map[string]string {
	rows := map[string]string{}
	idCol := -1
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			idCol = -1
			continue
		}
		cells := splitTableRow(trimmed)
		if idCol == -1 {
			for i, c := range cells {
				if strings.EqualFold(strings.TrimSpace(c), "stable id") {
					idCol = i
					break
				}
			}
			continue
		}
		if len(cells) <= idCol {
			continue
		}
		id := strings.TrimSpace(cells[idCol])
		if id == "" || strings.HasPrefix(id, "---") || strings.Trim(id, "-: ") == "" {
			continue
		}
		rows[id] = trimmed
	}
	return rows
}

func splitTableRow(row string) []string {
	row = strings.Trim(row, "|")
	return strings.Split(row, "|")
}

// StableIDs returns every stable id across the project's oracles (machines
// and formal), mapped to the design-relative oracle path each came from
// ("machines/Deal.oracle.md", "formal/Policy.oracle.md").
func StableIDs(projectRoot string, cfg Config) (map[string]string, error) {
	oracles, err := LoadOracles(projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, o := range oracles {
		for _, id := range o.IDs {
			ids[id] = o.Rel
		}
	}
	return ids, nil
}

// Oracle is one committed oracle file with its parsed identity and ids.
type Oracle struct {
	Path string   // absolute path
	Rel  string   // design-relative, slash separated
	Kind string   // "transition", "authorization", "tenant-scoping", ... ("" when no heading)
	Name string   // declared name ("applicabilityElection", "policy") or the file stem
	IDs  []string // stable ids, sorted
}

// Formal reports whether the oracle lives under formal/ (a relational
// decision table) rather than machines/ (a transition oracle).
func (o Oracle) Formal() bool {
	return strings.HasPrefix(o.Rel, "formal/")
}

// LoadOracles parses every committed oracle, sorted by design-relative path.
func LoadOracles(projectRoot string, cfg Config) ([]Oracle, error) {
	files, err := OracleFiles(projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	oracles := make([]Oracle, 0, len(files))
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			return nil, fmt.Errorf("read oracle %s: %w", f, rerr)
		}
		content := string(data)
		kind, name := OracleIdentity(f, content)
		rows := OracleRows(content)
		ids := make([]string, 0, len(rows))
		for id := range rows {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		oracles = append(oracles, Oracle{
			Path: f,
			Rel:  OracleRel(projectRoot, cfg, f),
			Kind: kind,
			Name: name,
			IDs:  ids,
		})
	}
	sort.Slice(oracles, func(i, j int) bool { return oracles[i].Rel < oracles[j].Rel })
	return oracles, nil
}

// MatchesSelector reports whether a user-supplied selector names this
// oracle: its declared name, file stem, base name, or design-relative path,
// compared case-insensitively ("tenant", "Tenant.oracle.md",
// "machines/Tenant.oracle.md", "formal/Policy", "policy" all resolve).
func (o Oracle) MatchesSelector(sel string) bool {
	s := strings.ToLower(strings.TrimSpace(sel))
	if s == "" {
		return false
	}
	candidates := []string{o.Name, OracleStem(o.Path), filepath.Base(o.Path), o.Rel, strings.TrimSuffix(o.Rel, ".oracle.md")}
	for _, c := range candidates {
		if strings.ToLower(c) == s {
			return true
		}
	}
	return false
}

// kindAliases are the conventional short forms of a formal oracle's kind, so
// the plan's test-id convention ("P-authz-oracle" for the authorization
// table, per machinery's go-crm reference suite) selects the oracle.
var kindAliases = map[string][]string{
	"authorization": {"authz"},
}

// NamedIn reports whether a prose block (a BUILD.md milestone) names this
// oracle. Transition oracles match on their machine name or file stem as a
// whole token, case-insensitively ("the `ErasureRequest` machine",
// "erasureRequest"). Formal oracles carry generic names ("policy",
// "isolation") that would collide with ordinary prose, so they match only
// on an oracle-qualified mention: the file name (Policy.oracle.md), or the
// name or kind immediately followed by "-oracle", " oracle", or ".oracle"
// (T-isolation-oracle, "the authorization oracle", "policy.oracle").
func (o Oracle) NamedIn(block string) bool {
	lower := strings.ToLower(block)
	if o.Formal() {
		if strings.Contains(lower, strings.ToLower(filepath.Base(o.Path))) {
			return true
		}
		aliases := []string{o.Name, o.Kind}
		aliases = append(aliases, kindAliases[strings.ToLower(o.Kind)]...)
		for _, alias := range aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
				continue
			}
			for _, suffix := range []string{"-oracle", " oracle", ".oracle"} {
				if strings.Contains(lower, alias+suffix) {
					return true
				}
			}
		}
		return false
	}
	for _, alias := range []string{o.Name, OracleStem(o.Path)} {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias != "" && TokenIn(alias, lower) {
			return true
		}
	}
	return false
}

// TokenIn reports whether token appears in text as a whole token (no
// [A-Za-z0-9_-] on either side), the same rule machinery's gates use, so a
// stable id never matches inside a longer identifier.
func TokenIn(token, text string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], token)
		if i < 0 {
			return false
		}
		i += start
		before := byte(' ')
		if i > 0 {
			before = text[i-1]
		}
		after := byte(' ')
		if i+len(token) < len(text) {
			after = text[i+len(token)]
		}
		if !isTokenByte(before) && !isTokenByte(after) {
			return true
		}
		start = i + 1
	}
}

func isTokenByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
