package converge

import (
	"fmt"
	"path/filepath"
	"strings"
)

// normalizeVersion extracts a bare semver string from a tool's version
// output. Known formats:
//
//	pvg 1.55.0                (release ldflag, no leading v)
//	pvg dev-abc1234 (...)     (source build without a tag)
//	nd version v0.10.20
//	vlt v0.11.0
//	v1.2.3 / 1.2.3            (already bare)
//
// The last whitespace-separated field of the first line is taken and any
// leading "v" stripped. Returns "" when no version-looking token is found.
func normalizeVersion(out string) string {
	line := strings.TrimSpace(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	// Drop a trailing parenthesised suffix like "(2026-04-06T08:34:56Z)".
	if i := strings.Index(line, "("); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	v := strings.TrimPrefix(fields[len(fields)-1], "v")
	if v == "" || !(v[0] >= '0' && v[0] <= '9') {
		return ""
	}
	return v
}

// sameVersion reports whether an installed tool's version output matches a
// pinned release version ("v1.2.3" or "1.2.3").
func sameVersion(installedOutput, pinned string) bool {
	got := normalizeVersion(installedOutput)
	want := strings.TrimPrefix(pinned, "v")
	return got != "" && want != "" && got == want
}

// dirProbe abstracts the filesystem and sudo checks that install-dir
// selection depends on, so planning is unit-testable offline.
type dirProbe struct {
	writable func(dir string) bool
	sudoOK   func() bool
	homeDir  func() (string, error)
}

// installTarget is where a tool binary should be written.
type installTarget struct {
	Dir   string
	Sudo  bool
	Fresh bool // true when the tool was not previously installed
}

const systemBinDir = "/usr/local/bin"

// selectInstallDir applies the hybrid install dir rule:
//   - already installed: replace IN PLACE at its current location, using
//     sudo -n when the location is not writable and sudo -n works;
//   - fresh install: prefer /usr/local/bin when writable or sudo -n is
//     available, else fall back to ~/.local/bin.
func selectInstallDir(installedPath string, probe dirProbe) (installTarget, error) {
	if installedPath != "" {
		dir := filepath.Dir(installedPath)
		if probe.writable(dir) {
			return installTarget{Dir: dir}, nil
		}
		if probe.sudoOK() {
			return installTarget{Dir: dir, Sudo: true}, nil
		}
		return installTarget{}, fmt.Errorf("%s is not writable and passwordless sudo is unavailable", dir)
	}

	if probe.writable(systemBinDir) {
		return installTarget{Dir: systemBinDir, Fresh: true}, nil
	}
	if probe.sudoOK() {
		return installTarget{Dir: systemBinDir, Sudo: true, Fresh: true}, nil
	}
	home, err := probe.homeDir()
	if err != nil {
		return installTarget{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return installTarget{Dir: filepath.Join(home, ".local", "bin"), Fresh: true}, nil
}
