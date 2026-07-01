// Package converge is the shared convergence engine behind `pvg setup` and
// `pvg update`. It drives every distributed Paivot artifact -- tool binaries
// (pvg, nd, vlt), the vlt Claude skill, and the paivot-graph/nd Claude
// plugins -- to the versions pinned by the channel manifest.
package converge

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/paivot-ai/pvg/internal/channel"
	"github.com/paivot-ai/pvg/internal/gates"
	"github.com/paivot-ai/pvg/internal/paivotcfg"
)

// toolSpec describes how to query one managed tool binary.
type toolSpec struct {
	Name        string
	VersionArgs []string
}

// toolSpecs is the fixed convergence order. pvg first so a fixed pvg can be
// re-run if anything later fails.
var toolSpecs = []toolSpec{
	{Name: "pvg", VersionArgs: []string{"version"}},
	{Name: "nd", VersionArgs: []string{"--version"}},
	{Name: "vlt", VersionArgs: []string{"version"}},
	// modelith backs the optional dnf.domain_model D&F artifact. It is a
	// third-party tool (stacklok/modelith) pinned by the channel manifest; if
	// the manifest does not pin it, convergence SKIPs it gracefully.
	{Name: "modelith", VersionArgs: []string{"--version"}},
}

// Options selects which convergence surfaces run.
type Options struct {
	// Ref is the paivot-graph git ref to fetch the manifest at ("" = main).
	Ref string
	// DryRun reports what would change without mutating anything.
	DryRun bool
	// Force reinstalls artifacts even when already at the pinned version.
	Force bool
	// Tools restricts binary convergence to these names (nil = all).
	Tools []string
	// VltSkill converges ~/.claude/skills/vlt-skill to the channel pin.
	VltSkill bool
	// Plugins converges the Claude plugins via the claude CLI.
	Plugins bool
	// PathWiring appends ~/.local/bin to ~/.profile when needed (setup only).
	PathWiring bool
	// RecommendAnalyzers prints the optional code-quality analyzer nudge for
	// `pvg gates` after the summary (setup only).
	RecommendAnalyzers bool
	// Out receives progress lines; defaults to os.Stdout.
	Out io.Writer
}

// Artifact is one row of the final summary.
type Artifact struct {
	Name      string
	Kind      string // tool | skill | plugin
	Installed string
	Channel   string
	State     string // ok | updated | installed | failed | skipped | would-change
}

// Report is the outcome of a convergence run.
type Report struct {
	Manifest  channel.Manifest
	Artifacts []Artifact
	Failed    bool
}

// fetchManifest is overridable for tests.
var fetchManifest = channel.FetchRaw

// Run converges the selected artifacts to the channel manifest pins. It
// always fetches the manifest fresh (never from the nudge cache). A nil
// error with Report.Failed=true means individual steps failed but the run
// itself completed; callers should exit non-zero.
func Run(opts Options) (Report, error) {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if runtime.GOOS == "windows" {
		return Report{Failed: true}, fmt.Errorf("windows is not supported; install the Paivot tools manually")
	}
	var rep Report
	report := func(status, format string, args ...any) {
		fmt.Fprintf(out, "%s: %s\n", status, fmt.Sprintf(format, args...))
		if status == "FAIL" {
			rep.Failed = true
		}
	}

	m, raw, err := fetchManifest(opts.Ref)
	if err != nil {
		report("FAIL", "channel manifest: %v", err)
		return rep, err
	}
	rep.Manifest = m
	report("OK", "channel %s manifest fetched (updated %s)", orUnknown(m.Channel), orUnknown(m.Updated))
	if opts.Ref == "" || opts.Ref == channel.DefaultRef {
		_ = channel.SaveCache(m, raw) // keep the nudge cache in sync, best effort
	}

	// Memoize the sudo probe so a passwordless-sudo check runs at most once.
	sudoChecked, sudoOK := false, false
	probe := dirProbe{
		writable: dirWritable,
		sudoOK: func() bool {
			if !sudoChecked {
				sudoOK = sudoNonInteractive()
				sudoChecked = true
			}
			return sudoOK
		},
		homeDir: userHomeDir,
	}

	usedLocalBin := false
	for _, spec := range toolSpecs {
		if !toolSelected(spec.Name, opts.Tools) {
			continue
		}
		pin, ok := m.Tools[spec.Name]
		if !ok {
			report("SKIP", "%s: not pinned by the channel manifest", spec.Name)
			rep.Artifacts = append(rep.Artifacts, Artifact{Name: spec.Name, Kind: "tool", State: "skipped"})
			continue
		}
		art, local := convergeTool(spec, pin, opts, probe, report)
		rep.Artifacts = append(rep.Artifacts, art)
		usedLocalBin = usedLocalBin || local
	}

	if opts.PathWiring {
		if usedLocalBin {
			if action, err := ensurePathWiring(); err != nil {
				report("FAIL", "PATH wiring: %v", err)
			} else {
				report("OK", "PATH wiring: %s", action)
			}
		} else {
			report("SKIP", "PATH wiring: ~/.local/bin not used")
		}
	}

	if opts.VltSkill {
		rep.Artifacts = append(rep.Artifacts, convergeVltSkill(m, opts, report))
	}

	if opts.Plugins {
		pluginVersions, ok := convergePlugins(m, opts.DryRun, report)
		names := make([]string, 0, len(m.Plugins))
		for name := range m.Plugins {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			state := "ok"
			if opts.DryRun {
				state = "would-change"
			} else if !ok {
				state = "failed"
			}
			rep.Artifacts = append(rep.Artifacts, Artifact{
				Name:      name + " (plugin)",
				Kind:      "plugin",
				Installed: pluginVersions[name],
				Channel:   m.Plugins[name].Version,
				State:     state,
			})
		}
	}

	printSummary(out, rep)
	if opts.RecommendAnalyzers {
		printAnalyzerRecommendation(out, gates.MissingRecommended())
	}
	return rep, nil
}

