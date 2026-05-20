// Package vltadapter wraps the local vlt CLI as a providers.NotesAdapter.
//
// All operations shell out to vlt and parse its --json output where available.
// Reads with no JSON form (read, append output) are consumed as raw markdown.
package vltadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/paivot-ai/pvg/internal/providers"
)

const adapterName = "vlt"

// execCommandContext is overridable in tests.
var execCommandContext = exec.CommandContext

func init() {
	providers.RegisterNotes(adapterName, New)
}

// New builds a vlt adapter from the given config map. Required keys:
//
//	vault: vlt vault name (matched against Obsidian config) or absolute path
//
// If vault is missing or empty, the adapter falls back to the VLT_VAULT env
// var, matching vlt's own default.
func New(cfg map[string]interface{}) (providers.NotesAdapter, error) {
	vault, _ := cfg["vault"].(string)
	return &Adapter{vault: vault}, nil
}

// Adapter implements providers.NotesAdapter against a vlt CLI installation.
type Adapter struct {
	vault string
}

// Name reports the adapter name as registered.
func (a *Adapter) Name() string { return adapterName }

// Capabilities returns no optional capabilities; vlt's notes contract is
// purely the required ops.
func (a *Adapter) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet()
}

// --- Search / browse ---

func (a *Adapter) Search(ctx context.Context, query string, limit int) ([]providers.SearchHit, error) {
	args := []string{"search", "query=" + query}
	out, err := a.runJSON(ctx, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Title   string  `json:"title"`
		Path    string  `json:"path"`
		Snippet string  `json:"snippet"`
		Score   float64 `json:"score"`
	}
	if len(bytes.TrimSpace(out)) == 0 || string(bytes.TrimSpace(out)) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode vlt search json: %w", err)
	}
	hits := make([]providers.SearchHit, 0, len(raw))
	for i, r := range raw {
		if limit > 0 && i >= limit {
			break
		}
		hits = append(hits, providers.SearchHit{
			Ref:     providers.NoteRef{Path: r.Path},
			Title:   r.Title,
			Snippet: r.Snippet,
			Score:   r.Score,
		})
	}
	return hits, nil
}

func (a *Adapter) List(ctx context.Context, folder string) ([]providers.NoteRef, error) {
	args := []string{"files"}
	if folder != "" {
		args = append(args, "folder="+folder)
	}
	out, err := a.runJSON(ctx, args...)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 || string(bytes.TrimSpace(out)) == "null" {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal(out, &paths); err != nil {
		return nil, fmt.Errorf("decode vlt files json: %w", err)
	}
	refs := make([]providers.NoteRef, len(paths))
	for i, p := range paths {
		refs[i] = providers.NoteRef{Path: p}
	}
	return refs, nil
}

// --- Read / write ---

func (a *Adapter) Read(ctx context.Context, ref providers.NoteRef) (providers.Note, error) {
	if ref.Path == "" {
		return providers.Note{}, fmt.Errorf("vlt Read: empty NoteRef.Path")
	}
	body, err := a.run(ctx, "read", "file="+pathToTitle(ref.Path))
	if err != nil {
		return providers.Note{}, mapReadError(err, ref.Path)
	}
	props, err := a.readProperties(ctx, ref)
	if err != nil {
		// Properties failure is not fatal -- some notes have no frontmatter.
		props = nil
	}
	title, stripped := splitFrontmatter(string(body))
	return providers.Note{
		Ref:        ref,
		Title:      title,
		Body:       stripped,
		Properties: props,
	}, nil
}

func (a *Adapter) Create(ctx context.Context, in providers.CreateNoteInput) (providers.Note, error) {
	if in.Ref.Path == "" {
		return providers.Note{}, fmt.Errorf("vlt Create: empty Ref.Path")
	}
	title := in.Title
	if title == "" {
		title = pathToTitle(in.Ref.Path)
	}
	body := in.Body
	if len(in.Properties) > 0 {
		body = renderFrontmatter(in.Properties) + body
	}
	args := []string{
		"create",
		"name=" + title,
		"path=" + in.Ref.Path,
		"content=" + body,
		"silent",
	}
	if _, err := a.run(ctx, args...); err != nil {
		return providers.Note{}, err
	}
	return a.Read(ctx, in.Ref)
}

// renderFrontmatter writes a minimal YAML frontmatter block. Values are
// stringified with %v -- callers wanting structured types should pre-encode.
func renderFrontmatter(props map[string]interface{}) string {
	if len(props) == 0 {
		return ""
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, props[k])
	}
	b.WriteString("---\n")
	return b.String()
}

