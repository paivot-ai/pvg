// Package ndadapter wraps the local nd CLI as a providers.BacklogAdapter.
//
// All operations shell out to nd with --json --vault <dir> and parse the
// emitted records into the normalized provider types. The adapter is
// intentionally thin: it owns no nd state, holds no caches, and never edits
// vault files directly.
package ndadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/paivot-ai/pvg/internal/providers"
)

const adapterName = "nd"

// execCommandContext is overridable in tests.
var execCommandContext = exec.CommandContext

func init() {
	providers.RegisterBacklog(adapterName, New)
}

// New builds an nd adapter from the given config map. Required keys:
//
//	vault: path to the nd vault directory (relative or absolute)
//
// If vault is missing or empty, the adapter defaults to ".vault" relative to
// the process working directory, matching nd's own default.
func New(cfg map[string]interface{}) (providers.BacklogAdapter, error) {
	vault, _ := cfg["vault"].(string)
	if vault == "" {
		vault = ".vault"
	}
	return &Adapter{vault: vault}, nil
}

// Adapter implements providers.BacklogAdapter against an nd CLI installation.
type Adapter struct {
	vault string
}

// Name reports the adapter name as registered.
func (a *Adapter) Name() string { return adapterName }

// Capabilities reports nd's optional features. nd supports defer, archive, and
// doctor natively; cycles and attachments are not in the nd model.
func (a *Adapter) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapDefer,
		providers.CapArchive,
		providers.CapDoctor,
	)
}

// --- Issue lifecycle ---

func (a *Adapter) Create(ctx context.Context, in providers.CreateIssueInput) (providers.Issue, error) {
	args := []string{"create"}
	if in.Title != "" {
		args = append(args, "--title", in.Title)
	}
	if in.Body != "" {
		args = append(args, "--description", in.Body)
	}
	if in.Parent != "" {
		args = append(args, "--parent", in.Parent)
	}
	if in.Assignee != "" {
		args = append(args, "--assignee", in.Assignee)
	}
	if len(in.Labels) > 0 {
		args = append(args, "--labels", strings.Join(in.Labels, ","))
	}
	args = append(args, "--json")

	out, err := a.run(ctx, args...)
	if err != nil {
		return providers.Issue{}, err
	}
	created, err := decodeIssue(out)
	if err != nil {
		return providers.Issue{}, err
	}

	// Apply blocked-by links one-by-one; nd has no inline flag for this on create.
	for _, blocker := range in.BlockedBy {
		if err := a.Link(ctx, blocker, created.ID, providers.LinkBlocks); err != nil {
			return created, fmt.Errorf("link blocker %s -> %s: %w", blocker, created.ID, err)
		}
	}
	// nd create --json emits a thin record (ID-focused), so re-fetch the full
	// issue so callers get a uniformly-populated providers.Issue.
	if created.ID != "" {
		return a.Show(ctx, created.ID)
	}
	return created, nil
}

func (a *Adapter) Show(ctx context.Context, id string) (providers.Issue, error) {
	out, err := a.run(ctx, "show", id, "--json")
	if err != nil {
		return providers.Issue{}, mapShowError(err, id)
	}
	return decodeIssue(out)
}

func (a *Adapter) List(ctx context.Context, f providers.ListFilter) ([]providers.Issue, error) {
	args := []string{"list", "--json"}
	args = appendListFilter(args, f)
	out, err := a.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return decodeIssueList(out)
}

func (a *Adapter) Update(ctx context.Context, id string, in providers.UpdateIssueInput) (providers.Issue, error) {
	args := []string{"update", id}
	if in.Title != nil {
		args = append(args, "--title", *in.Title)
	}
	if in.Body != nil {
		args = append(args, "--description", *in.Body)
	}
	if in.Status != nil {
		args = append(args, "--status", ndStatus(*in.Status))
	}
	if in.SetAssignee != nil {
		args = append(args, "--assignee", *in.SetAssignee)
	}
	if len(in.AddLabels) > 0 {
		args = append(args, "--add-label", strings.Join(in.AddLabels, ","))
	}
	if len(in.DropLabels) > 0 {
		args = append(args, "--remove-label", strings.Join(in.DropLabels, ","))
	}
	args = append(args, "--json")

	if _, err := a.run(ctx, args...); err != nil {
		return providers.Issue{}, err
	}
	return a.Show(ctx, id)
}

func (a *Adapter) Close(ctx context.Context, id string) error {
	_, err := a.run(ctx, "close", id)
	return err
}

func (a *Adapter) Reopen(ctx context.Context, id string) error {
	_, err := a.run(ctx, "reopen", id)
	return err
}

// --- Comments ---

