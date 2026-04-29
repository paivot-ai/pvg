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

// RunNotes handles `pvg notes <subcommand> [args...]`.
func RunNotes(args []string) error {
	if len(args) == 0 {
		notesUsage(os.Stderr)
		return fmt.Errorf("missing subcommand")
	}
	router, err := openNotes()
	if err != nil {
		return err
	}
	ctx := context.Background()

	sub, rest := args[0], args[1:]
	switch sub {
	case "search":
		return notesSearch(ctx, router, rest)
	case "create":
		return notesCreate(ctx, router, rest)
	case "read":
		return notesRead(ctx, router, rest)
	case "append":
		return notesAppend(ctx, router, rest)
	case "list":
		return notesList(ctx, router, rest)
	case "property:get":
		return notesPropertyGet(ctx, router, rest)
	case "property:set":
		return notesPropertySet(ctx, router, rest)
	case "help", "-h", "--help":
		notesUsage(os.Stdout)
		return nil
	default:
		notesUsage(os.Stderr)
		return fmt.Errorf("unknown notes subcommand %q", sub)
	}
}

func notesUsage(w io.Writer) {
	fmt.Fprintln(w, `pvg notes -- normalized knowledge base CLI

Subcommands:
  search <query> [--limit N] [--json]
  create <path> [--title T] [--body B] [--prop key=val ...]
  read <path> [--json]
  append <path> --body B
  list [--folder F] [--json]
  property:get <path> <key>
  property:set <path> <key> <value>

Adapter selected from .paivot/config.yaml; defaults to vlt if absent.`)
}

func openNotes() (*providers.NotesRouter, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := paivotcfg.LocateProjectRoot(cwd)
	if err != nil {
		root = cwd
	}
	cfg, err := paivotcfg.Load(root)
	if err != nil {
		return nil, err
	}
	primary, err := providers.BuildNotes(cfg.Notes.Primary.Adapter, cfg.Notes.Primary.Config)
	if err != nil {
		return nil, fmt.Errorf("build primary notes: %w", err)
	}
	mirrors := make([]providers.NotesAdapter, 0, len(cfg.Notes.Mirrors))
	for _, m := range cfg.Notes.Mirrors {
		mirror, err := providers.BuildNotes(m.Adapter, m.Config)
		if err != nil {
			return nil, fmt.Errorf("build mirror notes %q: %w", m.Adapter, err)
		}
		mirrors = append(mirrors, mirror)
	}
	return providers.NewNotesRouter(primary, mirrors, nil), nil
}

func notesSearch(ctx context.Context, r *providers.NotesRouter, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "max hits")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("search requires a query")
	}
	query := strings.Join(fs.Args(), " ")
	hits, err := r.Search(ctx, query, *limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(hits)
	}
	for _, h := range hits {
		fmt.Printf("%s\t%s\n", h.Ref.Path, h.Title)
	}
	return nil
}

func notesCreate(ctx context.Context, r *providers.NotesRouter, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	title := fs.String("title", "", "note title")
	body := fs.String("body", "", "markdown body")
	props := newPropFlag()
	fs.Var(props, "prop", "property as key=value (repeatable)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("create requires <path>")
	}
	in := providers.CreateNoteInput{
		Ref:        providers.NoteRef{Path: fs.Arg(0)},
		Title:      *title,
		Body:       *body,
		Properties: props.values,
	}
	got, err := r.Create(ctx, in)
	if err != nil {
		return err
	}
	return printNote(got, *jsonOut)
}

func notesRead(ctx context.Context, r *providers.NotesRouter, args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("read requires <path>")
	}
	got, err := r.Read(ctx, providers.NoteRef{Path: fs.Arg(0)})
	if err != nil {
		return err
	}
	return printNote(got, *jsonOut)
}

func notesAppend(ctx context.Context, r *providers.NotesRouter, args []string) error {
	fs := flag.NewFlagSet("append", flag.ContinueOnError)
	body := fs.String("body", "", "text to append")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || *body == "" {
		return fmt.Errorf("append requires <path> and --body")
	}
	_, err := r.Append(ctx, providers.NoteRef{Path: fs.Arg(0)}, *body)
	return err
}

func notesList(ctx context.Context, r *providers.NotesRouter, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	folder := fs.String("folder", "", "folder to scope to")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	refs, err := r.List(ctx, *folder)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(refs)
	}
	for _, ref := range refs {
		fmt.Println(ref.Path)
	}
	return nil
}

func notesPropertyGet(ctx context.Context, r *providers.NotesRouter, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("property:get requires <path> <key>")
	}
	v, err := r.GetProperty(ctx, providers.NoteRef{Path: args[0]}, args[1])
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", v)
	return nil
}

func notesPropertySet(ctx context.Context, r *providers.NotesRouter, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("property:set requires <path> <key> <value>")
	}
	return r.SetProperty(ctx, providers.NoteRef{Path: args[0]}, args[1], args[2])
}

func printNote(n providers.Note, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(n)
	}
	if n.Title != "" {
		fmt.Printf("# %s\n\n", n.Title)
	}
	fmt.Print(n.Body)
	if !strings.HasSuffix(n.Body, "\n") {
		fmt.Println()
	}
	return nil
}

// propFlag accepts repeated --prop key=value flags.
type propFlag struct {
	values map[string]interface{}
}

func newPropFlag() *propFlag { return &propFlag{values: map[string]interface{}{}} }

func (p *propFlag) String() string { return "" }
func (p *propFlag) Set(s string) error {
	idx := strings.Index(s, "=")
	if idx <= 0 {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	p.values[s[:idx]] = s[idx+1:]
	return nil
}