// printAnalyzerRecommendation prints the optional code-quality analyzer nudge
// after the convergence summary. When all recommended analyzers are present it
// prints a single concise line; otherwise it lists the missing ones with exact
// install commands, the apt caveat, and the single-language fallbacks.
func printAnalyzerRecommendation(out io.Writer, missing []gates.Analyzer) {
	if len(missing) == 0 {
		fmt.Fprintln(out, "\nCode-quality analyzers: lizard, jscpd present.")
		return
	}
	fmt.Fprintln(out, "\nOptional: code-quality analyzers for `pvg gates`")
	fmt.Fprintln(out, "  The delivered-code quality gate shells out to external analyzers for")
	fmt.Fprintln(out, "  duplication and complexity. Without them those gates SKIP -- they never")
	fmt.Fprintln(out, "  block falsely, but you lose the strongest signal against copy-paste and")
	fmt.Fprintln(out, "  unmaintainable code. Install the missing recommended tools:")
	fmt.Fprintln(out)
	for _, a := range missing {
		fmt.Fprintf(out, "    %-24s # %s  [recommended]\n", a.Install, a.Purpose)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Single-language fallbacks (only if you skip lizard):")
	fmt.Fprintln(out, "    go install github.com/fzipp/gocyclo/cmd/gocyclo@latest   # Go")
	fmt.Fprintln(out, "    pip install radon                                        # Python (Ubuntu: apt install python3-radon)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  apt alone is not enough -- only radon ships in the Ubuntu repos. pip and npm")
	fmt.Fprintln(out, "  (already on most dev machines) get you the multi-language tools.")
}

func toolSelected(name string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == name {
			return true
		}
	}
	return false
}

// convergeTool brings one tool binary to its pinned version. Returns the
// summary artifact and whether ~/.local/bin was the install target.
func convergeTool(spec toolSpec, pin channel.Pin, opts Options, probe dirProbe, report func(status, format string, args ...any)) (Artifact, bool) {
	art := Artifact{Name: spec.Name, Kind: "tool", Channel: pin.Version}

	installedPath, installedOut := installedTool(spec)
	art.Installed = normalizeVersion(installedOut)
	if art.Installed == "" && installedPath != "" {
		art.Installed = "unknown"
	}

	if !opts.Force && sameVersion(installedOut, pin.Version) {
		report("OK", "%s %s is current", spec.Name, pin.Version)
		art.State = "ok"
		return art, false
	}

	target, err := selectInstallDir(installedPath, probe)
	if err != nil {
		report("FAIL", "%s: %v", spec.Name, err)
		art.State = "failed"
		return art, false
	}
	local := isLocalBin(target.Dir, probe)

	if opts.DryRun {
		report("OK", "would install %s %s to %s (currently %s)", spec.Name, pin.Version, target.Dir, orNotInstalled(art.Installed))
		art.State = "would-change"
		return art, local
	}

	if err := installBinary(spec.Name, pin.Repo, pin.Version, target); err != nil {
		report("FAIL", "%s: %v", spec.Name, err)
		art.State = "failed"
		return art, false
	}
	if target.Fresh {
		report("OK", "%s %s installed to %s", spec.Name, pin.Version, target.Dir)
		art.State = "installed"
	} else {
		report("OK", "%s %s updated in place at %s", spec.Name, pin.Version, target.Dir)
		art.State = "updated"
	}
	art.Installed = strings.TrimPrefix(pin.Version, "v")
	return art, local
}