func (a *Adapter) AddComment(ctx context.Context, id, body string) (providers.Comment, error) {
	// nd takes the comment text as a positional argument, not a flag.
	if _, err := a.run(ctx, "comments", "add", id, body); err != nil {
		return providers.Comment{}, err
	}
	return providers.Comment{
		Author:    "",
		Body:      body,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (a *Adapter) ListComments(ctx context.Context, id string) ([]providers.Comment, error) {
	out, err := a.run(ctx, "comments", "list", id, "--json")
	if err != nil {
		// nd errors with `heading "Comments" not found` when the issue has no
		// Comments section yet. Treat that as an empty list.
		if strings.Contains(err.Error(), "Comments") && strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	return decodeComments(out)
}

func decodeComments(out []byte) ([]providers.Comment, error) {
	var raw []struct {
		Author    string    `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		comments, parseErr := decodeMarkdownComments(trimmed)
		if parseErr == nil {
			return comments, nil
		}
		return nil, fmt.Errorf("decode comments json: %w", err)
	}
	cs := make([]providers.Comment, len(raw))
	for i, r := range raw {
		cs[i] = providers.Comment{Author: r.Author, Body: r.Body, CreatedAt: r.CreatedAt}
	}
	return cs, nil
}

func decodeMarkdownComments(out []byte) ([]providers.Comment, error) {
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var comments []providers.Comment
	var current *providers.Comment
	var body []string

	flush := func() {
		if current == nil {
			return
		}
		c := *current
		c.Body = strings.TrimRight(strings.Join(body, "\n"), "\n")
		comments = append(comments, c)
		current = nil
		body = nil
	}

	for _, line := range lines {
		if createdAt, author, ok := parseMarkdownCommentHeading(line); ok {
			flush()
			current = &providers.Comment{Author: author, CreatedAt: createdAt}
			continue
		}
		if current != nil {
			body = append(body, line)
		}
	}
	flush()

	if len(comments) == 0 {
		if strings.TrimSpace(text) == "## Comments" {
			return nil, nil
		}
		return nil, fmt.Errorf("no nd comment headings found")
	}
	return comments, nil
}

func parseMarkdownCommentHeading(line string) (time.Time, string, bool) {
	if !strings.HasPrefix(line, "### ") {
		return time.Time{}, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "### "))
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return time.Time{}, "", false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, fields[0])
	if err != nil {
		createdAt, err = time.Parse(time.RFC3339, fields[0])
	}
	if err != nil {
		return time.Time{}, "", false
	}
	author := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	return createdAt, author, true
}

// --- Links ---

func (a *Adapter) Link(ctx context.Context, from, to string, kind providers.LinkKind) error {
	switch kind {
	case providers.LinkBlocks:
		_, err := a.run(ctx, "dep", "add", to, from)
		return err
	case providers.LinkChildOf:
		_, err := a.run(ctx, "update", from, "--parent", to)
		return err
	default:
		return fmt.Errorf("%w: link kind %q", providers.ErrUnsupported, kind)
	}
}

func (a *Adapter) Unlink(ctx context.Context, from, to string, kind providers.LinkKind) error {
	switch kind {
	case providers.LinkBlocks:
		_, err := a.run(ctx, "dep", "rm", to, from)
		return err
	case providers.LinkChildOf:
		_, err := a.run(ctx, "update", from, "--parent", "")
		return err
	default:
		return fmt.Errorf("%w: link kind %q", providers.ErrUnsupported, kind)
	}
}

// --- Derived queries ---

func (a *Adapter) Ready(ctx context.Context, f providers.ReadyFilter) ([]providers.Issue, error) {
	args := []string{"ready", "--json"}
	if len(f.Labels) > 0 {
		args = append(args, "--label", strings.Join(f.Labels, ","))
	}
	if f.Limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", f.Limit))
	}
	out, err := a.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return decodeIssueList(out)
}

func (a *Adapter) Blocked(ctx context.Context) ([]providers.Issue, error) {
	out, err := a.run(ctx, "blocked", "--json")
	if err != nil {
		return nil, err
	}
	return decodeIssueList(out)
}

func (a *Adapter) Prime(ctx context.Context, _ providers.PrimeOptions) (string, error) {
	out, err := a.run(ctx, "prime")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- Optional capabilities ---

// Defer implements providers.OptionalBacklogDefer.
func (a *Adapter) Defer(ctx context.Context, id string, until time.Time) error {
	args := []string{"defer", id}
	if !until.IsZero() {
		args = append(args, "--until", until.UTC().Format("2006-01-02"))
	}
	_, err := a.run(ctx, args...)
	return err
}

// Undefer implements providers.OptionalBacklogDefer.
func (a *Adapter) Undefer(ctx context.Context, id string) error {
	_, err := a.run(ctx, "undefer", id)
	return err
}

// Archive implements providers.OptionalBacklogArchive.
func (a *Adapter) Archive(ctx context.Context) (string, error) {
	out, err := a.run(ctx, "archive", "--closed-only", "--json")
	if err != nil {
		return "", err
	}
	var resp struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		// nd archive may print non-JSON path; return raw for callers.
		return strings.TrimSpace(string(out)), nil
	}
	return resp.Output, nil
}

// Doctor implements providers.OptionalBacklogDoctor.
func (a *Adapter) Doctor(ctx context.Context) ([]providers.DoctorFinding, error) {
	out, err := a.run(ctx, "doctor", "--json")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 || string(bytes.TrimSpace(out)) == "null" {
		return nil, nil
	}
	var raw []struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
		IssueID  string `json:"issue_id"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		// fall back: surface the raw text as a single info finding
		return []providers.DoctorFinding{{Severity: "info", Message: strings.TrimSpace(string(out))}}, nil
	}
	out2 := make([]providers.DoctorFinding, len(raw))
	for i, r := range raw {
		out2[i] = providers.DoctorFinding{Severity: r.Severity, Message: r.Message, IssueID: r.IssueID}
	}
	return out2, nil
}

// --- Internal helpers ---

func (a *Adapter) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--vault", a.vault}, args...)
	cmd := execCommandContext(ctx, "nd", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("nd %s: %s", strings.Join(full, " "), msg)
	}
	return stdout.Bytes(), nil
}

func appendListFilter(args []string, f providers.ListFilter) []string {
	for _, lbl := range f.Labels {
		args = append(args, "--label", lbl)
	}
	if f.Parent != "" {
		args = append(args, "--parent", f.Parent)
	}
	if len(f.Status) == 1 {
		// nd accepts a single --status value
		args = append(args, "--status", ndStatus(f.Status[0]))
	}
	if len(f.Status) == 0 || hasClosed(f.Status) {
		args = append(args, "--all")
	}
	if f.Limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", f.Limit))
	}
	return args
}

func hasClosed(ss []providers.Status) bool {
	for _, s := range ss {
		if s == providers.StatusClosed {
			return true
		}
	}
	return false
}

// flexTime parses nd's RFC3339 timestamps but tolerates empty strings
// (nd emits "" for fields like ClosedAt on still-open issues).
type flexTime struct{ time.Time }

func (f *flexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		f.Time = time.Time{}
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// Fall back to RFC3339 (no fractional seconds).
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return err
		}
	}
	f.Time = t
	return nil
}

