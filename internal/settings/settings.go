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

	"github.com/paivot-ai/pvg/internal/ndvault"
)

const settingsFile = ".vault/knowledge/.settings.yaml"

// defaults for all known settings.
// Keys here must match those documented in commands/vault-settings.md.
var defaults = map[string]string{
	"session_start_max_notes":      "10",
	"auto_capture":                 "true",
	"staleness_days":               "30",
	"stack_detection":              "false",
	"bug_fast_track":               "false",
	"project_vault_git":            "ask",
	"default_scope":                "system",
	"proposal_expiry_days":         "30",
	"auto_init_project_vault":      "ask",
	"workflow.solo_dev":            "true",
	"workflow.fsm":                 "false",
	"workflow.sequence":            "open,in_progress,closed",
	"workflow.exit_rules":          "blocked:open,in_progress;deferred:open,in_progress",
	"workflow.custom_statuses":     "",
	"dnf.specialist_review":        "false",
	"dnf.max_iterations":           "3",
	"architecture.c4":              "false",
	"loop.persist_across_sessions": "true",
	"lint.quality_gates":           "",
	"lint.brownfield":              "false",
	"update.nudge":                 "true",
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

		settings[key] = value
		fmt.Printf("  set %s = %s\n", key, value)

		if strings.HasPrefix(key, "workflow.") {
			workflowChanged = true
		}
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
			settings[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return settings
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

func settingOrDefault(settings map[string]string, key string) string {
	if val := settings[key]; val != "" {
		return val
	}
	return defaults[key]
}
