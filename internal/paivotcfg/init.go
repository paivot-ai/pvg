package paivotcfg

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InitOptions controls how Init writes the .paivot scaffolding.
type InitOptions struct {
	// Force overwrites an existing config.yaml. Without it, Init errors when
	// the file already exists.
	Force bool

	// WithLinearMirror writes a commented-out backlog mirror entry pointing
	// at Linear, with placeholder team_key and api_key_env.
	WithLinearMirror bool

	// WithConfluenceMirror writes a commented-out notes mirror for Confluence.
	WithConfluenceMirror bool
}

// InitResult reports what Init created or skipped.
type InitResult struct {
	Dir              string
	ConfigPath       string
	ConfigCreated    bool
	GitignorePath    string
	GitignoreCreated bool
}

// Init creates the .paivot directory under projectRoot and seeds it with a
// documented config.yaml plus a .gitignore that hides any future secrets file.
// It is idempotent: existing files are not overwritten unless opts.Force is set.
func Init(projectRoot string, opts InitOptions) (InitResult, error) {
	if projectRoot == "" {
		return InitResult{}, errors.New("paivotcfg.Init: empty projectRoot")
	}
	dir := filepath.Join(projectRoot, ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InitResult{}, fmt.Errorf("create %s: %w", dir, err)
	}

	res := InitResult{Dir: dir}
	res.ConfigPath = filepath.Join(dir, ConfigFile)
	res.GitignorePath = filepath.Join(dir, ".gitignore")

	created, err := writeIfAbsent(res.ConfigPath, renderDefaultConfig(opts), opts.Force)
	if err != nil {
		return res, fmt.Errorf("write %s: %w", res.ConfigPath, err)
	}
	res.ConfigCreated = created

	created, err = writeIfAbsent(res.GitignorePath, defaultGitignore, false)
	if err != nil {
		return res, fmt.Errorf("write %s: %w", res.GitignorePath, err)
	}
	res.GitignoreCreated = created

	return res, nil
}

// PrintInitResult writes a human-readable summary of an InitResult to w.
func PrintInitResult(w io.Writer, res InitResult) {
	fmt.Fprintf(w, "paivot config dir: %s\n", res.Dir)
	if res.ConfigCreated {
		fmt.Fprintf(w, "  + wrote %s\n", res.ConfigPath)
	} else {
		fmt.Fprintf(w, "  = kept existing %s\n", res.ConfigPath)
	}
	if res.GitignoreCreated {
		fmt.Fprintf(w, "  + wrote %s\n", res.GitignorePath)
	} else {
		fmt.Fprintf(w, "  = kept existing %s\n", res.GitignorePath)
	}
}

func writeIfAbsent(path, content string, force bool) (bool, error) {
	_, err := os.Stat(path)
	if err == nil && !force {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

const defaultGitignore = `# Local-only paivot artifacts that must never be committed.
secrets.yaml
secrets.local.yaml
*.secrets
`

// renderDefaultConfig builds the seeded config.yaml. The body is heavily
// commented so users can pick up the schema without leaving the file.
func renderDefaultConfig(opts InitOptions) string {
	header := `# .paivot/config.yaml -- per-repo provider configuration for pvg.
#
# This file selects which adapters pvg routes through for backlog (issue
# tracker) and notes (knowledge base) operations. Defaults match the
# historical setup: nd backlog, vlt notes, no mirrors. With this file
# absent, pvg behaves identically to the pre-abstraction CLI.
#
# Schema:
#   <section>:
#     primary:
#       adapter: <name>
#       config: { adapter-specific keys }
#     mirrors:
#       - adapter: <name>
#         config: { ... }
#
# Reads always go to the primary. Writes go to the primary first; on
# success they fan out best-effort to mirrors. Mirror failures are logged
# but never returned to the caller -- mirrors are visibility-only.
#
# Env-var interpolation: any string value of the form ${NAME} or $NAME is
# replaced with the value of os.Getenv("NAME") at load time. Use this to
# keep API keys out of the file.

`

	backlogPrimary := `backlog:
  primary:
    adapter: nd
    config:
      vault: .vault
`

	backlogMirrors := ""
	if opts.WithLinearMirror {
		backlogMirrors = `  mirrors:
    - adapter: linear
      config:
        team_key: ENG               # set to your Linear team key
        api_key_env: LINEAR_API_KEY # the env var that holds your Linear token
`
	} else {
		backlogMirrors = `  # mirrors: (uncomment to enable visibility-only fan-out)
  #   - adapter: linear
  #     config:
  #       team_key: ENG
  #       api_key_env: LINEAR_API_KEY
`
	}

	notesPrimary := `
notes:
  primary:
    adapter: vlt
    config:
      vault: Claude
`

	notesMirrors := ""
	if opts.WithConfluenceMirror {
		notesMirrors = `  mirrors:
    - adapter: confluence
      config:
        space_key: ENG
        base_url: https://example.atlassian.net/wiki
        api_key_env: CONFLUENCE_API_KEY
`
	} else {
		notesMirrors = `  # mirrors: (uncomment to enable visibility-only fan-out)
  #   - adapter: confluence
  #     config:
  #       space_key: ENG
  #       base_url: https://example.atlassian.net/wiki
  #       api_key_env: CONFLUENCE_API_KEY
`
	}

	footer := `
# Available adapters in this build:
#   backlog: nd, linear
#   notes:   vlt
# (confluence/jira/notion are not yet implemented; the placeholders above
#  document the planned shape for when they land.)
`

	return header + backlogPrimary + backlogMirrors + notesPrimary + notesMirrors + footer
}
