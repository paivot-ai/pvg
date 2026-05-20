# pvg

[![CI](https://github.com/paivot-ai/pvg/actions/workflows/ci.yml/badge.svg)](https://github.com/paivot-ai/pvg/actions/workflows/ci.yml)
[![Release](https://github.com/paivot-ai/pvg/actions/workflows/release.yml/badge.svg)](https://github.com/paivot-ai/pvg/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/paivot-ai/pvg)](https://goreportcard.com/report/github.com/paivot-ai/pvg)

Deterministic control plane for Paivot runtimes with external orchestration surfaces. `pvg` started as the enforcement binary for [paivot-graph](https://github.com/paivot-ai/paivot-graph), and now also owns the shared workflow operations used by Codex and OpenCode: live `nd` routing, deterministic next-step selection, story transitions, merge gating, recovery, and vault governance.

```
pvg init [--force]           # Scaffold .paivot/ provider config in a repo
pvg issues <subcommand>      # Provider-abstracted backlog CLI (12 verbs)
pvg notes <subcommand>       # Provider-abstracted notes CLI (7 verbs)
pvg hook session-start       # Load vault context at session start
pvg guard                    # PreToolUse scope guard (reads JSON from stdin)
pvg nd root --ensure         # Resolve/init the shared live nd vault
pvg nd ready --json          # Run nd against the shared live vault
pvg story verify-delivery ID # Check nd delivery proof completeness
pvg story merge ID           # Merge an accepted story branch
pvg loop next --json         # Select the next deterministic orchestration action
pvg seed [--force]           # Seed vault with agent prompts and conventions
pvg loop setup --all         # Start unattended execution loop
pvg loop snapshot            # Checkpoint active agent/worktree state
pvg loop recover             # Clean up after context loss
pvg worktree remove <path>   # Safely remove a worktree (CWD-independent)
pvg version                  # Print version
```

## Why pvg exists

Paivot needs some parts of the workflow to be structural rather than advisory:

- the live backlog must stay shared across worktrees
- the dispatcher must know what happens next after compaction or agent failure
- delivery and acceptance transitions must be consistent across hosts
- merges, vault writes, and recovery paths must be enforceable instead of remembered

Early versions handled that with shell scripts and prompt conventions. As the system grew, that became too fragile: quoting drift, inconsistent recovery behavior, and too much runtime-critical logic living only in prompts.

`pvg` consolidates that into a single Go binary:

- **Scope guard** -- Blocks direct writes to protected vault directories (methodology/, conventions/, decisions/, etc.), enforcing the proposal workflow. Allows `_inbox/` writes and all `vlt` commands.
- **Session lifecycle** -- Loads vault context at session start, saves knowledge before compaction and stop, logs session end.
- **Dispatcher mode** -- Tracks both D&F agents (BA, Designer, Architect) and execution agents (Sr PM, Developer, PM) so the guard can distinguish responsible-agent writes from orchestrator writes.
- **Execution loop** -- Manages unattended story execution with configurable iteration limits and automatic blocking detection.
- **Deterministic next-action selection** -- Exposes `pvg loop next --json` so Codex/OpenCode dispatchers can ask the same source of truth what should happen next instead of re-implementing delivered/rejected/ready ordering in prompts.
- **Story transitions** -- Exposes `pvg story deliver|accept|reject|verify-delivery|merge` so delivery, acceptance, rejection, and merge gating are shared across runtimes.
- **Vault seeding** -- Writes agent prompts and behavioral notes to the Obsidian vault under an exclusive `vlt` lock to prevent concurrent write corruption.
- **FSM governance** -- Enforces the configured `nd` status pipeline from project settings when enabled.

Today the split is:

- [paivot-graph](https://github.com/paivot-ai/paivot-graph): Claude Code plugin surface, hooks, marketplace packaging
- [paivot-codex](https://github.com/paivot-ai/paivot-codex): Codex skills and prompts, including the `pvg` skill for deterministic workflow operations
- [paivot-opencode](https://github.com/paivot-ai/paivot-opencode): OpenCode commands and agent manifests that call into `pvg`
- `pvg`: shared deterministic workflow engine for the parts that should not live only in prompts

## Installation

### Pre-built binaries

Download from [Releases](https://github.com/paivot-ai/pvg/releases):

```bash
# macOS (Apple Silicon)
gh release download -R paivot-ai/pvg -p '*darwin_arm64*' -D /tmp
tar xzf /tmp/pvg_*_darwin_arm64.tar.gz -C ~/go/bin

# macOS (Intel)
gh release download -R paivot-ai/pvg -p '*darwin_amd64*' -D /tmp
tar xzf /tmp/pvg_*_darwin_amd64.tar.gz -C ~/go/bin

# Linux (amd64)
gh release download -R paivot-ai/pvg -p '*linux_amd64*' -D /tmp
tar xzf /tmp/pvg_*_linux_amd64.tar.gz -C ~/go/bin

# Linux (arm64)
gh release download -R paivot-ai/pvg -p '*linux_arm64*' -D /tmp
tar xzf /tmp/pvg_*_linux_arm64.tar.gz -C ~/go/bin

# Windows (amd64)
gh release download -R paivot-ai/pvg -p '*windows_amd64*' -D %TEMP%
tar xzf %TEMP%/pvg_*_windows_amd64.zip -C $env:GOPATH\bin

# Windows (arm64)
gh release download -R paivot-ai/pvg -p '*windows_arm64*' -D %TEMP%
tar xzf %TEMP%/pvg_*_windows_arm64.zip -C $env:GOPATH\bin
```

### From source (requires Go 1.24+)

```bash
git clone https://github.com/paivot-ai/pvg.git
cd pvg
make build     # produces ./pvg binary
make install   # installs to $GOPATH/bin
```

## Command reference

### Lifecycle hooks

Called by Claude Code via `hooks.json`. Each reads JSON from stdin and writes structured output to stdout.

| Command | Hook event | Description |
|---------|-----------|-------------|
| `pvg hook session-start` | SessionStart | Load vault context, project knowledge, operating mode |
| `pvg hook pre-compact` | PreCompact | Save decisions, patterns, and debug insights before compaction |
| `pvg hook stop` | Stop | Capture session knowledge before ending |
| `pvg hook session-end` | SessionEnd | Log session end, clean up dispatcher state |
| `pvg hook user-prompt` | UserPromptSubmit | Auto-detect and manage dispatcher mode |
| `pvg hook subagent-start` | SubagentStart | Track dispatcher-relevant agent activation (BA, Designer, Architect, Sr PM, Developer, PM) |
| `pvg hook subagent-stop` | SubagentStop | Track dispatcher-relevant agent deactivation and emit mandatory CWD reset guidance for worktree agents |

### Project scaffolding

```bash
pvg init                          # Seed .paivot/config.yaml + .gitignore (idempotent)
pvg init --force                  # Overwrite an existing config
pvg init --with-linear-mirror     # Pre-seed an active Linear backlog mirror
pvg init --with-confluence-mirror # Pre-seed an active Confluence notes mirror
```

`pvg init` creates a `.paivot/` directory in the repo with a heavily-commented
`config.yaml` (provider abstraction config) and a `.gitignore` that hides any
local secrets file. Existing files are preserved unless `--force` is passed.

### Provider configuration

`.paivot/config.yaml` selects the adapters pvg routes through for backlog and
notes operations. With the file absent, pvg behaves identically to the
pre-abstraction CLI (nd backlog + vlt notes, no mirrors).

```yaml
backlog:
  primary:
    adapter: nd
    config:
      vault: .vault
  # mirrors: (visibility-only fan-out; failures logged but never returned)
  #   - adapter: linear
  #     config:
  #       team_key: ENG
  #       api_key_env: LINEAR_API_KEY  # env-var interpolation: ${NAME} or $NAME

notes:
  primary:
    adapter: vlt
    config:
      vault: Claude
```

Reads always go to the primary. Writes go to the primary first; on success
they fan out best-effort to mirrors. Available adapters in this build:
`nd`, `linear` (backlog); `vlt` (notes). Placeholders for `confluence`,
`jira`, `notion` document the planned shape.

### Issues abstraction

```bash
pvg issues create "Title" --body "..." --labels x,y --parent EPIC-1
pvg issues show <id> [--json]
pvg issues list [--status S] [--label L] [--parent ID] [--limit N] [--json]
pvg issues update <id> [--title T] [--body B] [--status S] [--add-label x] [--remove-label x]
pvg issues close <id>
pvg issues reopen <id>
pvg issues comment <id> --body "..."
pvg issues comments <id> [--json]
pvg issues link <from> --blocks <to>           # <from> blocks <to>
pvg issues link <from> --child-of <to>         # <from> is a child of <to>
pvg issues unlink <from> --blocks <to>
pvg issues unlink <from> --child-of <to>
pvg issues ready [--label L] [--limit N] [--json]
pvg issues blocked [--json]
pvg issues prime                                # warm adapter caches / preflight auth
```

Backlog adapter is selected from `.paivot/config.yaml`; defaults to `nd` if
absent. Backend-specific operations (nd cycles, nd stale, nd graph) remain
under `pvg nd` -- see below.

### Notes abstraction

```bash
pvg notes search "<query>" [--limit N] [--json]
pvg notes create <path> [--title T] [--body B] [--prop key=val ...]
pvg notes read <path> [--json]
pvg notes append <path> --body "..."
pvg notes list [--folder F] [--json]
pvg notes property:get <path> <key>
pvg notes property:set <path> <key> <value>
```

Notes adapter is selected from `.paivot/config.yaml`; defaults to `vlt` if
absent. Graph operations on the global vault (`vlt read --follow`, `vlt
orphans`, `vlt unresolved`, `vlt move`, heading-anchored `vlt patch`) are
intentionally vlt-specific and continue to be invoked directly -- they have no
clean cross-backend abstraction.

### nd workflow (backend-specific passthrough)

```bash
pvg nd root --ensure                     # Print/init the nd vault path
pvg nd ready --json                      # Pass through to nd with --vault injected
pvg nd update PROJ-a1b --status=open     # Any nd command works without remembering --vault
pvg nd dep add <issue> <depends-on>      # <issue> depends on <depends-on>
pvg nd dep cycles                        # Detect circular dependencies
pvg nd stale --days=14                   # Surface stale issues
```

`pvg nd` is a thin passthrough: it resolves the correct vault path and injects
`--vault` automatically, then runs `nd <args...>` verbatim. Argument order and
flags match `nd` exactly. Use it for nd-only operations that have no
provider-abstracted equivalent.

#### Vault resolution order

1. `ND_VAULT_DIR` or `PAIVOT_ND_VAULT` environment variable (highest priority)
2. `.vault/.nd-shared.yaml` -- explicit shared worktree vault (points to `git commondir`)
3. Nearest `.vault/` directory walking up the tree (default)

**Local vault is the default.** Shared worktree vaults are opt-in only. To enable shared mode, create `.vault/.nd-shared.yaml`:

```yaml
mode: git_common_dir
path: paivot/nd-vault
```

Without this file, pvg always uses the local `.vault/` directory. Run `pvg doctor` to verify vault resolution is correct.

### Story helpers

```bash
pvg story deliver PROJ-a1b
pvg story accept PROJ-a1b --reason "Accepted: tests and AC matched"
pvg story reject PROJ-a1b --feedback "EXPECTED: ... DELIVERED: ... GAP: ... FIX: ..."
pvg story verify-delivery PROJ-a1b
pvg story merge PROJ-a1b
```

These helpers centralize the common Paivot story transitions, delivery-proof checks, and merge path that used to live in shell scripts.

### Scope guard

```bash
echo '{"tool_name":"Edit","tool_input":{"file_path":"/path/to/file"}}' | pvg guard
```

Exit codes:
- `0` -- Operation allowed
- `2` -- Operation blocked (protected vault path or governance violation)

Two protection layers:
1. **System vault** -- Protects methodology/, conventions/, decisions/, patterns/, debug/, concepts/, projects/, people/. Allows `_inbox/` and `_templates/`.
2. **Project vault** -- Protects `.vault/knowledge/` files. Allows `.settings.yaml`.

`vlt` CLI commands remain the intended path for vault changes. Direct file I/O is blocked because it bypasses advisory locking. `pvg` hook write paths acquire explicit `vlt` locks before mirroring session state.

Additional execution safeguard:
- **Story merge gate** -- In Paivot-managed repos, `git merge story/<STORY_ID>` is blocked until the matching nd issue is both labeled `accepted` and `closed`. This applies during active loops and other Paivot execution flows, even if dispatcher mode is currently off.

### Execution loop

```bash
pvg loop setup --all                    # Run all ready stories
pvg loop setup --epic PROJ-a1b          # Target a specific epic
pvg loop setup --all --max 25           # Limit iterations
pvg loop next --json                    # Decide delivered vs rejected vs ready next
pvg loop status                         # Show loop state
pvg loop cancel                         # Cancel active loop
pvg loop snapshot                       # Checkpoint active agent/worktree state
pvg loop snapshot --agent ID=TYPE       # Include agent assignments
pvg loop recover                        # Clean up after context loss
```

`pvg loop next --json` is the host-agnostic queue selector. It tells Codex/OpenCode-style
dispatchers what to do next without re-implementing the workflow in prompts:

- `decision=act` with a selected story and role (`pm_acceptor` or `developer`)
- `decision=wait` when only in-progress work remains
- `decision=complete` when the backlog is done
- `decision=blocked` when only blocked work remains
- `decision=other` when only non-dispatcher workflow states remain

In `--epic` mode it drains the priority epic first, then falls back to the rest of the backlog.

The selector is intentionally additive. `paivot-graph` keeps its existing Claude hook flow,
while Codex and OpenCode can reuse the same evaluation logic instead of carrying their own
parallel copies of the queue-selection rules.

### Worktree safety

```bash
pvg worktree remove .claude/worktrees/dev-PROJ-a1b          # Remove worktree safely
pvg worktree remove .claude/worktrees/dev-PROJ-a1b --json   # JSON output
```

`pvg worktree remove` resolves the project root from the worktree path itself (by parsing
the `.claude/worktrees/` convention), not from the current working directory. That means the
removal logic itself does not depend on the caller's shell location once `pvg` is running.
It still should be invoked from a healthy shell, typically after `cd $PROJECT_ROOT && pwd`.

After removal, it always runs `git worktree prune` to clean stale metadata. If the worktree
directory is already gone, it prunes instead of erroring.

During dispatcher flows, `pvg hook subagent-stop` intentionally does not delete developer
worktrees anymore. Claude Code may hand control back to the parent session with the just-finished
subagent CWD still active; deleting that worktree inside the hook can strand the parent shell in a
non-existent directory before its next Bash command even starts. The safe sequence is:

```bash
cd $PROJECT_ROOT && pwd
pvg worktree remove .claude/worktrees/dev-PROJ-a1b
```

### Diagnostics

```bash
pvg doctor              # Check vault config, nd reachability, worktree hygiene
pvg doctor --json       # Structured output
pvg doctor --fix        # Auto-repair fixable issues (prune worktrees, nd doctor --fix)
```

Checks: vault-resolution, nd-reachable, shared-config-consistency, nd-doctor, loop-state, worktree-hygiene.

### Dispatcher mode

```bash
pvg dispatcher on       # Enable (orchestrator becomes coordinator-only)
pvg dispatcher off      # Disable
pvg dispatcher status   # Show state and active BLT agents
```

### Vault seeding

```bash
pvg seed              # Bootstrap vault notes (skip if exists)
pvg seed --force      # Overwrite all vault notes with latest content
```

Seeds agent prompts (11 agents: BA, Designer, Architect, the three BLT challengers, Sr PM, Developer, PM, Anchor, Retro), skill content, and behavioral notes (Session Operating Mode, Pre-Compact Checklist, Stop Capture Checklist) into the Obsidian vault.

### Settings

```bash
pvg settings                         # Show all settings
pvg settings stack_detection         # Read one setting
pvg settings stack_detection=true    # Set a value
```

### Other

| Command | Description |
|---------|-------------|
| `pvg version` | Print version |
| `pvg help` | Show usage |

## Architecture

```
cmd/pvg/
  main.go              CLI entry point, argument parsing, command dispatch

internal/
  dispatcher/          Dispatcher mode state management (D&F + execution agent tracking)
  governance/          Vault seeding with vlt lock
  guard/               Scope guard (system vault, project vault, dispatcher, FSM)
  lifecycle/           Session hooks (start, pre-compact, stop, end, user-prompt, subagent)
  loop/                Execution loop (setup, evaluate, cancel, snapshot, recover)
  story/               Shared story transitions, delivery checks, merge path
  worktree/            Safe worktree operations (CWD-independent removal)
  settings/            Project settings (YAML read/write)
  vaultcfg/            Vault discovery and configuration
```

### Dependencies

| Dependency | Purpose |
|-----------|---------|
| [vlt](https://github.com/paivot-ai/vlt) | Obsidian vault operations (library import) |

## Development

```bash
make build    # compile
make test     # run tests (verbose)
make vet      # run go vet
make install  # install to $GOPATH/bin
make clean    # remove build artifacts
```

### Running tests

```bash
go test -v ./...                    # verbose output
go test -cover ./...                # with coverage
go test -run TestCheckFilePath ./.. # run specific test
```

All tests use `t.TempDir()` for isolated environments. No mocks in integration tests.

## Releasing

Tag and push:

```bash
git tag vX.Y.Z   # e.g. v1.53.13
git push origin vX.Y.Z
```

The [release workflow](.github/workflows/release.yml) runs tests, then uses GoReleaser to produce binaries for darwin/linux/windows x amd64/arm64.

## License

Apache License 2.0. See [LICENSE](LICENSE) for full text.