// ndIssue mirrors the JSON shape emitted by `nd show --json` and `nd list --json`.
type ndIssue struct {
	ID         string   `json:"ID"`
	Title      string   `json:"Title"`
	Status     string   `json:"Status"`
	Priority   int      `json:"Priority"`
	Type       string   `json:"Type"`
	Assignee   string   `json:"Assignee"`
	Labels     []string `json:"Labels"`
	Parent     string   `json:"Parent"`
	Blocks     []string `json:"Blocks"`
	BlockedBy  []string `json:"BlockedBy"`
	Body       string   `json:"Body"`
	CreatedAt  flexTime `json:"CreatedAt"`
	UpdatedAt  flexTime `json:"UpdatedAt"`
	ClosedAt   flexTime `json:"ClosedAt"`
	DeferUntil string   `json:"DeferUntil"`
	FilePath   string   `json:"FilePath"`
}

func decodeIssue(raw []byte) (providers.Issue, error) {
	var n ndIssue
	if err := json.Unmarshal(raw, &n); err != nil {
		return providers.Issue{}, fmt.Errorf("decode nd issue json: %w", err)
	}
	return ndToProvider(n), nil
}

func decodeIssueList(raw []byte) ([]providers.Issue, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var ns []ndIssue
	if err := json.Unmarshal(raw, &ns); err != nil {
		return nil, fmt.Errorf("decode nd issue list json: %w", err)
	}
	out := make([]providers.Issue, len(ns))
	for i, n := range ns {
		out[i] = ndToProvider(n)
	}
	return out, nil
}

func ndToProvider(n ndIssue) providers.Issue {
	return providers.Issue{
		ID:        n.ID,
		Title:     n.Title,
		Body:      n.Body,
		Status:    fromNdStatus(n.Status),
		Labels:    n.Labels,
		Parent:    n.Parent,
		Blocks:    n.Blocks,
		BlockedBy: n.BlockedBy,
		Assignee:  n.Assignee,
		CreatedAt: n.CreatedAt.Time,
		UpdatedAt: n.UpdatedAt.Time,
		Extras: map[string]interface{}{
			"priority":    n.Priority,
			"type":        n.Type,
			"file_path":   n.FilePath,
			"defer_until": n.DeferUntil,
			"closed_at":   n.ClosedAt.Time,
		},
	}
}

func fromNdStatus(s string) providers.Status {
	switch s {
	case "open":
		return providers.StatusOpen
	case "in_progress":
		return providers.StatusInProgress
	case "blocked":
		return providers.StatusBlocked
	case "deferred":
		return providers.StatusDeferred
	case "closed":
		return providers.StatusClosed
	default:
		return providers.Status(s)
	}
}

func ndStatus(s providers.Status) string {
	switch s {
	case providers.StatusOpen:
		return "open"
	case providers.StatusInProgress:
		return "in_progress"
	case providers.StatusBlocked:
		return "blocked"
	case providers.StatusDeferred:
		return "deferred"
	case providers.StatusClosed:
		return "closed"
	default:
		return string(s)
	}
}

func mapShowError(err error, id string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("%w: issue %s", providers.ErrNotFound, id)
	}
	return err
}
