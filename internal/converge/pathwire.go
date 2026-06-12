package converge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pathMarker guards the PATH export line in ~/.profile so re-running setup
// never duplicates it.
const pathMarker = "# paivot-managed PATH (pvg setup)"

const pathExportLine = `export PATH="$HOME/.local/bin:$PATH"`

// shResolvesPvg reports whether a bare POSIX shell can resolve pvg. When it
// cannot, ~/.local/bin installs need PATH wiring.
var shResolvesPvg = func() bool {
	return execCommand("sh", "-c", "command -v pvg").Run() == nil
}

// ensurePathWiring appends a marker-guarded PATH export to ~/.profile when
// pvg was installed into ~/.local/bin but a bare shell cannot resolve it.
// Returns (action, error): action describes what happened for the step line.
func ensurePathWiring() (string, error) {
	if shResolvesPvg() {
		return "PATH already resolves pvg", nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	profile := filepath.Join(home, ".profile")

	if data, err := os.ReadFile(profile); err == nil {
		if strings.Contains(string(data), pathMarker) {
			return fmt.Sprintf("%s already contains the paivot PATH line (open a new shell to pick it up)", profile), nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", profile, err)
	}

	f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", profile, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "\n%s\n%s\n", pathMarker, pathExportLine); err != nil {
		return "", fmt.Errorf("append to %s: %w", profile, err)
	}
	return fmt.Sprintf("added ~/.local/bin to PATH in %s -- open a new shell or `. %s` to pick it up", profile, profile), nil
}
