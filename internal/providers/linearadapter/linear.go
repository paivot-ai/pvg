// Package linearadapter implements providers.BacklogAdapter against Linear's
// GraphQL API. It runs in-process via net/http -- no MCP dependency -- so pvg
// can be configured to talk to Linear directly or as a mirror behind a
// local-first primary like nd.
//
// Required config keys:
//
//	team_key:     Linear team key (e.g., "ENG"). Required for Create.
//	api_key_env:  env var name to read the Linear API key from.
//	api_key:      literal API key (only useful when committed config is .gitignored).
//	endpoint:     override GraphQL endpoint (default: https://api.linear.app/graphql).
//
// Either api_key_env or api_key must resolve to a non-empty token.
package linearadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/paivot-ai/pvg/internal/providers"
)

const (
	adapterName     = "linear"
	defaultEndpoint = "https://api.linear.app/graphql"
)

// httpClient is overridable in tests.
var httpClient = &http.Client{Timeout: 30 * time.Second}

func init() {
	providers.RegisterBacklog(adapterName, New)
}

// New builds a Linear adapter from the given config map.
//
// Optional config: status_overrides maps a provider.Status string to a Linear
// workflow state NAME (not type). This is the right way to disambiguate teams
// that have multiple states sharing the same Linear type -- e.g. HexGraph's
// "Product" team has both "Started" (type=started) and "Delivered" (type=
// started). Without an override the adapter would pick whichever the API
// returns first, which is non-deterministic.
//
//	status_overrides:
//	  in_progress: Started
//	  closed: Accepted
func New(cfg map[string]interface{}) (providers.BacklogAdapter, error) {
	teamKey, _ := cfg["team_key"].(string)
	endpoint, _ := cfg["endpoint"].(string)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	apiKey, _ := cfg["api_key"].(string)
	if apiKey == "" {
		if envName, _ := cfg["api_key_env"].(string); envName != "" {
			apiKey = os.Getenv(envName)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("linear: api_key or api_key_env required")
	}

	overrides := map[string]string{}
	if raw, ok := cfg["status_overrides"].(map[string]interface{}); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				overrides[k] = s
			}
		}
	}

	return &Adapter{
		endpoint:        endpoint,
		apiKey:          apiKey,
		teamKey:         teamKey,
		statusOverrides: overrides,
	}, nil
}

// Adapter implements providers.BacklogAdapter.
type Adapter struct {
	endpoint        string
	apiKey          string
	teamKey         string
	statusOverrides map[string]string // provider.Status -> Linear state NAME

	// teamID is resolved lazily on first Create.
	teamID string
}

// Name reports the adapter name.
func (a *Adapter) Name() string { return adapterName }

// Capabilities reports Linear's optional features. Linear has cycles natively;
// defer maps to snoozed state but the contract is loose enough that we omit it
// for the v1 adapter.
func (a *Adapter) Capabilities() providers.CapabilitySet {
	return providers.NewCapabilitySet(
		providers.CapAttachments,
		providers.CapCycles,
	)
}

// --- Issue lifecycle ---

func (a *Adapter) Create(ctx context.Context, in providers.CreateIssueInput) (providers.Issue, error) {
	teamID, err := a.resolveTeamID(ctx)
	if err != nil {
		return providers.Issue{}, err
	}

	labelIDs, err := a.resolveLabelIDs(ctx, teamID, in.Labels)
	if err != nil {
		return providers.Issue{}, err
	}

	input := map[string]interface{}{
		"teamId":      teamID,
		"title":       in.Title,
		"description": in.Body,
	}
	if in.Parent != "" {
		input["parentId"] = in.Parent
	}
	if in.Assignee != "" {
		input["assigneeId"] = in.Assignee
	}
	if len(labelIDs) > 0 {
		input["labelIds"] = labelIDs
	}

	var resp struct {
		IssueCreate struct {
			Success bool         `json:"success"`
			Issue   linearIssue  `json:"issue"`
			Errors  []apiError   `json:"errors,omitempty"`
		} `json:"issueCreate"`
	}
	if err := a.gql(ctx, mutationIssueCreate, map[string]interface{}{"input": input}, &resp); err != nil {
		return providers.Issue{}, err
	}
	if !resp.IssueCreate.Success {
		return providers.Issue{}, fmt.Errorf("linear: issueCreate failed: %v", resp.IssueCreate.Errors)
	}

	created := linearToProvider(resp.IssueCreate.Issue)

	for _, blocker := range in.BlockedBy {
		if err := a.Link(ctx, blocker, created.ID, providers.LinkBlocks); err != nil {
			return created, fmt.Errorf("link blocker %s -> %s: %w", blocker, created.ID, err)
		}
	}
	if len(in.BlockedBy) > 0 {
		return a.Show(ctx, created.ID)
	}
	return created, nil
}

