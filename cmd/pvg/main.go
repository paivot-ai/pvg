// pvg is the paivot-graph CLI -- a deterministic replacement for shell hooks
// and scripts. It uses vlt as a library for all vault operations, encoding
// scope guards, proposal workflow, and session lifecycle in Go.
//
// This replaces: vault-scope-guard.sh, vault-session-start.sh,
// vault-pre-compact.sh, vault-stop.sh, vault-session-end.sh, seed-vault.sh
//
// Usage:
//
//	pvg hook session-start       # SessionStart hook
//	pvg hook pre-compact         # PreCompact hook
//	pvg hook stop                # Stop hook
//	pvg hook session-end         # SessionEnd hook
//	pvg guard                    # PreToolUse scope guard (stdin: JSON)
//	pvg nd root --ensure         # Resolve/init shared nd vault
//	pvg nd ready --json          # Run nd against shared live vault
//	pvg nd sync                  # Export live vault to .vault/backlog-snapshot/
//	pvg nd restore [--force]     # Restore live vault from the snapshot
//	pvg seed [--force]           # Seed vault with agent prompts
//	pvg story verify-delivery ID # Check delivery-proof completeness
//	pvg story merge ID           # Merge an accepted story branch
//	pvg settings [key|key=value] # View/read/set project settings
//	pvg loop setup [--all|--epic EPIC_ID] [--max-iterations|--max N]
//	pvg loop cancel              # Cancel active loop
//	pvg loop status              # Show loop state
//	pvg loop next --json         # Select the next orchestration action
//	pvg loop snapshot            # Checkpoint agent/worktree state
//	pvg loop recover             # Clean up after context loss
//	pvg worktree remove <path>   # Safely remove a worktree (CWD-independent)
//	pvg fetch-vlt-skill [--force] # Download and install vlt skill
//	pvg verify [path...] [flags] # Scan for stubs, thin files, TODOs
//	pvg doctor [--json] [--fix]  # Run diagnostic checks
//	pvg version                  # Print version
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/paivot-ai/pvg/internal/dispatcher"
	"github.com/paivot-ai/pvg/internal/doctor"
	"github.com/paivot-ai/pvg/internal/governance"
	"github.com/paivot-ai/pvg/internal/guard"
	"github.com/paivot-ai/pvg/internal/lifecycle"
	plint "github.com/paivot-ai/pvg/internal/lint"
	"github.com/paivot-ai/pvg/internal/loop"
	"github.com/paivot-ai/pvg/internal/ndsync"
	"github.com/paivot-ai/pvg/internal/ndvault"
	"github.com/paivot-ai/pvg/internal/paivotcfg"
	"github.com/paivot-ai/pvg/internal/providercli"
	_ "github.com/paivot-ai/pvg/internal/providers/linearadapter"
	_ "github.com/paivot-ai/pvg/internal/providers/ndadapter"
	_ "github.com/paivot-ai/pvg/internal/providers/vltadapter"
	"github.com/paivot-ai/pvg/internal/rtm"
	"github.com/paivot-ai/pvg/internal/settings"
	"github.com/paivot-ai/pvg/internal/story"
	"github.com/paivot-ai/pvg/internal/vaultcfg"
	"github.com/paivot-ai/pvg/internal/verify"
	"github.com/paivot-ai/pvg/internal/worktree"
)

// Set at build time via -ldflags "-X main.version=..."
// Falls back to VCS info from go build metadata when not set.
var version = ""

func resolvedVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var vcsRev, vcsTime, vcsDirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				vcsRev = s.Value
			case "vcs.time":
				vcsTime = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					vcsDirty = "-dirty"
				}
			}
		}
		if vcsRev != "" {
			short := vcsRev
			if len(short) > 7 {
				short = short[:7]
			}
			v := "dev-" + short + vcsDirty
			if vcsTime != "" {
				v += " (" + vcsTime + ")"
			}
			return v
		}
	}
	return "dev"
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "hook":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "pvg hook: missing subcommand (session-start, pre-compact, stop, session-end)")
			os.Exit(1)
		}
		err = runHook(args[0])
	case "guard":
		err = runGuard()
	case "seed":
		force := len(args) > 0 && args[0] == "--force"
		err = runSeed(force)
	case "init":
		err = runInit(args)
	case "loop":
		err = runLoop(args)
	case "dispatcher":
		err = runDispatcher(args)
	case "nd":
		err = runND(args)
	case "issues":
		err = providercli.RunIssues(args)
	case "notes":
		err = providercli.RunNotes(args)
	case "settings":
		err = settings.Run(args)
	case "story":
		err = runStory(args)
	case "lint":
		err = runLint(args)
	case "rtm":
		err = runRTM(args)
	case "verify":
		err = runVerify(args)
	case "worktree", "wt":
		err = runWorktree(args)
	case "doctor":
		err = runDoctor(args)
	case "fetch-vlt-skill":
		force := len(args) > 0 && (args[0] == "--force" || args[0] == "-f")
		err = lifecycle.FetchVltSkill(force)
	case "version", "--version", "-V":
		fmt.Printf("pvg %s\n", resolvedVersion())
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "pvg: unknown command %q\n", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		if ec, ok := err.(exitCoder); ok {
			os.Exit(ec.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "pvg %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `pvg -- paivot-graph CLI

Commands:
  hook session-start     SessionStart lifecycle hook
  hook pre-compact       PreCompact lifecycle hook
  hook stop              Stop lifecycle hook
  hook session-end       SessionEnd lifecycle hook
  hook user-prompt       UserPromptSubmit hook (auto-detect dispatcher mode)
  hook subagent-start    SubagentStart hook (BLT agent tracking)
  hook subagent-stop     SubagentStop hook (BLT agent tracking)
  hook memory-read       PostToolUse hook (intercept Read on memory files)
  hook memory-write      PostToolUse hook (intercept Write on memory files)
  hook memory-edit       PostToolUse hook (intercept Edit on memory files)
  guard                  PreToolUse scope guard (reads JSON from stdin)
  loop setup [flags]     Start an execution loop (--all, --epic ID, --max[-iterations] N)
  loop cancel            Cancel active execution loop
  loop status            Show execution loop state
  loop next              Select the next orchestration action
  loop snapshot          Checkpoint active agent/worktree state
  loop recover           Clean up after context loss
  dispatcher on|off|status  Manage dispatcher mode
  nd root [--ensure]       Print (and optionally initialize) the shared live nd vault
  nd sync                  Export the live nd vault to .vault/backlog-snapshot/ (git durability)
  nd restore [--force]     Restore the live nd vault from .vault/backlog-snapshot/
  nd <args...>             Run nd against the shared live vault
  issues <subcommand>      Backlog operations via configured adapter (nd, linear, ...)
  notes <subcommand>       Notes operations via configured adapter (vlt, confluence, ...)
  init [flags]           Bootstrap .paivot/config.yaml + .gitignore in this repo
  seed [--force]         Seed vault with agent prompts and conventions
  settings [key|key=value]  View, read, or set project settings
  story <subcommand>        Shared story workflow helpers
  lint [--backlog] [--json] [--epic ID]  Backlog quality checks (collisions + structure)
  rtm [check] [--json]      Requirement Traceability Matrix (D&F coverage check)
  verify [path...] [flags]  Scan source files for stubs, thin files, TODOs
  worktree remove <path>   Safely remove a worktree (CWD-independent) [alias: wt]
  doctor [--json] [--fix]  Run diagnostic checks on vault configuration
  fetch-vlt-skill [--force]  Download and install the vlt skill from GitHub
  version                Print version
	help                   Show this help`)
}

type exitCoder interface {
	ExitCode() int
}

type cliExit struct {
	code int
}

func (e cliExit) Error() string  { return "" }
func (e cliExit) ExitCode() int  { return e.code }
func (e cliExit) String() string { return "" }

func ensureGitRepo(cwd string) error {
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("not a git repository (no .git directory in %s); Paivot requires git for branch management and agent worktrees -- run 'git init' and restart the session", cwd)
	}
	return nil
}

// resolveGitRoot returns the git repository root for the current working
// directory using "git rev-parse --show-toplevel". This anchors state file
// paths to the repo root rather than relying on CWD, which may differ across
// Agent invocations or point to a deleted worktree.
func resolveGitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git root: %w (is this a git repository?)", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureNDInitialized(cwd string) error {
	vaultDir, err := ndvault.Resolve(cwd)
	if err != nil {
		return fmt.Errorf("resolve nd vault: %w", err)
	}

	ndConfigPath := filepath.Join(vaultDir, ".nd.yaml")
	_, err = os.Stat(ndConfigPath)
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("nd is not initialized for this repo (%s missing); initialize nd before using Paivot execution commands", ndConfigPath)
	}
	return fmt.Errorf("check nd initialization: %w", err)
}

func runHook(name string) error {
	switch name {
	case "session-start":
		return lifecycle.SessionStart()
	case "pre-compact":
		return lifecycle.PreCompact()
	case "stop":
		return lifecycle.Stop()
	case "session-end":
		return lifecycle.SessionEnd()
	case "user-prompt":
		return lifecycle.UserPromptSubmit()
	case "subagent-start":
		return lifecycle.SubagentStart()
	case "subagent-stop":
		return lifecycle.SubagentStop()
	case "memory-read":
		return lifecycle.MemoryRead()
	case "memory-write":
		return lifecycle.MemoryWrite()
	case "memory-edit":
		return lifecycle.MemoryEdit()
	default:
		return fmt.Errorf("unknown hook %q", name)
	}
}

func runGuard() error {
	// Parse JSON from stdin -- fail-closed on parse errors to prevent
	// bypasses via malformed input.
	input, err := guard.ParseInput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pvg guard: failed to parse hook input: %v\n", err)
		os.Exit(2)
		return nil // unreachable, for the compiler
	}

	// Determine vault directory. If the global Claude vault is unavailable,
	// keep enforcing project-local protections (.vault/knowledge/, nd FSM,
	// merge gate) and simply skip system-vault checks.
	vaultDir, err := vaultcfg.VaultDir()
	if err != nil {
		vaultDir = ""
	}

	// Get project root (CWD) for project vault checks
	cwd, _ := os.Getwd()

	// Check the operation
	result := guard.Check(vaultDir, cwd, input)
	if !result.Allowed {
		fmt.Fprintln(os.Stderr, result.Reason)
		os.Exit(2)
	}

	return nil
}

func runDispatcher(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: pvg dispatcher on|off|status")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	switch args[0] {
	case "on":
		if err := dispatcher.On(cwd); err != nil {
			return fmt.Errorf("enable dispatcher mode: %w", err)
		}
		fmt.Println("Dispatcher mode enabled.")
	case "off":
		if err := dispatcher.Off(cwd); err != nil {
			return fmt.Errorf("disable dispatcher mode: %w", err)
		}
		fmt.Println("Dispatcher mode disabled.")
	case "status":
		dispatcher.Status(cwd)
	default:
		return fmt.Errorf("unknown dispatcher subcommand %q (use on|off|status)", args[0])
	}
	return nil
}

func runND(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pvg nd root [--ensure] | pvg nd sync | pvg nd restore [--force] | pvg nd <nd-args...>")
	}

	// resolveRoot returns the project root, preferring the main repo root
	// when CWD is inside a worktree. Worktrees have a .vault/ from the git
	// checkout but it lacks gitignored nd runtime data (issues/, .nd.yaml).
	// Using the main repo root ensures vault resolution finds the real nd vault.
	resolveRoot := func() (string, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		if mainRoot, resolveErr := worktree.ResolveProjectRoot(cwd); resolveErr == nil {
			return mainRoot, nil
		}
		return cwd, nil
	}

	if args[0] == "root" {
		projectRoot, err := resolveRoot()
		if err != nil {
			return err
		}
		ensure := false
		for _, arg := range args[1:] {
			if arg == "--ensure" {
				ensure = true
				continue
			}
			return fmt.Errorf("unknown flag %q", arg)
		}
		var vaultDir string
		if ensure {
			vaultDir, err = ndvault.Ensure(projectRoot)
		} else {
			vaultDir, err = ndvault.Resolve(projectRoot)
		}
		if err != nil {
			return err
		}
		fmt.Println(vaultDir)
		return nil
	}

	if args[0] == "sync" {
		if len(args) > 1 {
			return fmt.Errorf("pvg nd sync takes no arguments")
		}
		projectRoot, err := resolveRoot()
		if err != nil {
			return err
		}
		vaultDir, err := ndvault.Resolve(projectRoot)
		if err != nil {
			return fmt.Errorf("resolve nd vault: %w", err)
		}
		res, err := ndsync.Sync(projectRoot, vaultDir)
		if err != nil {
			return err
		}
		fmt.Printf("[ND SYNC] %d issue(s) exported to %s\n", res.Issues, res.SnapshotDir)
		return nil
	}

	if args[0] == "restore" {
		force := false
		for _, arg := range args[1:] {
			if arg == "--force" {
				force = true
				continue
			}
			return fmt.Errorf("unknown flag %q (usage: pvg nd restore [--force])", arg)
		}
		projectRoot, err := resolveRoot()
		if err != nil {
			return err
		}
		vaultDir, err := ndvault.Resolve(projectRoot)
		if err != nil {
			return fmt.Errorf("resolve nd vault: %w", err)
		}
		res, err := ndsync.Restore(projectRoot, vaultDir, force)
		if err != nil {
			return err
		}
		fmt.Printf("[ND RESTORE] %d issue(s) restored to %s\n", res.Issues, res.VaultDir)
		if res.Replaced > 0 {
			fmt.Printf("  Replaced %d pre-existing issue(s) (--force)\n", res.Replaced)
		}
		if res.ConfigRestored {
			fmt.Println("  .nd.yaml restored from snapshot")
		}
		return nil
	}

	for _, arg := range args {
		if arg == "--vault" || strings.HasPrefix(arg, "--vault=") {
			return fmt.Errorf("pvg nd manages --vault automatically; remove explicit --vault")
		}
	}

	projectRoot, err := resolveRoot()
	if err != nil {
		return err
	}

	vaultDir, err := ndvault.Ensure(projectRoot)
	if err != nil {
		return fmt.Errorf("ensure nd vault: %w", err)
	}

	ndArgs := append([]string{"--vault", vaultDir}, args...)
	// #nosec G702 -- intentional argv-based passthrough to nd; no shell interpolation.
	cmd := exec.Command("nd", ndArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return cliExit{code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run nd: %w", err)
	}
	return nil
}

func runStory(args []string) error {
	if len(args) == 0 {
		storyUsage()
		return fmt.Errorf("missing subcommand")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	switch args[0] {
	case "deliver":
		if len(args) != 2 {
			storyUsage()
			return fmt.Errorf("usage: pvg story deliver <story-id>")
		}
		msg, err := story.Transition(cwd, "deliver", args[1], story.TransitionOptions{})
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "accept":
		return runStoryAccept(cwd, args[1:])
	case "reject":
		return runStoryReject(cwd, args[1:])
	case "merge":
		return runStoryMerge(cwd, args[1:])
	case "verify-delivery":
		return runStoryVerifyDelivery(cwd, args[1:])
	case "help", "--help", "-h":
		storyUsage()
		return nil
	default:
		storyUsage()
		return fmt.Errorf("unknown story subcommand %q", args[0])
	}
}

func storyUsage() {
	fmt.Fprintln(os.Stderr, `pvg story -- shared workflow helpers

Subcommands:
  deliver <story-id>                      Mark a story delivered
  accept <story-id> [--reason TEXT] [--next STORY]
                                         Accept and close a story
  reject <story-id> [--feedback TEXT]    Reject a story back to open
  merge <story-id> [--base BRANCH]       Merge an accepted story branch
  verify-delivery <story-id> [--json]    Check delivery proof completeness`)
}

func runStoryAccept(cwd string, args []string) error {
	if len(args) == 0 {
		storyUsage()
		return fmt.Errorf("usage: pvg story accept <story-id> [--reason TEXT] [--next STORY]")
	}

	storyID := args[0]
	opts := story.TransitionOptions{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			if i+1 >= len(args) {
				return fmt.Errorf("--reason requires an argument")
			}
			i++
			opts.Reason = args[i]
		case "--next":
			if i+1 >= len(args) {
				return fmt.Errorf("--next requires an argument")
			}
			i++
			opts.NextStory = args[i]
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	msg, err := story.Transition(cwd, "accept", storyID, opts)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func runStoryReject(cwd string, args []string) error {
	if len(args) == 0 {
		storyUsage()
		return fmt.Errorf("usage: pvg story reject <story-id> [--feedback TEXT]")
	}

	storyID := args[0]
	opts := story.TransitionOptions{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--feedback":
			if i+1 >= len(args) {
				return fmt.Errorf("--feedback requires an argument")
			}
			i++
			opts.Feedback = args[i]
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	msg, err := story.Transition(cwd, "reject", storyID, opts)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func runStoryMerge(cwd string, args []string) error {
	if len(args) == 0 {
		storyUsage()
		return fmt.Errorf("usage: pvg story merge <story-id> [--base BRANCH]")
	}

	storyID := args[0]
	baseBranch := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--base":
			if i+1 >= len(args) {
				return fmt.Errorf("--base requires an argument")
			}
			i++
			baseBranch = args[i]
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	msg, err := story.Merge(cwd, storyID, baseBranch)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return cliExit{code: exitErr.ExitCode()}
		}
		return err
	}
	fmt.Println(msg)
	return nil
}

func runStoryVerifyDelivery(cwd string, args []string) error {
	if len(args) == 0 {
		storyUsage()
		return fmt.Errorf("usage: pvg story verify-delivery <story-id> [--json]")
	}

	storyID := args[0]
	jsonOutput := false
	for _, arg := range args[1:] {
		if arg == "--json" {
			jsonOutput = true
			continue
		}
		return fmt.Errorf("unknown flag %q", arg)
	}

	report, err := story.VerifyDelivery(cwd, storyID)
	if err != nil {
		return err
	}

	if jsonOutput {
		payload, err := report.FormatJSON()
		if err != nil {
			return err
		}
		fmt.Println(payload)
	} else {
		fmt.Print(report.FormatText())
	}

	if report.Failed > 0 {
		return cliExit{code: 1}
	}
	return nil
}

func runLoop(args []string) error {
	if len(args) < 1 {
		loopUsage()
		return fmt.Errorf("missing subcommand")
	}

	// Anchor to git root, not CWD. Agents may run from different directories
	// or from deleted worktrees, causing state file reads/writes to target the
	// wrong path. Using the repo root ensures all invocations share one state.
	cwd, err := resolveGitRoot()
	if err != nil {
		return fmt.Errorf("cannot determine project root: %w", err)
	}

	switch args[0] {
	case "help", "--help", "-h":
		loopUsage()
		return nil
	case "setup":
		return loopSetup(cwd, args[1:])
	case "cancel":
		return loopCancel(cwd)
	case "status":
		return loopStatus(cwd)
	case "next":
		return loopNext(cwd, args[1:])
	case "snapshot":
		return loopSnapshot(cwd, args[1:])
	case "recover":
		return loopRecover(cwd, args[1:])
	case "rotate":
		return loopRotate(cwd, args[1:])
	default:
		loopUsage()
		return fmt.Errorf("unknown loop subcommand %q", args[0])
	}
}

func loopUsage() {
	fmt.Fprintln(os.Stderr, `pvg loop -- execution loop management

Subcommands:
	setup [flags]   Start an execution loop
	cancel          Cancel active execution loop
	status          Show execution loop state
  next [--json] [--n N]  Select the next orchestration action(s) (N = wave size, default 1, max 6)
	rotate EPIC_ID  Rotate loop to the next epic after completion gate
	snapshot        Checkpoint active agent/worktree state
	recover         Clean up after context loss

Setup flags:
  (no flags)               Auto-select highest-priority epic (DEFAULT)
  --epic EPIC_ID           Target a specific epic (or pass EPIC_ID as positional arg)
  --all                    Legacy: run across all epics (no containment)
  --max-iterations N       Max iterations before stopping (default: 50, 0 for unlimited)
  --max N                  Alias for --max-iterations
  --help, -h               Show this help

Snapshot flags:
  --agent ID=TYPE          Agent assignment (repeatable, e.g. --agent PROJ-a1b=developer)
  --json                   Output as JSON

Next flags:
  --all                    Resolve next action against the whole backlog
  --epic EPIC_ID           Prefer a priority epic, then fall back to the backlog
  --n N                    Select up to N distinct-story actions (wave; default 1, max 6)
  --json                   Output as JSON

Recover flags:
  --json                   Output as JSON

Examples:
  pvg loop setup --all
  pvg loop setup --epic PROJ-a1b
  pvg loop next --json
  pvg loop next --epic PROJ-a1b --json
  pvg loop setup PROJ-a1b --max 10
  pvg loop setup --all --max-iterations 25
  pvg loop snapshot --agent PROJ-a1b=developer
  pvg loop recover`)
}

func loopSetup(cwd string, args []string) error {
	// Parse flags manually (consistent with pvg pattern, no cobra)
	var (
		mode    = ""
		epicID  = ""
		maxIter = 50 // default
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			loopUsage()
			return nil
		case "--all":
			mode = "all"
		case "--epic":
			if i+1 >= len(args) {
				return fmt.Errorf("--epic requires an argument")
			}
			i++
			epicID = args[i]
			mode = "epic"
		case "--max-iterations", "--max":
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires an argument", args[i])
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return fmt.Errorf("--max-iterations must be a non-negative integer")
			}
			maxIter = n
		default:
			// Reject unknown flags before positional fallback
			if len(args[i]) > 1 && args[i][0] == '-' {
				loopUsage()
				return fmt.Errorf("unknown flag %q", args[i])
			}
			// Positional argument -- treat as epic ID
			if mode == "" {
				epicID = args[i]
				mode = "epic"
			} else {
				loopUsage()
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}

	if err := ensureGitRepo(cwd); err != nil {
		return err
	}

	if err := ensureNDInitialized(cwd); err != nil {
		return err
	}

	// Idempotent: if already active, report status and return success
	if loop.IsActive(cwd) {
		fmt.Println("[LOOP] Execution loop already active -- no changes made.")
		return loopStatus(cwd)
	}

	autoRotate := false

	// Default: auto-select the highest-priority epic with actionable work.
	if mode == "" {
		selectedID, selectedTitle, err := loop.AutoSelectEpic(cwd)
		if err != nil {
			return fmt.Errorf("auto-select epic: %w", err)
		}
		if selectedID == "" {
			return fmt.Errorf("no actionable epics found; create epics with stories first, or use --all for global mode")
		}
		epicID = selectedID
		mode = "epic"
		autoRotate = true
		fmt.Printf("[LOOP] Auto-selected epic: %s", selectedID)
		if selectedTitle != "" {
			fmt.Printf(" (%s)", selectedTitle)
		}
		fmt.Println()
	}

	// Validate epic if specified (covers both auto-selected and explicit --epic).
	if mode == "epic" {
		if err := loop.ValidateEpic(cwd, epicID); err != nil {
			return fmt.Errorf("validate epic: %w", err)
		}
	}

	state := loop.NewState(mode, epicID, maxIter)
	state.AutoRotate = autoRotate || mode == "epic" // always rotate in epic mode

	if err := loop.WriteState(cwd, state); err != nil {
		return fmt.Errorf("write loop state: %w", err)
	}

	fmt.Println("[LOOP] Execution loop activated.")
	fmt.Printf("  Mode: %s (epic-at-a-time, auto-rotate=%v)\n", mode, state.AutoRotate)
	if epicID != "" {
		fmt.Printf("  Target: %s\n", epicID)
	}
	if maxIter > 0 {
		fmt.Printf("  Max iterations: %d\n", maxIter)
	} else {
		fmt.Println("  Max iterations: unlimited")
	}
	return nil
}

func loopCancel(cwd string) error {
	if !loop.IsActive(cwd) {
		fmt.Println("[LOOP] No active loop to cancel.")
		return nil
	}

	state, _ := loop.ReadState(cwd)
	if err := loop.RemoveState(cwd); err != nil {
		return fmt.Errorf("remove loop state: %w", err)
	}

	fmt.Println("[LOOP] Execution loop cancelled.")
	if state != nil {
		fmt.Printf("  Completed iterations: %d\n", state.Iteration)
		fmt.Printf("  Wait iterations: %d\n", state.WaitIterations)
	}
	return nil
}

func loopStatus(cwd string) error {
	state, err := loop.ReadState(cwd)
	if err != nil {
		fmt.Println("[LOOP] No active loop.")
		return nil
	}

	if !state.Active {
		fmt.Println("[LOOP] Loop state exists but is inactive.")
		return nil
	}

	fmt.Println("[LOOP] Execution loop active.")
	fmt.Printf("  Mode: %s (auto-rotate=%v)\n", state.Mode, state.AutoRotate)
	if state.TargetEpic != "" {
		fmt.Printf("  Target: %s\n", state.TargetEpic)
	}
	if len(state.CompletedEpics) > 0 {
		fmt.Printf("  Completed epics: %v\n", state.CompletedEpics)
	}
	fmt.Printf("  Iteration: %d", state.Iteration)
	if state.MaxIterations > 0 {
		fmt.Printf(" / %d", state.MaxIterations)
	}
	fmt.Println()
	fmt.Printf("  Consecutive waits: %d / %d\n", state.ConsecutiveWaits, state.MaxConsecutiveWaits)
	fmt.Printf("  Total wait iterations: %d\n", state.WaitIterations)
	fmt.Printf("  Started: %s\n", state.StartedAt)
	return nil
}

func loopNext(cwd string, args []string) error {
	jsonOutput := false
	mode := ""
	targetEpic := ""
	scopeSource := "default"
	activeLoop := false
	scopeRoot := cwd
	waveSize := 1

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			loopUsage()
			return nil
		case "--json":
			jsonOutput = true
		case "--all":
			mode = "all"
			targetEpic = ""
			scopeSource = "flag"
		case "--epic":
			if i+1 >= len(args) {
				return fmt.Errorf("--epic requires an argument")
			}
			i++
			mode = "epic"
			targetEpic = args[i]
			scopeSource = "flag"
		case "--n":
			if i+1 >= len(args) {
				return fmt.Errorf("--n requires an argument")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return fmt.Errorf("--n must be a positive integer")
			}
			if n > loop.MaxWaveSize {
				n = loop.MaxWaveSize
			}
			waveSize = n
		default:
			if len(args[i]) > 1 && args[i][0] == '-' {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			return fmt.Errorf("unexpected argument: %s", args[i])
		}
	}

	if mode == "" {
		if state, root, err := loop.ReadStateRoot(cwd); err == nil && state.Active {
			mode = state.Mode
			targetEpic = state.TargetEpic
			scopeRoot = root
			scopeSource = "loop_state"
			activeLoop = true
		} else {
			mode = "all"
		}
	}

	result, err := loop.EvaluateNext(scopeRoot, mode, targetEpic, waveSize)
	if err != nil {
		return err
	}
	result.ActiveLoop = activeLoop
	result.ScopeSource = scopeSource

	if jsonOutput {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal next action: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("[NEXT] decision=%s scope=%s", result.Decision, result.ScopeSource)
	if result.TargetEpic != "" {
		fmt.Printf(" target=%s", result.TargetEpic)
	}
	fmt.Println()
	fmt.Printf("  Reason: %s\n", result.Reason)
	fmt.Printf("  Counts: delivered=%d rejected=%d ready=%d in_progress=%d blocked=%d other=%d\n",
		result.Counts.Delivered, result.Counts.Rejected, result.Counts.Ready,
		result.Counts.InProgress, result.Counts.Blocked, result.Counts.Other)
	if result.Next != nil {
		fmt.Printf("  Next: %s %s (%s, scope=%s", result.Next.Role, result.Next.StoryID, result.Next.Queue, result.Next.Scope)
		if result.Next.Phase != "" {
			fmt.Printf(", phase=%s", result.Next.Phase)
		}
		if result.Next.HardTDD {
			fmt.Print(", hard-tdd")
		}
		fmt.Println(")")
	}
	if len(result.Actions) > 1 {
		fmt.Printf("  Wave (%d actions):\n", len(result.Actions))
		for _, action := range result.Actions {
			fmt.Printf("    %s %s (%s)\n", action.Role, action.StoryID, action.Queue)
		}
	}
	if result.NextEpic != "" {
		fmt.Printf("  Rotate to: %s", result.NextEpic)
		if result.NextEpicTitle != "" {
			fmt.Printf(" (%s)", result.NextEpicTitle)
		}
		fmt.Println()
	}
	return nil
}

func loopRotate(cwd string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pvg loop rotate requires EPIC_ID argument")
	}
	epicID := args[0]

	if err := loop.ValidateEpic(cwd, epicID); err != nil {
		return fmt.Errorf("validate epic: %w", err)
	}

	state, err := loop.ReadState(cwd)
	if err != nil {
		return fmt.Errorf("read loop state: %w", err)
	}

	prev := state.TargetEpic
	if err := loop.Rotate(cwd, epicID); err != nil {
		return err
	}

	fmt.Printf("[LOOP] Rotated: %s -> %s\n", prev, epicID)
	return nil
}

func loopSnapshot(cwd string, args []string) error {
	agentAssignments := make(map[string]string)
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			loopUsage()
			return nil
		case "--json":
			jsonOutput = true
		case "--agent":
			if i+1 >= len(args) {
				return fmt.Errorf("--agent requires ID=TYPE argument")
			}
			i++
			parts := strings.SplitN(args[i], "=", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--agent value must be ID=TYPE (e.g. PROJ-a1b=developer)")
			}
			agentAssignments[parts[0]] = parts[1]
		default:
			if len(args[i]) > 1 && args[i][0] == '-' {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			return fmt.Errorf("unexpected argument: %s", args[i])
		}
	}

	snap, err := loop.BuildSnapshot(cwd, agentAssignments)
	if err != nil {
		return fmt.Errorf("build snapshot: %w", err)
	}

	if err := loop.WriteSnapshot(cwd, snap); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	if jsonOutput {
		data, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal snapshot: %w", err)
		}
		fmt.Println(string(data))
	} else {
		fmt.Printf("[SNAPSHOT] Saved at %s\n", snap.TakenAt)
		fmt.Printf("  Stories: %d\n", len(snap.Stories))
		for _, s := range snap.Stories {
			agent := s.AgentType
			if agent == "" {
				agent = "-"
			}
			wt := s.WorktreePath
			if wt == "" {
				wt = "(none)"
			}
			fmt.Printf("  %s  agent=%s  worktree=%s\n", s.StoryID, agent, wt)
		}
	}

	return nil
}

func loopRecover(cwd string, args []string) error {
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			loopUsage()
			return nil
		case "--json":
			jsonOutput = true
		default:
			if len(args[i]) > 1 && args[i][0] == '-' {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			return fmt.Errorf("unexpected argument: %s", args[i])
		}
	}

	cfg, err := loop.BuildRecoverConfig(cwd)
	if err != nil {
		return fmt.Errorf("build recover config: %w", err)
	}

	plan := loop.EvaluateRecover(cfg)
	execErrors := loop.ExecuteRecover(cwd, plan)

	// Remove snapshot after recovery
	if err := loop.RemoveSnapshot(cwd); err != nil {
		execErrors = append(execErrors, fmt.Sprintf("remove snapshot: %v", err))
	}

	if jsonOutput {
		report := struct {
			Summary  loop.RecoverSummary  `json:"summary"`
			Actions  []loop.RecoverAction `json:"actions"`
			Warnings []string             `json:"warnings,omitempty"`
			Errors   []string             `json:"errors,omitempty"`
		}{
			Summary:  plan.Summary,
			Actions:  plan.Actions,
			Warnings: cfg.Warnings,
			Errors:   execErrors,
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal report: %w", err)
		}
		fmt.Println(string(data))
	} else {
		fmt.Println("[RECOVER] Recovery complete.")
		fmt.Printf("  Worktrees removed: %d\n", plan.Summary.WorktreesRemoved)
		fmt.Printf("  Branches deleted:  %d\n", plan.Summary.BranchesDeleted)
		fmt.Printf("  Branches preserved: %d (delivered/accepted, not merged)\n", plan.Summary.BranchesPreserved)
		fmt.Printf("  Stale branches:    %d\n", plan.Summary.StaleBranchesDeleted)
		fmt.Printf("  Stories reset:     %d\n", plan.Summary.StoriesReset)
		fmt.Printf("  Stories delivered:  %d (needs PM review)\n", plan.Summary.StoriesDelivered)
		fmt.Printf("  Orphan worktrees:  %d\n", plan.Summary.OrphanWorktrees)
		for _, action := range plan.Actions {
			if action.Kind == loop.ActionPreserveBranch {
				fmt.Printf("    preserved: %s (%s)\n", action.BranchName, action.StoryID)
			}
		}
		if len(cfg.Warnings) > 0 {
			fmt.Printf("  Warnings: %d\n", len(cfg.Warnings))
			for _, w := range cfg.Warnings {
				fmt.Printf("    - %s\n", w)
			}
		}
		if len(execErrors) > 0 {
			fmt.Printf("  Errors: %d\n", len(execErrors))
			for _, e := range execErrors {
				fmt.Printf("    - %s\n", e)
			}
		}
	}

	return nil
}

func runLint(args []string) error {
	jsonOutput := false
	backlogMode := false
	epicID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--backlog":
			backlogMode = true
		case "--epic":
			if i+1 >= len(args) {
				return fmt.Errorf("--epic requires an argument")
			}
			i++
			epicID = args[i]
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, `pvg lint -- deterministic backlog quality checks

Default mode scans all non-closed stories for PRODUCES blocks and flags any
artifact (file path) claimed by more than one story.

--backlog runs the artifact-collision check PLUS all backlog structure
checks: walking-skeleton, capstone, mandatory-skills, consumes-signature,
consumes-produces, stale-refs, external-integration, atomicity,
vertical-slice, dep-cycles, release-gate, and paths-exist (brownfield only).
Findings are 'error' (must fix; exit 1) or 'review' (judgment flag; exit 0).

Usage:
  pvg lint [--json]
  pvg lint --backlog [--json] [--epic EPIC_ID]

Flags:
  --backlog        Run the full backlog structure check suite
  --epic EPIC_ID   Scope story-level checks to one epic's children
  --json           Output as JSON
  --help           Show this help`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	if epicID != "" && !backlogMode {
		return fmt.Errorf("--epic requires --backlog")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	vaultDir, err := ndvault.Resolve(cwd)
	if err != nil {
		return fmt.Errorf("resolve nd vault: %w", err)
	}

	if backlogMode {
		projectRoot, err := resolveGitRoot()
		if err != nil {
			projectRoot = cwd
		}

		result, err := plint.CheckBacklog(plint.BacklogOptions{
			VaultDir:    vaultDir,
			ProjectRoot: projectRoot,
			EpicID:      epicID,
		})
		if err != nil {
			return err
		}

		if jsonOutput {
			out, err := plint.FormatBacklogJSON(result)
			if err != nil {
				return err
			}
			fmt.Println(out)
		} else {
			fmt.Print(plint.FormatBacklogText(result))
		}

		if result.Errors > 0 {
			return cliExit{code: 1}
		}
		return nil
	}

	result, err := plint.CheckArtifactCollisions(vaultDir)
	if err != nil {
		return err
	}

	if jsonOutput {
		out, err := plint.FormatJSON(result)
		if err != nil {
			return err
		}
		fmt.Println(out)
	} else {
		fmt.Print(plint.FormatText(result))
	}

	if !result.Passed {
		return cliExit{code: 1}
	}
	return nil
}

func runRTM(args []string) error {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "check", "": // "check" is the default subcommand
			continue
		case "--json":
			jsonOutput = true
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, `pvg rtm -- Requirement Traceability Matrix

Reads BUSINESS.md, DESIGN.md, and ARCHITECTURE.md for tagged requirements
([NEW], [EXPANDED], [CRITICAL], [REQUIRED], [CHANGED]) and checks that each
has a covering story in the nd backlog.

Usage:
  pvg rtm [check] [--json]

Flags:
  --json    Output as JSON
  --help    Show this help`)
			return nil
		default:
			return fmt.Errorf("unknown argument %q", arg)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	vaultDir, err := ndvault.Resolve(cwd)
	if err != nil {
		return fmt.Errorf("resolve nd vault: %w", err)
	}

	result, err := rtm.CheckCoverage(cwd, vaultDir)
	if err != nil {
		return err
	}

	if jsonOutput {
		out, err := rtm.FormatJSON(result)
		if err != nil {
			return err
		}
		fmt.Println(out)
	} else {
		fmt.Print(rtm.FormatText(result))
	}

	if !result.Passed {
		return cliExit{code: 1}
	}
	return nil
}

func runVerify(args []string) error {
	opts := verify.DefaultOptions()
	format := "text"
	checkE2e := false
	var paths []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, `pvg verify -- scan source files for quality issues

Usage: pvg verify [path...] [flags]

Flags:
  --format text|json    Output format (default: text)
  --min-lines N         Minimum lines of code for substance check (default: 10)
  --include-tests       Include test files in scan (default: skip them)
  --check-e2e           Check that e2e test files exist (exit 1 if none found)
  --help, -h            Show this help

If no paths given, scans the current directory recursively.
Skips test files, vendor/, node_modules/, .git/, .vault/ by default.

Exit code 0 if clean, 1 if issues found.`)
			return nil
		case "--check-e2e":
			checkE2e = true
		case "--format":
			if i+1 >= len(args) {
				return fmt.Errorf("--format requires an argument (text or json)")
			}
			i++
			format = args[i]
			if format != "text" && format != "json" {
				return fmt.Errorf("--format must be text or json")
			}
		case "--min-lines":
			if i+1 >= len(args) {
				return fmt.Errorf("--min-lines requires a number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return fmt.Errorf("--min-lines must be a positive integer")
			}
			opts.MinLines = n
		case "--include-tests":
			opts.IncludeTests = true
		default:
			if len(args[i]) > 1 && args[i][0] == '-' {
				return fmt.Errorf("unknown flag %q (see pvg verify --help)", args[i])
			}
			paths = append(paths, args[i])
		}
	}

	// E2e existence check mode
	if checkE2e {
		root := "."
		if len(paths) > 0 {
			root = paths[0]
		}
		e2eResult, err := verify.CheckE2e(root)
		if err != nil {
			return err
		}
		switch format {
		case "json":
			j, err := json.MarshalIndent(e2eResult, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(j))
		default:
			fmt.Print(verify.FormatE2eText(e2eResult))
		}
		if !e2eResult.Found {
			os.Exit(1)
		}
		return nil
	}

	result, err := verify.Scan(paths, opts)
	if err != nil {
		return err
	}

	switch format {
	case "json":
		j, err := verify.FormatJSON(result)
		if err != nil {
			return err
		}
		fmt.Println(j)
	default:
		fmt.Print(verify.FormatText(result))
	}

	if !result.Passed {
		os.Exit(1)
	}
	return nil
}

func runWorktree(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `pvg worktree -- safe worktree operations

Subcommands:
  remove <path> [--json]   Remove a worktree safely (resolves project root from path, not CWD)

The remove command uses the worktree path to find the project root, so it works
without relying on the caller's current shell location once the command starts. It always prunes
stale worktree metadata after removal.`)
		return cliExit{code: 1}
	}

	switch args[0] {
	case "remove":
		return worktreeRemove(args[1:])
	case "--help", "-h":
		fmt.Fprintln(os.Stderr, `pvg worktree -- safe worktree operations

Subcommands:
  remove <path> [--json]   Remove a worktree safely`)
		return nil
	default:
		return fmt.Errorf("unknown worktree subcommand %q (try: remove)", args[0])
	}
}

func worktreeRemove(args []string) error {
	jsonOutput := false
	var wtPath string

	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonOutput = true
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(os.Stderr, `pvg worktree remove <path> [--json]

Safely remove a git worktree. Resolves the project root from the worktree path
itself (not from CWD), so the removal logic does not depend on the caller's shell location.

After removal, runs git worktree prune to clean up stale metadata.

If the worktree directory is already gone, prunes stale metadata instead.
Reset to the project root before invoking this command; a host shell whose CWD
already points at a deleted directory may fail before pvg can start.`)
			return nil
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			if wtPath != "" {
				return fmt.Errorf("expected exactly one worktree path, got extra argument %q", arg)
			}
			wtPath = arg
		}
	}

	if wtPath == "" {
		return fmt.Errorf("missing worktree path (usage: pvg worktree remove <path>)")
	}

	result := worktree.SafeRemove(wtPath)

	if jsonOutput {
		fmt.Println(result.FormatJSON())
	} else {
		fmt.Println(result.FormatText())
	}

	if result.Error != "" {
		return cliExit{code: 1}
	}
	return nil
}

func runDoctor(args []string) error {
	jsonOutput := false
	fix := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--fix":
			fix = true
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, `pvg doctor -- diagnostic checks for vault configuration

Checks:
  vault-resolution          Verify nd vault resolves and contains .nd.yaml
  nd-reachable              Verify nd binary is on PATH
  shared-config-consistency Check shared vault config consistency
  nd-doctor                 Run nd doctor and report findings
  loop-state                Verify loop state file is valid
  worktree-hygiene          Check for stale git worktrees

Flags:
  --json    Output as JSON
  --fix     Auto-fix fixable issues (prune worktrees, rehash nd issues)
  --help    Show this help`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q (see pvg doctor --help)", arg)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	report := doctor.RunAll(cwd)

	if fix {
		actions := doctor.Fix(cwd, report)
		for _, a := range actions {
			fmt.Fprintf(os.Stderr, "[FIX] %s\n", a)
		}
		// Re-run checks after fix for accurate post-fix report.
		report = doctor.RunAll(cwd)
	}

	if jsonOutput {
		out, jsonErr := doctor.FormatJSON(report)
		if jsonErr != nil {
			return jsonErr
		}
		fmt.Println(out)
	} else {
		fmt.Print(doctor.FormatText(report))
	}

	if !report.Passed {
		return cliExit{code: 1}
	}
	return nil
}

func runInit(args []string) error {
	opts := paivotcfg.InitOptions{}
	for _, a := range args {
		switch a {
		case "--force", "-f":
			opts.Force = true
		case "--with-linear-mirror":
			opts.WithLinearMirror = true
		case "--with-confluence-mirror":
			opts.WithConfluenceMirror = true
		case "--help", "-h":
			fmt.Fprintln(os.Stdout, `pvg init [flags]

Bootstrap the .paivot/ directory in this repo with a documented config.yaml
plus a .gitignore that hides any secrets file. Idempotent: existing files
are left alone unless --force is passed.

Flags:
  --force, -f                  overwrite an existing config.yaml
  --with-linear-mirror         seed an active Linear backlog mirror
  --with-confluence-mirror     seed an active Confluence notes mirror`)
			return nil
		default:
			return fmt.Errorf("init: unknown flag %q (try --help)", a)
		}
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	res, err := paivotcfg.Init(root, opts)
	if err != nil {
		return err
	}
	paivotcfg.PrintInitResult(os.Stdout, res)
	return nil
}

func runSeed(force bool) error {
	pluginDir := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginDir == "" {
		// Try to find it relative to the pvg binary
		exe, err := os.Executable()
		if err == nil {
			// bin/pvg -> plugin root is ../
			candidate := filepath.Dir(filepath.Dir(exe))
			if _, serr := os.Stat(filepath.Join(candidate, ".claude-plugin")); serr == nil {
				pluginDir = candidate
			}
		}
	}
	return governance.Seed(force, pluginDir)
}
