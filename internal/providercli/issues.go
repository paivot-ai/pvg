// Package providercli wires the provider abstraction into the pvg CLI.
// It exposes `pvg issues ...` and `pvg notes ...` subcommands that load the
// per-repo .paivot/config.yaml, build a primary+mirror router, and dispatch
// each operation through it.
package providercli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paivot-ai/pvg/internal/paivotcfg"
	"github.com/paivot-ai/pvg/internal/providers"
)

// RunIssues handles `pvg issues <subcommand> [args...]`.
func RunIssues(args []string) error {
	if len(args) == 0 {
		issuesUsage(os.Stderr)
		return fmt.Errorf("missing subcommand")
	}
	router, err := openBacklog()
	if err != nil {
		return err
	}
	ctx := context.Background()

	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return issuesCreate(ctx, router, rest)
	case "show":
		return issuesShow(ctx, router, rest)
	case "list":
		return issuesList(ctx, router, rest)
	case "update":
		return issuesUpdate(ctx, router, rest)
	case "close":
		return issuesClose(ctx, router, rest)
	case "reopen":
		return issuesReopen(ctx, router, rest)
	case "comment":
		return issuesComment(ctx, router, rest)
	case "comments":
		return issuesComments(ctx, router, rest)
	case "link":
		return issuesLink(ctx, router, rest, false)
	case "unlink":
		return issuesLink(ctx, router, rest, true)
	case "ready":
		return issuesReady(ctx, router, rest)
	case "blocked":
		return issuesBlocked(ctx, router)
	case "prime":
		return issuesPrime(ctx, router)
	case "help", "-h", "--help":
		issuesUsage(os.Stdout)
		return nil
	default:
		issuesUsage(os.Stderr)
		return fmt.Errorf("unknown issues subcommand %q", sub)
	}
}

func issuesUsage(w io.Writer) {
	fmt.Fprintln(w, `pvg issues -- normalized issue tracker CLI

Subcommands:
  create [title] [--body B] [--labels x,y] [--parent ID] [--assignee A] [--blocked-by IDs]
  show <id> [--json]
  list [--status S] [--label L] [--parent ID] [--limit N] [--json]
  update <id> [--title T] [--body B] [--status S] [--add-label x] [--remove-label x]
  close <id>
  reopen <id>
  comment <id> --body B
  comments <id> [--json]
  link <from> --blocks <to>
  link <from> --child-of <to>
  unlink <from> --blocks <to>
  unlink <from> --child-of <to>
  ready [--label L] [--limit N] [--json]
  blocked [--json]
  prime

Adapter selected from .paivot/config.yaml; defaults to nd if absent.`)
}

func openBacklog() (*providers.BacklogRouter, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := paivotcfg.LocateProjectRoot(cwd)
	if err != nil {
		root = cwd // fall back to cwd if no project root found
	}
	cfg, err := paivotcfg.Load(root)
	if err != nil {
		return nil, err
	}
	primary, err := providers.BuildBacklog(cfg.Backlog.Primary.Adapter, cfg.Backlog.Primary.Config)
	if err != nil {
		return nil, fmt.Errorf("build primary backlog: %w", err)
	}
	mirrors := make([]providers.BacklogAdapter, 0, len(cfg.Backlog.Mirrors))
	for _, m := range cfg.Backlog.Mirrors {
		mirror, err := providers.BuildBacklog(m.Adapter, m.Config)
		if err != nil {
			return nil, fmt.Errorf("build mirror backlog %q: %w", m.Adapter, err)
		}
		mirrors = append(mirrors, mirror)
	}
	return providers.NewBacklogRouter(primary, mirrors, nil), nil
}

