// Package settings manages project-local vault settings (.vault/knowledge/.settings.yaml).
package settings

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/paivot-ai/pvg/internal/ndvault"
)

// BuildVersion is the running pvg version, assigned from main at startup.
// Empty (a dev build or library use) reads as "dev" via RunningVersion.
var BuildVersion = ""

// RunningVersion returns BuildVersion, or "dev" when unset.
func RunningVersion() string {
	if BuildVersion == "" {
		return "dev"
	}
	return BuildVersion
}

// timeNow is a seam for tests to pin the calibration stamp date.
var timeNow = time.Now

const settingsFile = ".vault/knowledge/.settings.yaml"

// defaults for all known settings.
// Keys here must match those documented in commands/vault-settings.md.
var defaults = map[string]string{
	"session_start_max_notes":  "10",
	"auto_capture":             "true",
	"staleness_days":           "30",
	"stack_detection":          "false",
	"bug_fast_track":           "false",
	"project_vault_git":        "ask",
	"default_scope":            "system",
	"proposal_expiry_days":     "30",
	"auto_init_project_vault":  "ask",
	"workflow.solo_dev":        "true",
	"workflow.fsm":             "false",
	"workflow.sequence":        "open,in_progress,closed",
	"workflow.exit_rules":      "blocked:open,in_progress;deferred:open,in_progress",
	"workflow.custom_statuses": "",
	// D&F specialist challengers default ON: production posture assumes each
	// BLT document gets a challenge pass unless the project opts out.
	"dnf.specialist_review": "true",
	"dnf.max_iterations":    "3",
	// Deprecated pair, kept for existing projects: the machinery design
	// substrate (design.machinery) supersedes both. The adapter skills read
	// design.machinery; these two only gate the legacy narrative-twin flow.
	"dnf.domain_model": "false",
	"architecture.c4":  "false",
	// The machinery design substrate: domain model -> C4 contract -> state
	// machines -> oracles, checked by `machinery check`. Strictly user-opt-in:
	// off (default) disables it everywhere; on promises it (a missing design
	// then fails loudly); auto is a deliberate, explicit choice to re-enable
	// artifact detection (.machinery.json or design/domain.modelith.yaml).
	// Artifact presence alone never enables the substrate.
	"design.machinery": "off",
	// The gate list `pvg story approve-red` runs for the RED exit check.
	// Empty (default) derives it from .machinery.json: the staged list
	// without gt (Gt-tests is whole-design and belongs to the seal, the
	// per-story half of it -- every story-cited id carried by a test -- is
	// what approve-red checks itself), and without --impl when no list is
	// staged (machinery's default selection would pull the impl-only gates
	// in). An explicit list is used verbatim; --impl rides along only when
	// it names g4 or gt. `pvg gates --seal` runs the whole-design check.
	"design.red_gates": "",
	// Project verification and review workflows on a machinery-first or
	// tooling-mandated project (for example the elixir-phoenix plugin):
	// verify.command is run INLINE by the developer before delivery (a
	// skill or command that spawns no subagents, e.g. /phx:verify);
	// review.command is run by the DISPATCHER at the epic completion gate,
	// because review workflows spawn specialist subagents and the developer
	// and PM cannot (e.g. /phx:review). Empty = no project workflow.
	"verify.command": "",
	"review.command": "",
	// hard_tdd.preauthorized=true records the user's standing authorization
	// of hard-TDD for every story: `pvg lint --backlog` then requires the
	// hard-tdd label on every non-closed story, or the hard-tdd-exempt label
	// plus a "HARD-TDD EXEMPT:" justification line (docs, config, discovery).
	"hard_tdd.preauthorized":       "false",
	"loop.persist_across_sessions": "true",
	// Resume hints for semi-persistent story agents: when "true" (default),
	// `pvg loop agent set` handles make `pvg loop next` emit
	// resume_agent/resume_count on developer_rework and pm_review actions
	// (capped at 2 resumes per handle). "false" disables emission.
	"loop.agent_resume":  "true",
	"lint.quality_gates": "",
	"lint.brownfield":    "false",
	// Path prefixes the brownfield paths-exist check treats as greenfield:
	// a story may name a file under them before it exists on disk or in a
	// PRODUCES block (a monorepo whose design tree has a long history while
	// the implementation tree is new). Comma-separated, e.g. "lib/,test/".
	"lint.paths_exist.exclude": "",
	"update.nudge":             "true",
	// Per-role model overrides for Paivot agents. Empty = no override
	// (the agent's frontmatter model wins). See setSettings for validation.
	"model.developer":            "",
	"model.pm":                   "",
	"model.sr_pm":                "",
	"model.anchor":               "",
	"model.retro":                "",
	"model.ba":                   "",
	"model.designer":             "",
	"model.architect":            "",
	"model.ba_challenger":        "",
	"model.designer_challenger":  "",
	"model.architect_challenger": "",
	// Metric quality gates on delivered code (pvg gates). Mode keys take
	// off|warn|block; absent analyzer tools cause the gate to be skipped and
	// noted, never silently passed. See setSettings for mode validation.
	"gates.complexity":            "block",
	"gates.complexity.warn_cc":    "15",
	"gates.complexity.block_cc":   "30",
	"gates.duplication":           "block",
	"gates.duplication.max_pct":   "10",
	"gates.duplication.min_lines": "50",
	"gates.file_loc":              "warn",
	"gates.file_loc.max":          "400",
	"gates.exclude":               "vendor/,node_modules/,*.generated.*,*.pb.go,migrations/,*.lock,*.min.*,dist/,build/",
	// Calibration stamp: written automatically by `pvg settings` whenever a
	// gates.* or model.* key changes; read by `pvg doctor`; not meant to be
	// set by hand.
	"calibration.stamped": "",
	"calibration.pvg":     "",
}

