package converge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/channel"
)

// claudeInstallPointer is printed when the claude CLI is missing.
const claudeInstallPointer = "install Claude Code first: https://claude.com/claude-code"

// runClaude executes the claude CLI through the exec seam and returns its
// combined output.
var runClaude = func(args ...string) (string, error) {
	out, err := execCommand("claude", args...).CombinedOutput()
	return string(out), err
}

// marketplaceEntry is one entry from `claude plugin marketplace list`.
type marketplaceEntry struct {
	Name       string
	SourceType string // "GitHub", "Directory", ...
	Source     string // org/repo or path
}

// pluginEntry is one entry from `claude plugin list`.
type pluginEntry struct {
	Name        string
	Marketplace string
	Version     string
}

// entryHeader recognizes a list-entry header line ("❯ name", "- name",
// "* name") and returns the entry name.
func entryHeader(trimmed string) (string, bool) {
	for _, marker := range []string{"❯ ", "- ", "* "} {
		if strings.HasPrefix(trimmed, marker) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, marker)), true
		}
	}
	return "", false
}

// parseMarketplaceList parses `claude plugin marketplace list` output:
//
//	Configured marketplaces:
//
//	  ❯ nd
//	    Source: Directory (/path/to/nd-skill)
//	  ❯ openai-codex
//	    Source: GitHub (openai/codex-plugin-cc)
func parseMarketplaceList(out string) []marketplaceEntry {
	var entries []marketplaceEntry
	var cur *marketplaceEntry
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if name, ok := entryHeader(t); ok {
			entries = append(entries, marketplaceEntry{Name: name})
			cur = &entries[len(entries)-1]
			continue
		}
		if cur != nil && strings.HasPrefix(t, "Source:") {
			rest := strings.TrimSpace(strings.TrimPrefix(t, "Source:"))
			if i := strings.Index(rest, "("); i >= 0 && strings.HasSuffix(rest, ")") {
				cur.SourceType = strings.TrimSpace(rest[:i])
				cur.Source = rest[i+1 : len(rest)-1]
			} else {
				cur.SourceType = rest
			}
		}
	}
	return entries
}

// parsePluginList parses `claude plugin list` output:
//
//	Installed plugins:
//
//	  ❯ nd@nd
//	    Version: 0.10.20
//	    Scope: user
//	    Status: enabled
func parsePluginList(out string) []pluginEntry {
	var entries []pluginEntry
	var cur *pluginEntry
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if name, ok := entryHeader(t); ok {
			e := pluginEntry{Name: name}
			if i := strings.LastIndex(name, "@"); i >= 0 {
				e.Name, e.Marketplace = name[:i], name[i+1:]
			}
			entries = append(entries, e)
			cur = &entries[len(entries)-1]
			continue
		}
		if cur != nil && strings.HasPrefix(t, "Version:") {
			cur.Version = strings.TrimSpace(strings.TrimPrefix(t, "Version:"))
		}
	}
	return entries
}

// convergePlugins drives the claude CLI so the channel-pinned plugins are
// installed from GitHub marketplaces at the pinned versions. Reports each
// action through report and returns false when any plugin failed to
// converge or verify (the step keeps going across plugins).
func convergePlugins(m channel.Manifest, dryRun bool, report func(status, format string, args ...any)) bool {
	if _, err := lookPath("claude"); err != nil {
		report("FAIL", "claude CLI not found -- %s", claudeInstallPointer)
		return false
	}

	names := make([]string, 0, len(m.Plugins))
	for name := range m.Plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	mpOut, err := runClaude("plugin", "marketplace", "list")
	if err != nil {
		report("FAIL", "claude plugin marketplace list: %v\n%s", err, strings.TrimSpace(mpOut))
		return false
	}
	marketplaces := parseMarketplaceList(mpOut)

	ok := true
	for _, name := range names {
		pin := m.Plugins[name]
		if err := convergeOnePlugin(name, pin, marketplaces, dryRun, report); err != nil {
			report("FAIL", "plugin %s: %v", name, err)
			ok = false
		}
	}
	if dryRun {
		return ok
	}

	// Verify final versions against the channel pins.
	listOut, err := runClaude("plugin", "list")
	if err != nil {
		report("FAIL", "claude plugin list: %v\n%s", err, strings.TrimSpace(listOut))
		return false
	}
	installed := parsePluginList(listOut)
	for _, name := range names {
		pin := m.Plugins[name]
		if verifyPluginVersion(name, pin.Version, installed, report) != nil {
			ok = false
		}
	}
	return ok
}

func convergeOnePlugin(name string, pin channel.PluginPin, marketplaces []marketplaceEntry, dryRun bool, report func(status, format string, args ...any)) error {
	var existing *marketplaceEntry
	for i := range marketplaces {
		if marketplaces[i].Name == name {
			existing = &marketplaces[i]
			break
		}
	}

	if dryRun {
		switch {
		case existing == nil:
			report("OK", "would add marketplace %s (%s) and install %s@%s", name, pin.Marketplace, name, name)
		case existing.SourceType == "Directory":
			report("OK", "would replace Directory marketplace %s (%s) with GitHub %s", name, existing.Source, pin.Marketplace)
		default:
			report("OK", "would update plugin %s@%s to %s", name, name, pin.Version)
		}
		return nil
	}

	if existing != nil && existing.SourceType == "Directory" {
		if out, err := runClaude("plugin", "marketplace", "remove", name); err != nil {
			return fmt.Errorf("marketplace remove %s: %v\n%s", name, err, strings.TrimSpace(out))
		}
		existing = nil
	}
	if existing == nil {
		if out, err := runClaude("plugin", "marketplace", "add", pin.Marketplace); err != nil {
			return fmt.Errorf("marketplace add %s: %v\n%s", pin.Marketplace, err, strings.TrimSpace(out))
		}
	}

	spec := name + "@" + name
	if out, err := runClaude("plugin", "install", spec); err != nil {
		// Tolerate an already-installed plugin; the subsequent update
		// converges its version.
		if !strings.Contains(strings.ToLower(out), "already") {
			return fmt.Errorf("plugin install %s: %v\n%s", spec, err, strings.TrimSpace(out))
		}
	}
	if out, err := runClaude("plugin", "marketplace", "update", name); err != nil {
		return fmt.Errorf("marketplace update %s: %v\n%s", name, err, strings.TrimSpace(out))
	}
	if out, err := runClaude("plugin", "update", spec); err != nil {
		return fmt.Errorf("plugin update %s: %v\n%s", spec, err, strings.TrimSpace(out))
	}
	return nil
}

// verifyPluginVersion checks that name@name is installed at the pinned
// version and reports OK/FAIL accordingly.
func verifyPluginVersion(name, pinned string, installed []pluginEntry, report func(status, format string, args ...any)) error {
	var versions []string
	for _, e := range installed {
		if e.Name == name && e.Marketplace == name {
			if e.Version == pinned {
				report("OK", "plugin %s@%s at %s", name, name, pinned)
				return nil
			}
			versions = append(versions, e.Version)
		}
	}
	if len(versions) == 0 {
		err := fmt.Errorf("plugin %s@%s not installed (channel pins %s)", name, name, pinned)
		report("FAIL", "%v", err)
		return err
	}
	err := fmt.Errorf("plugin %s@%s is %s, channel pins %s", name, name, strings.Join(versions, ", "), pinned)
	report("FAIL", "%v", err)
	return err
}