func issuesCreate(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	body := fs.String("body", "", "issue body / description")
	labelsCSV := fs.String("labels", "", "comma-separated labels")
	parent := fs.String("parent", "", "parent epic ID")
	assignee := fs.String("assignee", "", "assignee")
	blockedByCSV := fs.String("blocked-by", "", "comma-separated blocker IDs")
	jsonOut := fs.Bool("json", false, "emit JSON")
	known := map[string]bool{"body": true, "labels": true, "parent": true, "assignee": true, "blocked-by": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	title := strings.Join(fs.Args(), " ")
	if title == "" {
		return fmt.Errorf("create requires a title")
	}
	in := providers.CreateIssueInput{
		Title:     title,
		Body:      *body,
		Labels:    splitCSV(*labelsCSV),
		Parent:    *parent,
		Assignee:  *assignee,
		BlockedBy: splitCSV(*blockedByCSV),
	}
	out, err := r.Create(ctx, in)
	if err != nil {
		return err
	}
	return printIssue(out, *jsonOut)
}

func issuesShow(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderArgs(nil, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("show requires an issue ID")
	}
	got, err := r.Show(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printIssue(got, *jsonOut)
}

func issuesList(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	status := fs.String("status", "", "filter by status (open|in_progress|blocked|deferred|closed)")
	label := fs.String("label", "", "filter by label")
	parent := fs.String("parent", "", "filter by parent ID")
	limit := fs.Int("limit", 0, "max results (0 = unlimited)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	known := map[string]bool{"status": true, "label": true, "parent": true, "limit": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	f := providers.ListFilter{Parent: *parent, Limit: *limit}
	if *status != "" {
		f.Status = []providers.Status{providers.Status(*status)}
	}
	if *label != "" {
		f.Labels = []string{*label}
	}
	got, err := r.List(ctx, f)
	if err != nil {
		return err
	}
	return printIssues(got, *jsonOut)
}

func issuesUpdate(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	title := fs.String("title", "", "new title")
	body := fs.String("body", "", "new body")
	status := fs.String("status", "", "new status")
	addLabel := fs.String("add-label", "", "label to add")
	removeLabel := fs.String("remove-label", "", "label to remove")
	jsonOut := fs.Bool("json", false, "emit JSON")
	known := map[string]bool{"title": true, "body": true, "status": true, "add-label": true, "remove-label": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("update requires an issue ID")
	}
	in := providers.UpdateIssueInput{}
	if isFlagSet(fs, "title") {
		in.Title = title
	}
	if isFlagSet(fs, "body") {
		in.Body = body
	}
	if *status != "" {
		s := providers.Status(*status)
		in.Status = &s
	}
	if *addLabel != "" {
		in.AddLabels = splitCSV(*addLabel)
	}
	if *removeLabel != "" {
		in.DropLabels = splitCSV(*removeLabel)
	}
	got, err := r.Update(ctx, fs.Arg(0), in)
	if err != nil {
		return err
	}
	return printIssue(got, *jsonOut)
}

func issuesClose(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("close requires an issue ID")
	}
	return r.Close(ctx, args[0])
}

func issuesReopen(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("reopen requires an issue ID")
	}
	return r.Reopen(ctx, args[0])
}

func issuesComment(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("comment", flag.ContinueOnError)
	body := fs.String("body", "", "comment body")
	known := map[string]bool{"body": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 || *body == "" {
		return fmt.Errorf("comment requires <id> and --body")
	}
	c, err := r.AddComment(ctx, fs.Arg(0), *body)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", c.Body)
	return nil
}

func issuesComments(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("comments", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderArgs(nil, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("comments requires an issue ID")
	}
	cs, err := r.ListComments(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(cs)
	}
	for _, c := range cs {
		fmt.Printf("- %s by %s at %s\n  %s\n", c.ID, c.Author, c.CreatedAt.Format("2006-01-02 15:04"), c.Body)
	}
	return nil
}

func issuesLink(ctx context.Context, r *providers.BacklogRouter, args []string, unlink bool) error {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	blocks := fs.String("blocks", "", "issue ID that the source blocks")
	childOf := fs.String("child-of", "", "parent issue ID")
	known := map[string]bool{"blocks": true, "child-of": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("link requires <from-id>")
	}
	from := fs.Arg(0)
	switch {
	case *blocks != "":
		if unlink {
			return r.Unlink(ctx, from, *blocks, providers.LinkBlocks)
		}
		return r.Link(ctx, from, *blocks, providers.LinkBlocks)
	case *childOf != "":
		if unlink {
			return r.Unlink(ctx, from, *childOf, providers.LinkChildOf)
		}
		return r.Link(ctx, from, *childOf, providers.LinkChildOf)
	default:
		return fmt.Errorf("link requires --blocks or --child-of")
	}
}

func issuesReady(ctx context.Context, r *providers.BacklogRouter, args []string) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	label := fs.String("label", "", "filter by label")
	limit := fs.Int("limit", 0, "max results")
	jsonOut := fs.Bool("json", false, "emit JSON")
	known := map[string]bool{"label": true, "limit": true}
	if err := fs.Parse(reorderArgs(known, args)); err != nil {
		return err
	}
	f := providers.ReadyFilter{Limit: *limit}
	if *label != "" {
		f.Labels = []string{*label}
	}
	got, err := r.Ready(ctx, f)
	if err != nil {
		return err
	}
	return printIssues(got, *jsonOut)
}

func issuesBlocked(ctx context.Context, r *providers.BacklogRouter) error {
	got, err := r.Blocked(ctx)
	if err != nil {
		return err
	}
	return printIssues(got, false)
}

func issuesPrime(ctx context.Context, r *providers.BacklogRouter) error {
	out, err := r.Prime(ctx, providers.PrimeOptions{})
	if err != nil {
		return err
	}
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

// --- formatting helpers ---

func printIssue(i providers.Issue, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(i)
	}
	fmt.Printf("%s [%s] %s\n", i.ID, i.Status, i.Title)
	if len(i.Labels) > 0 {
		fmt.Printf("  labels: %s\n", strings.Join(i.Labels, ", "))
	}
	if i.Parent != "" {
		fmt.Printf("  parent: %s\n", i.Parent)
	}
	if len(i.BlockedBy) > 0 {
		fmt.Printf("  blocked-by: %s\n", strings.Join(i.BlockedBy, ", "))
	}
	if len(i.Blocks) > 0 {
		fmt.Printf("  blocks: %s\n", strings.Join(i.Blocks, ", "))
	}
	return nil
}

func printIssues(is []providers.Issue, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(is)
	}
	for _, i := range is {
		fmt.Printf("%s [%s] %s\n", i.ID, i.Status, i.Title)
	}
	return nil
}

// reorderArgs moves all flag-looking tokens to the front of args, preserving
// their relative order, so that Go's flag package (which stops parsing at the
// first non-flag token) can pick up flags that appeared after positionals.
//
// A "flag token" is anything beginning with "-". For "-x value" or "--x value"
// without `=`, the following token is treated as the flag's value when its
// name is in the known set. Unknown flags are still hoisted; their following
// token is kept attached defensively.
func reorderArgs(knownWithValue map[string]bool, args []string) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.Index(name, "="); eq >= 0 {
				continue // value glued with =
			}
			if knownWithValue[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
