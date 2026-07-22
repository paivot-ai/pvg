package guard

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ndUpgradeRe matches `nd upgrade` invocations anchored at the start of a
// command (or after ;|&), with an optional path prefix and an optional `pvg `
// wrapper, so `pvg nd upgrade` hits the same check as bare `nd upgrade`.
// Reuses ndCmdPrefix from fsm.go.
var ndUpgradeRe = regexp.MustCompile(ndCmdPrefix + `upgrade(?:\s|$)`)

const ndUpgradeBlockMsg = "BLOCKED: nd is version-pinned by the Paivot channel manifest. " +
	"Run 'pvg update' to converge the toolchain instead of 'nd upgrade'."

// CheckNDUpgrade blocks `nd upgrade` (and `pvg nd upgrade`) inside a
// Paivot-managed project. nd's self-updater conflicts with pvg's
// channel-pinned convergence: the manifest pins the nd version, and a
// self-updated binary silently drifts from it. Always on for Paivot projects,
// deliberately NOT gated on dispatcher mode -- the pin must hold in normal
// sessions too.
func CheckNDUpgrade(projectRoot, command string) Result {
	if projectRoot == "" || command == "" {
		return Result{Allowed: true}
	}

	// Quick negative: skip filesystem probes and regex when the command
	// cannot possibly be an upgrade.
	if !strings.Contains(command, "upgrade") {
		return Result{Allowed: true}
	}

	if !isPaivotManagedProject(projectRoot) {
		return Result{Allowed: true}
	}

	if ndUpgradeRe.MatchString(command) {
		return Result{Allowed: false, Reason: ndUpgradeBlockMsg}
	}

	return Result{Allowed: true}
}

// isPaivotManagedProject reports whether projectRoot (or any ancestor) is a
// Paivot-managed project: it contains .vault/issues/ or .paivot/config.yaml.
func isPaivotManagedProject(projectRoot string) bool {
	if _, _, found := findAncestorPath(projectRoot, filepath.Join(".vault", "issues")); found {
		return true
	}
	_, _, found := findAncestorPath(projectRoot, filepath.Join(".paivot", "config.yaml"))
	return found
}
