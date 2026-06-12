package lifecycle

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paivot-ai/pvg/internal/channel"
	"github.com/paivot-ai/pvg/internal/converge"
	"github.com/paivot-ai/pvg/internal/paivotcfg"
	"github.com/paivot-ai/pvg/internal/settings"
)

// Overridable for tests.
var (
	channelForNudge     = channel.ForNudge
	installedPvgVersion = func() string { return converge.InstalledToolVersion("pvg") }
	checkToolchainPins  = converge.CheckPins
)

// emitUpdateChecks prints the toolchain pin warnings and the channel nudge
// at session start. Both are advisory: they never block, never error, and
// the nudge is bounded by the channel cache's short refresh timeout.
func emitUpdateChecks(cwd string) {
	for _, w := range toolchainPinWarnings(cwd) {
		fmt.Println(w)
	}
	if line := channelNudgeLine(cwd); line != "" {
		fmt.Println(line)
	}
}

// toolchainPinWarnings checks installed tool versions against the project's
// optional toolchain pin (.paivot/config.yaml). No pin, no warnings.
func toolchainPinWarnings(cwd string) []string {
	root, err := paivotcfg.LocateProjectRoot(cwd)
	if err != nil {
		root = cwd
	}
	tc, err := paivotcfg.LoadToolchain(root)
	if err != nil || tc == nil {
		return nil
	}
	return checkToolchainPins(*tc)
}

// channelNudgeLine returns the one-line update nudge when the channel pins
// a newer pvg than the one installed, or "" when convergent, opted out
// (update.nudge=false), unverifiable (dev build), or offline with no cache.
func channelNudgeLine(cwd string) string {
	if !updateNudgeEnabled(cwd) {
		return ""
	}
	m, ok := channelForNudge()
	if !ok {
		return ""
	}
	pinned := m.Tools["pvg"].Version
	if pinned == "" {
		return ""
	}
	installed := installedPvgVersion()
	if installed == "" || installed == strings.TrimPrefix(pinned, "v") {
		return ""
	}
	channelName := m.Channel
	if channelName == "" {
		channelName = "stable"
	}
	return fmt.Sprintf("paivot: channel %s has pvg %s (installed %s) -- run pvg update", channelName, pinned, installed)
}

// updateNudgeEnabled reads the update.nudge project setting (default true).
func updateNudgeEnabled(cwd string) bool {
	loaded := settings.LoadFile(filepath.Join(cwd, ".vault", "knowledge", ".settings.yaml"))
	val, ok := loaded["update.nudge"]
	if !ok {
		val = settings.Default("update.nudge")
	}
	return val != "false"
}