// sortStrings is a tiny inline sort to avoid pulling in sort just for
// deterministic frontmatter ordering.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}

func (a *Adapter) Append(ctx context.Context, ref providers.NoteRef, body string) (providers.Note, error) {
	args := []string{"append", "file=" + pathToTitle(ref.Path), "content=" + body}
	if _, err := a.run(ctx, args...); err != nil {
		return providers.Note{}, err
	}
	return a.Read(ctx, ref)
}

// --- Properties ---

func (a *Adapter) GetProperty(ctx context.Context, ref providers.NoteRef, key string) (interface{}, error) {
	props, err := a.readProperties(ctx, ref)
	if err != nil {
		return nil, err
	}
	v, ok := props[key]
	if !ok {
		return nil, fmt.Errorf("%w: property %q on %s", providers.ErrNotFound, key, ref.Path)
	}
	return v, nil
}

func (a *Adapter) SetProperty(ctx context.Context, ref providers.NoteRef, key string, value interface{}) error {
	args := []string{
		"property:set",
		"file=" + pathToTitle(ref.Path),
		"name=" + key,
		"value=" + fmt.Sprintf("%v", value),
	}
	_, err := a.run(ctx, args...)
	return err
}

func (a *Adapter) readProperties(ctx context.Context, ref providers.NoteRef) (map[string]interface{}, error) {
	out, err := a.runJSON(ctx, "properties", "file="+pathToTitle(ref.Path))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 || string(bytes.TrimSpace(out)) == "null" {
		return nil, nil
	}
	var props map[string]interface{}
	if err := json.Unmarshal(out, &props); err != nil {
		return nil, fmt.Errorf("decode vlt properties json: %w", err)
	}
	return props, nil
}

// --- Internal helpers ---

func (a *Adapter) run(ctx context.Context, args ...string) ([]byte, error) {
	full := a.prefixVault(args)
	cmd := execCommandContext(ctx, "vlt", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("vlt %s: %s", strings.Join(full, " "), msg)
	}
	return stdout.Bytes(), nil
}

func (a *Adapter) runJSON(ctx context.Context, args ...string) ([]byte, error) {
	return a.run(ctx, append([]string{"--json"}, args...)...)
}

// prefixVault prepends `vault="<name>"` to the arg list when a vault is
// configured. vlt accepts vault as a positional key=value before the command.
func (a *Adapter) prefixVault(args []string) []string {
	if a.vault == "" {
		return append([]string(nil), args...)
	}
	return append([]string{"vault=" + a.vault}, args...)
}

// pathToTitle converts "folder/My Note.md" to "My Note", which is how vlt
// addresses notes by file= argument.
func pathToTitle(p string) string {
	if p == "" {
		return ""
	}
	last := p
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		last = p[idx+1:]
	}
	last = strings.TrimSuffix(last, ".md")
	return last
}

// splitFrontmatter extracts the title from a markdown body. If the body opens
// with --- ... ---, the frontmatter block is removed; the first H1 (or first
// non-empty line) becomes the title.
func splitFrontmatter(body string) (title, stripped string) {
	stripped = body
	if strings.HasPrefix(body, "---\n") {
		end := strings.Index(body[4:], "\n---")
		if end >= 0 {
			stripped = body[4+end+len("\n---"):]
			stripped = strings.TrimPrefix(stripped, "\n")
		}
	}
	for _, line := range strings.Split(stripped, "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
		if strings.TrimSpace(line) != "" {
			title = strings.TrimSpace(line)
			break
		}
	}
	return title, stripped
}

func mapReadError(err error, path string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "no such file") {
		return fmt.Errorf("%w: note %s", providers.ErrNotFound, path)
	}
	return err
}
