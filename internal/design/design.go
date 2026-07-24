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

// OracleFiles lists the committed transition oracles under the design dir.
func OracleFiles(projectRoot string, cfg Config) ([]string, error) {
	pattern := filepath.Join(projectRoot, filepath.FromSlash(cfg.Dir), "machines", "*.oracle.md")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
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

// StableIDs returns every stable id across the project's oracles, sorted,
// with the oracle file (base name) each came from.
func StableIDs(projectRoot string, cfg Config) (map[string]string, error) {
	files, err := OracleFiles(projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			return nil, fmt.Errorf("read oracle %s: %w", f, rerr)
		}
		base := filepath.Base(f)
		for id := range OracleRows(string(data)) {
			ids[id] = base
		}
	}
	return ids, nil
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