func (a *Adapter) Show(ctx context.Context, id string) (providers.Issue, error) {
	var resp struct {
		Issue linearIssue `json:"issue"`
	}
	if err := a.gql(ctx, queryIssue, map[string]interface{}{"id": id}, &resp); err != nil {
		return providers.Issue{}, err
	}
	if resp.Issue.ID == "" {
		return providers.Issue{}, fmt.Errorf("%w: linear issue %s", providers.ErrNotFound, id)
	}
	return linearToProvider(resp.Issue), nil
}

func (a *Adapter) List(ctx context.Context, f providers.ListFilter) ([]providers.Issue, error) {
	filter := map[string]interface{}{}
	if a.teamKey != "" {
		filter["team"] = map[string]interface{}{"key": map[string]interface{}{"eq": a.teamKey}}
	}
	if len(f.Status) > 0 {
		types := make([]string, 0, len(f.Status))
		for _, s := range f.Status {
			types = append(types, statusToLinearTypes(s)...)
		}
		filter["state"] = map[string]interface{}{"type": map[string]interface{}{"in": types}}
	}
	if f.Parent != "" {
		filter["parent"] = map[string]interface{}{"id": map[string]interface{}{"eq": f.Parent}}
	}
	if len(f.Labels) > 0 {
		filter["labels"] = map[string]interface{}{"name": map[string]interface{}{"in": f.Labels}}
	}

	first := f.Limit
	if first <= 0 {
		first = 50
	}

	var resp struct {
		Issues struct {
			Nodes []linearIssue `json:"nodes"`
		} `json:"issues"`
	}
	if err := a.gql(ctx, queryIssues, map[string]interface{}{
		"filter": filter,
		"first":  first,
	}, &resp); err != nil {
		return nil, err
	}
	return convertList(resp.Issues.Nodes), nil
}

func (a *Adapter) Update(ctx context.Context, id string, in providers.UpdateIssueInput) (providers.Issue, error) {
	input := map[string]interface{}{}
	if in.Title != nil {
		input["title"] = *in.Title
	}
	if in.Body != nil {
		input["description"] = *in.Body
	}
	if in.Status != nil {
		stateID, err := a.resolveStateID(ctx, *in.Status)
		if err != nil {
			return providers.Issue{}, err
		}
		input["stateId"] = stateID
	}
	if in.SetAssignee != nil {
		input["assigneeId"] = *in.SetAssignee
	}
	if len(in.AddLabels) > 0 {
		teamID, err := a.resolveTeamID(ctx)
		if err != nil {
			return providers.Issue{}, err
		}
		ids, err := a.resolveLabelIDs(ctx, teamID, in.AddLabels)
		if err != nil {
			return providers.Issue{}, err
		}
		input["labelIds"] = ids
	}

	var resp struct {
		IssueUpdate struct {
			Success bool         `json:"success"`
			Issue   linearIssue  `json:"issue"`
		} `json:"issueUpdate"`
	}
	if err := a.gql(ctx, mutationIssueUpdate, map[string]interface{}{
		"id":    id,
		"input": input,
	}, &resp); err != nil {
		return providers.Issue{}, err
	}
	if !resp.IssueUpdate.Success {
		return providers.Issue{}, fmt.Errorf("linear: issueUpdate failed for %s", id)
	}
	return linearToProvider(resp.IssueUpdate.Issue), nil
}

func (a *Adapter) Close(ctx context.Context, id string) error {
	stateID, err := a.resolveStateID(ctx, providers.StatusClosed)
	if err != nil {
		return err
	}
	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	return a.gql(ctx, mutationIssueUpdate, map[string]interface{}{
		"id":    id,
		"input": map[string]interface{}{"stateId": stateID},
	}, &resp)
}

func (a *Adapter) Reopen(ctx context.Context, id string) error {
	stateID, err := a.resolveStateID(ctx, providers.StatusOpen)
	if err != nil {
		return err
	}
	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	return a.gql(ctx, mutationIssueUpdate, map[string]interface{}{
		"id":    id,
		"input": map[string]interface{}{"stateId": stateID},
	}, &resp)
}

// --- Comments ---