// installedTool locates a tool and captures its version output. pvg is
// special: the running binary's own path and ldflag version are used.
func installedTool(spec toolSpec) (path, versionOut string) {
	if spec.Name == "pvg" {
		if p, err := selfPath(); err == nil {
			path = p
		} else if p, err := lookPath("pvg"); err == nil {
			path = p
		}
		return path, SelfVersion
	}
	p, err := lookPath(spec.Name)
	if err != nil {
		return "", ""
	}
	out, err := toolVersionOutput(p, spec.VersionArgs...)
	if err != nil {
		return p, ""
	}
	return p, out
}

func convergeVltSkill(m channel.Manifest, opts Options, report func(status, format string, args ...any)) Artifact {
	art := Artifact{Name: "vlt-skill", Kind: "skill"}
	pin, ok := m.Skills["vlt-skill"]
	if !ok {
		report("SKIP", "vlt-skill: not pinned by the channel manifest")
		art.State = "skipped"
		return art
	}
	art.Channel = pin.Version
	current := vltSkillState()
	art.Installed = strings.TrimPrefix(current, "v")

	if !opts.Force && current == pin.Version && current != "" {
		report("OK", "vlt skill %s is current", pin.Version)
		art.State = "ok"
		return art
	}
	if opts.DryRun {
		report("OK", "would install vlt skill %s (currently %s)", pin.Version, orNotInstalled(art.Installed))
		art.State = "would-change"
		return art
	}
	if err := installVltSkill(pin.Repo, pin.Version); err != nil {
		report("FAIL", "vlt skill: %v", err)
		art.State = "failed"
		return art
	}
	report("OK", "vlt skill %s installed to ~/.claude/skills/vlt-skill", pin.Version)
	art.Installed = strings.TrimPrefix(pin.Version, "v")
	art.State = "updated"
	return art
}

func isLocalBin(dir string, probe dirProbe) bool {
	home, err := probe.homeDir()
	if err != nil {
		return false
	}
	return dir == filepath.Join(home, ".local", "bin")
}

func printSummary(out io.Writer, rep Report) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Summary:")
	fmt.Fprintf(out, "  %-22s %-14s %-14s %s\n", "ARTIFACT", "INSTALLED", "CHANNEL", "STATUS")
	for _, a := range rep.Artifacts {
		fmt.Fprintf(out, "  %-22s %-14s %-14s %s\n",
			a.Name, orNotInstalled(a.Installed), orUnknown(a.Channel), a.State)
	}
	if rep.Failed {
		fmt.Fprintln(out, "\nOne or more steps failed.")
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func orNotInstalled(s string) string {
	if s == "" {
		return "(not installed)"
	}
	return s
}

// InstalledToolVersion returns the normalized installed version of a managed
// tool ("" when not installed or the version is unparseable, e.g. dev
// builds). Used by the session-start pin check and nudge.
func InstalledToolVersion(name string) string {
	if name == "pvg" {
		return normalizeVersion(SelfVersion)
	}
	for _, spec := range toolSpecs {
		if spec.Name != name {
			continue
		}
		p, err := lookPath(name)
		if err != nil {
			return ""
		}
		out, err := toolVersionOutput(p, spec.VersionArgs...)
		if err != nil {
			return ""
		}
		return normalizeVersion(out)
	}
	return ""
}

// CheckPins compares installed tool versions against a project toolchain
// pin and returns human-readable warning lines (empty when convergent).
// It never fails: tools that cannot be queried produce a warning line.
func CheckPins(tc paivotcfg.Toolchain) []string {
	pins := []struct{ name, pinned string }{
		{"pvg", tc.Pvg},
		{"nd", tc.Nd},
		{"vlt", tc.Vlt},
	}
	var warnings []string
	for _, p := range pins {
		if p.pinned == "" {
			continue
		}
		installed := InstalledToolVersion(p.name)
		want := strings.TrimPrefix(p.pinned, "v")
		switch {
		case installed == "":
			warnings = append(warnings, fmt.Sprintf(
				"WARNING: toolchain pin: %s pinned at %s but installed version is unknown -- run pvg update", p.name, p.pinned))
		case installed != want:
			warnings = append(warnings, fmt.Sprintf(
				"WARNING: toolchain pin: %s is %s but the project pins %s -- run pvg update", p.name, installed, p.pinned))
		}
	}
	return warnings
}