var execCommand = exec.Command

// Run handles the `pvg settings` command.
// With no args: display current settings.
// With a single key: print its value.
// With key=value args: set settings.
func Run(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	path := filepath.Join(cwd, settingsFile)

	if len(args) == 0 {
		return showSettings(path)
	}

	if len(args) == 1 && !strings.Contains(args[0], "=") {
		return showSetting(path, strings.TrimSpace(args[0]))
	}

	return setSettings(cwd, path, args)
}

func showSettings(path string) error {
	settings := loadSettings(path)

	fmt.Println("Project vault settings (.vault/knowledge/.settings.yaml):")
	fmt.Println()

	// Sort keys for stable output
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		val, ok := settings[k]
		if !ok {
			val = defaults[k] + " (default)"
		}
		fmt.Printf("  %s: %s\n", k, val)
	}

	// Show any extra settings not in defaults
	for k, v := range settings {
		if _, ok := defaults[k]; !ok {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}

	return nil
}

func showSetting(path, key string) error {
	if key == "" {
		return fmt.Errorf("missing setting key")
	}

	settings := loadSettings(path)
	if val, ok := settings[key]; ok {
		fmt.Println(val)
		return nil
	}

	if val, ok := defaults[key]; ok {
		fmt.Println(val)
		return nil
	}

	return fmt.Errorf("unknown setting %q", key)
}

func setSettings(projectRoot, path string, args []string) error {
	settings := loadSettings(path)
	originalContent, hadOriginalFile, err := readOptionalFile(path)
	if err != nil {
		return err
	}

	workflowChanged := false
	calibrationChanged := false
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid setting %q (expected key=value)", arg)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return fmt.Errorf("empty key in %q", arg)
		}

		if strings.HasPrefix(key, "model.") {
			if err := validateModelValue(key, value); err != nil {
				return err
			}
		}

		if isGateModeKey(key) {
			if err := validateGateMode(key, value); err != nil {
				return err
			}
		}

		if key == "design.machinery" && value != "auto" && value != "on" && value != "off" {
			return fmt.Errorf("invalid value %q for design.machinery (allowed: auto, on, off)", value)
		}

		if key == "loop.agent_resume" && value != "true" && value != "false" {
			return fmt.Errorf("invalid value %q for loop.agent_resume (allowed: true, false)", value)
		}

		if key == "hard_tdd.preauthorized" && value != "true" && value != "false" {
			return fmt.Errorf("invalid value %q for hard_tdd.preauthorized (allowed: true, false)", value)
		}

		settings[key] = value
		fmt.Printf("  set %s = %s\n", key, value)

		if strings.HasPrefix(key, "workflow.") {
			workflowChanged = true
		}
		// Any gates.* or model.* write is a calibration change, including
		// setting one to empty: clearing an override recalibrates against the
		// built-in default. calibration.* keys themselves never restamp.
		if strings.HasPrefix(key, "gates.") || strings.HasPrefix(key, "model.") {
			calibrationChanged = true
		}
	}

	// Stamp the calibration date and pvg version so `pvg doctor` can flag
	// tuned thresholds that outlive the toolchain they were tuned against.
	if calibrationChanged {
		stamped := timeNow().Format("2006-01-02")
		ver := RunningVersion()
		settings["calibration.stamped"] = stamped
		settings["calibration.pvg"] = ver
		fmt.Printf("  stamped calibration.stamped = %s (gates/model change)\n", stamped)
		fmt.Printf("  stamped calibration.pvg = %s (gates/model change)\n", ver)
	}

	if err := writeSettings(path, settings); err != nil {
		return err
	}

	if workflowChanged {
		if err := syncNdConfig(projectRoot, settings); err != nil {
			if restoreErr := restoreSettingsFile(path, originalContent, hadOriginalFile); restoreErr != nil {
				return fmt.Errorf("sync nd workflow settings: %w (also failed to restore settings file: %v)", err, restoreErr)
			}
			return fmt.Errorf("sync nd workflow settings: %w", err)
		}
	}

	return nil
}