func (a *Adapter) AddComment(ctx context.Context, id, body string) (providers.Comment, error) {
	var resp struct {
		CommentCreate struct {
			Success bool          `json:"success"`
			Comment linearComment `json:"comment"`
		} `json:"commentCreate"`
	}
	if err := a.gql(ctx, mutationCommentCreate, map[string]interface{}{
		"input": map[string]interface{}{"issueId": id, "body": body},
	}, &resp); err != nil {
		return providers.Comment{}, err
	}
	if !resp.CommentCreate.Success {
		return providers.Comment{}, fmt.Errorf("linear: commentCreate failed for %s", id)
	}
	return commentToProvider(resp.CommentCreate.Comment), nil
}

func (a *Adapter) ListComments(ctx context.Context, id string) ([]providers.Comment, error) {
	var resp struct {
		Issue struct {
			Comments struct {
				Nodes []linearComment `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := a.gql(ctx, queryIssueComments, map[string]interface{}{"id": id}, &resp); err != nil {
		return nil, err
	}
	out := make([]providers.Comment, len(resp.Issue.Comments.Nodes))
	for i, c := range resp.Issue.Comments.Nodes {
		out[i] = commentToProvider(c)
	}
	return out, nil
}

// --- Links ---

func (a *Adapter) Link(ctx context.Context, from, to string, kind providers.LinkKind) error {
	switch kind {
	case providers.LinkBlocks:
		var resp struct {
			IssueRelationCreate struct {
				Success bool `json:"success"`
			} `json:"issueRelationCreate"`
		}
		return a.gql(ctx, mutationIssueRelationCreate, map[string]interface{}{
			"input": map[string]interface{}{
				"issueId":        from,
				"relatedIssueId": to,
				"type":           "blocks",
			},
		}, &resp)
	case providers.LinkChildOf:
		_, err := a.Update(ctx, from, providers.UpdateIssueInput{}) // satisfies signature
		_ = err
		var resp struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		}
		return a.gql(ctx, mutationIssueUpdate, map[string]interface{}{
			"id":    from,
			"input": map[string]interface{}{"parentId": to},
		}, &resp)
	default:
		return fmt.Errorf("%w: link kind %q", providers.ErrUnsupported, kind)
	}
}

func (a *Adapter) Unlink(ctx context.Context, from, to string, kind providers.LinkKind) error {
	switch kind {
	case providers.LinkChildOf:
		var resp struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		}
		return a.gql(ctx, mutationIssueUpdate, map[string]interface{}{
			"id":    from,
			"input": map[string]interface{}{"parentId": nil},
		}, &resp)
	case providers.LinkBlocks:
		// Find and delete the relation: requires a list-then-delete dance.
		var listResp struct {
			Issue struct {
				Relations struct {
					Nodes []struct {
						ID           string `json:"id"`
						Type         string `json:"type"`
						RelatedIssue struct {
							ID string `json:"id"`
						} `json:"relatedIssue"`
					} `json:"nodes"`
				} `json:"relations"`
			} `json:"issue"`
		}
		if err := a.gql(ctx, queryIssueRelations, map[string]interface{}{"id": from}, &listResp); err != nil {
			return err
		}
		for _, r := range listResp.Issue.Relations.Nodes {
			if r.Type == "blocks" && r.RelatedIssue.ID == to {
				var del struct {
					IssueRelationDelete struct {
						Success bool `json:"success"`
					} `json:"issueRelationDelete"`
				}
				return a.gql(ctx, mutationIssueRelationDelete, map[string]interface{}{"id": r.ID}, &del)
			}
		}
		return fmt.Errorf("%w: blocks relation %s -> %s", providers.ErrNotFound, from, to)
	default:
		return fmt.Errorf("%w: link kind %q", providers.ErrUnsupported, kind)
	}
}

// --- Derived queries (computed client-side) ---

func (a *Adapter) Ready(ctx context.Context, f providers.ReadyFilter) ([]providers.Issue, error) {
	all, err := a.List(ctx, providers.ListFilter{
		Status: []providers.Status{providers.StatusOpen, providers.StatusInProgress},
		Labels: f.Labels,
		Limit:  f.Limit * 4, // pull extra to allow filtering, capped below
	})
	if err != nil {
		return nil, err
	}
	out := make([]providers.Issue, 0, len(all))
	for _, i := range all {
		if len(i.BlockedBy) == 0 {
			out = append(out, i)
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (a *Adapter) Blocked(ctx context.Context) ([]providers.Issue, error) {
	all, err := a.List(ctx, providers.ListFilter{
		Status: []providers.Status{providers.StatusOpen, providers.StatusInProgress, providers.StatusBlocked},
	})
	if err != nil {
		return nil, err
	}
	out := make([]providers.Issue, 0, len(all))
	for _, i := range all {
		if len(i.BlockedBy) > 0 {
			out = append(out, i)
		}
	}
	return out, nil
}

func (a *Adapter) Prime(ctx context.Context, _ providers.PrimeOptions) (string, error) {
	all, err := a.List(ctx, providers.ListFilter{Limit: 200})
	if err != nil {
		return "", err
	}
	var counts = map[providers.Status]int{}
	for _, i := range all {
		counts[i.Status]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Project Status (linear prime)\n\n")
	fmt.Fprintf(&b, "Total: %d | Open: %d | In Progress: %d | Blocked: %d | Closed: %d\n\n",
		len(all),
		counts[providers.StatusOpen],
		counts[providers.StatusInProgress],
		counts[providers.StatusBlocked],
		counts[providers.StatusClosed])
	fmt.Fprintln(&b, "## In Progress")
	any := false
	for _, i := range all {
		if i.Status == providers.StatusInProgress {
			fmt.Fprintf(&b, "- %s [%s] %s\n", i.ID, i.Status, i.Title)
			any = true
		}
	}
	if !any {
		fmt.Fprintln(&b, "(none)")
	}
	return b.String(), nil
}

// --- GraphQL plumbing ---

func (a *Adapter) gql(ctx context.Context, query string, variables map[string]interface{}, out interface{}) error {
	body, err := json.Marshal(map[string]interface{}{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal gql request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", a.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return providers.ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("linear http %d: %s", resp.StatusCode, string(raw))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []apiError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear gql errors: %v", envelope.Errors)
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("linear gql returned empty data: %s", string(raw))
	}
	return json.Unmarshal(envelope.Data, out)
}

func (a *Adapter) resolveTeamID(ctx context.Context) (string, error) {
	if a.teamID != "" {
		return a.teamID, nil
	}
	if a.teamKey == "" {
		return "", fmt.Errorf("linear: team_key is required for write operations")
	}
	var resp struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := a.gql(ctx, queryTeamByKey, map[string]interface{}{"key": a.teamKey}, &resp); err != nil {
		return "", err
	}
	for _, n := range resp.Teams.Nodes {
		if n.Key == a.teamKey {
			a.teamID = n.ID
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("linear: team %q not found", a.teamKey)
}

func (a *Adapter) resolveStateID(ctx context.Context, s providers.Status) (string, error) {
	teamID, err := a.resolveTeamID(ctx)
	if err != nil {
		return "", err
	}
	var resp struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := a.gql(ctx, queryWorkflowStates, map[string]interface{}{
		"teamId": teamID,
	}, &resp); err != nil {
		return "", err
	}

	// 1. Honor a configured name-override if present.
	if wantName, ok := a.statusOverrides[string(s)]; ok && wantName != "" {
		for _, n := range resp.WorkflowStates.Nodes {
			if n.Name == wantName {
				return n.ID, nil
			}
		}
		return "", fmt.Errorf("linear: status_override for %q references unknown state name %q on team %s", s, wantName, teamID)
	}

	// 2. Fall back to first-by-type. Ambiguous when a team has multiple
	// states sharing the same Linear type -- callers should set
	// status_overrides for those teams.
	wantTypes := statusToLinearTypes(s)
	for _, n := range resp.WorkflowStates.Nodes {
		for _, w := range wantTypes {
			if n.Type == w {
				return n.ID, nil
			}
		}
	}
	return "", fmt.Errorf("linear: no workflow state for status %q on team %s", s, teamID)
}

func (a *Adapter) resolveLabelIDs(ctx context.Context, teamID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	var resp struct {
		IssueLabels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Team struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	if err := a.gql(ctx, queryIssueLabels, map[string]interface{}{}, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range resp.IssueLabels.Nodes {
		if !wanted[n.Name] {
			continue
		}
		if n.Team.ID != "" && n.Team.ID != teamID {
			continue
		}
		if seen[n.Name] {
			continue
		}
		ids = append(ids, n.ID)
		seen[n.Name] = true
	}
	return ids, nil
}

// --- Conversions ---

func linearToProvider(l linearIssue) providers.Issue {
	out := providers.Issue{
		ID:        l.Identifier,
		Title:     l.Title,
		Body:      l.Description,
		Status:    fromLinearStateType(l.State.Type),
		Assignee:  l.Assignee.Name,
		CreatedAt: l.CreatedAt,
		UpdatedAt: l.UpdatedAt,
		Extras: map[string]interface{}{
			"uuid":      l.ID,
			"priority":  l.Priority,
			"state":     l.State.Name,
			"team_key":  l.Team.Key,
		},
	}
	if l.Parent.Identifier != "" {
		out.Parent = l.Parent.Identifier
	}
	for _, lbl := range l.Labels.Nodes {
		out.Labels = append(out.Labels, lbl.Name)
	}
	for _, c := range l.Children.Nodes {
		if c.Identifier != "" {
			out.Children = append(out.Children, c.Identifier)
		}
	}
	for _, r := range l.Relations.Nodes {
		if r.Type == "blocks" && r.RelatedIssue.Identifier != "" {
			out.Blocks = append(out.Blocks, r.RelatedIssue.Identifier)
		}
	}
	for _, r := range l.InverseRelations.Nodes {
		if r.Type == "blocks" && r.Issue.Identifier != "" {
			out.BlockedBy = append(out.BlockedBy, r.Issue.Identifier)
		}
	}
	sort.Strings(out.Labels)
	return out
}

func convertList(ns []linearIssue) []providers.Issue {
	out := make([]providers.Issue, len(ns))
	for i, n := range ns {
		out[i] = linearToProvider(n)
	}
	return out
}

func commentToProvider(c linearComment) providers.Comment {
	return providers.Comment{
		ID:        c.ID,
		Author:    c.User.Name,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
	}
}

func fromLinearStateType(t string) providers.Status {
	switch t {
	case "triage", "backlog", "unstarted":
		return providers.StatusOpen
	case "started":
		return providers.StatusInProgress
	case "completed":
		return providers.StatusClosed
	case "canceled":
		return providers.StatusClosed
	default:
		return providers.Status(t)
	}
}

func statusToLinearTypes(s providers.Status) []string {
	switch s {
	case providers.StatusOpen:
		return []string{"backlog", "unstarted", "triage"}
	case providers.StatusInProgress:
		return []string{"started"}
	case providers.StatusClosed:
		return []string{"completed", "canceled"}
	case providers.StatusBlocked:
		return []string{"started", "unstarted"}
	case providers.StatusDeferred:
		return []string{"backlog"}
	}
	return nil
}

// --- GraphQL types and operations ---

type linearIssue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"` // human-readable like "ENG-42"
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee struct {
		Name string `json:"name"`
	} `json:"assignee"`
	Team struct {
		Key string `json:"key"`
	} `json:"team"`
	Parent struct {
		Identifier string `json:"identifier"`
	} `json:"parent"`
	Children struct {
		Nodes []struct {
			Identifier string `json:"identifier"`
		} `json:"nodes"`
	} `json:"children"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Relations struct {
		Nodes []struct {
			Type         string `json:"type"`
			RelatedIssue struct {
				Identifier string `json:"identifier"`
			} `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				Identifier string `json:"identifier"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

type linearComment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	User      struct {
		Name string `json:"name"`
	} `json:"user"`
}

type apiError struct {
	Message string `json:"message"`
}

const issueFields = `
id identifier title description priority createdAt updatedAt
state { name type }
assignee { name }
team { key }
parent { identifier }
children { nodes { identifier } }
labels { nodes { name } }
relations { nodes { type relatedIssue { identifier } } }
inverseRelations { nodes { type issue { identifier } } }
`

var (
	queryIssue = `query($id: String!) { issue(id: $id) { ` + issueFields + ` } }`

	queryIssues = `query($filter: IssueFilter, $first: Int) { issues(filter: $filter, first: $first) { nodes { ` + issueFields + ` } } }`

	queryIssueComments = `query($id: String!) { issue(id: $id) { comments { nodes { id body createdAt user { name } } } } }`

	queryIssueRelations = `query($id: String!) { issue(id: $id) { relations { nodes { id type relatedIssue { id } } } } }`

	queryTeamByKey = `query($key: String!) { teams(filter: { key: { eq: $key } }) { nodes { id key } } }`

	queryWorkflowStates = `query($teamId: ID!) { workflowStates(filter: { team: { id: { eq: $teamId } } }) { nodes { id name type } } }`

	queryIssueLabels = `query { issueLabels(first: 200) { nodes { id name team { id } } } }`

	mutationIssueCreate = `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { ` + issueFields + ` } errors { message } } }`

	mutationIssueUpdate = `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success issue { ` + issueFields + ` } } }`

	mutationCommentCreate = `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id body createdAt user { name } } } }`

	mutationIssueRelationCreate = `mutation($input: IssueRelationCreateInput!) { issueRelationCreate(input: $input) { success } }`

	mutationIssueRelationDelete = `mutation($id: String!) { issueRelationDelete(id: $id) { success } }`
)