func loadSettings(path string) map[string]string {
	settings := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return settings
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			settings[strings.TrimSpace(parts[0])] = unquoteValue(strings.TrimSpace(parts[1]))
		}
	}
	return settings
}

// unquoteValue strips one matched pair of surrounding YAML quotes.
//
// `pvg settings key=value` always writes bare values, but the file is plain
// YAML and a user editing it by hand naturally writes `design.machinery:
// "on"`. Without this, that value compares unequal to every expected literal
// and the setting silently resolves to its default -- the substrate stays
// off, a boolean gate reads as unset. A setting the user believes they set
// must never be silently ignored.
func unquoteValue(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func writeSettings(path string, settings map[string]string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create settings directory: %w", err)
	}

	var lines []string
	lines = append(lines, "# paivot-graph project vault settings")
	lines = append(lines, "# Managed by: pvg settings key=value")
	lines = append(lines, "")

	// Sort keys for stable output
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, settings[k]))
	}
	lines = append(lines, "")

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read settings file %s: %w", path, err)
}

func restoreSettingsFile(path string, content []byte, existed bool) error {
	if existed {
		return os.WriteFile(path, content, 0644)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadFile reads and parses the settings from a file path.
// Returns a map of key-value pairs (empty if file is missing or unreadable).
func LoadFile(path string) map[string]string {
	return loadSettings(path)
}

// Default returns the built-in value for a known setting key.
func Default(key string) string {
	return defaults[key]
}

// syncNdConfig propagates workflow settings to nd.
func syncNdConfig(projectRoot string, settings map[string]string) error {
	vaultDir, err := ndvault.Resolve(projectRoot)
	if err != nil {
		return err
	}

	enabled := settings["workflow.fsm"] == "true"
	if enabled {
		custom := settingOrDefault(settings, "workflow.custom_statuses")
		sequence := settingOrDefault(settings, "workflow.sequence")
		rules := settingOrDefault(settings, "workflow.exit_rules")

		if custom != "" {
			if err := runNDConfigSet(vaultDir, "status.custom", custom); err != nil {
				return err
			}
		}
		if sequence != "" {
			if err := runNDConfigSet(vaultDir, "status.sequence", sequence); err != nil {
				return err
			}
		}
		if rules != "" {
			if err := runNDConfigSet(vaultDir, "status.exit_rules", rules); err != nil {
				return err
			}
		}
		return runNDConfigSet(vaultDir, "status.fsm", "true")
	} else {
		return runNDConfigSet(vaultDir, "status.fsm", "false")
	}
}

func runNDConfigSet(vaultDir, key, value string) error {
	cmd := execCommand("nd", "--vault", vaultDir, "config", "set", key, value)
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("nd config set %s=%s: %s", key, value, msg)
		}
		return fmt.Errorf("nd config set %s=%s: %w", key, value, err)
	}
	return nil
}

// validateModelValue checks a model.<role> setting value. Empty means "no
// override"; the aliases opus/sonnet/haiku/fable/inherit and any full model id
// (claude-*) are accepted. Everything else is rejected to catch typos.
func validateModelValue(key, value string) error {
	switch value {
	case "", "opus", "sonnet", "haiku", "fable", "inherit":
		return nil
	}
	if strings.HasPrefix(value, "claude-") {
		return nil
	}
	return fmt.Errorf("invalid model %q for %s (allowed: opus, sonnet, haiku, fable, inherit, or a claude-* model id)", value, key)
}

// isGateModeKey reports whether a key is one of the three gates.* mode keys
// (those that take off|warn|block). The numeric threshold keys and the
// exclude list are NOT mode keys.
func isGateModeKey(key string) bool {
	switch key {
	case "gates.complexity", "gates.duplication", "gates.file_loc":
		return true
	}
	return false
}

// validateGateMode checks a gates.* mode setting value. Only off|warn|block
// are accepted; everything else is rejected to catch typos.
func validateGateMode(key, value string) error {
	switch value {
	case "off", "warn", "block":
		return nil
	}
	return fmt.Errorf("invalid mode %q for %s (allowed: off, warn, block)", value, key)
}

func settingOrDefault(settings map[string]string, key string) string {
	if val := settings[key]; val != "" {
		return val
	}
	return defaults[key]
}
