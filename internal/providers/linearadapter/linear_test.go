package linearadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/providers"
)

func TestRegistration_RequiresAPIKey(t *testing.T) {
	_, err := providers.BuildBacklog("linear", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when no api_key configured")
	}
}

func TestNew_ReadsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("LINEAR_TEST_KEY", "lin_api_xyz")
	a, err := New(map[string]interface{}{
		"api_key_env": "LINEAR_TEST_KEY",
		"team_key":    "ENG",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	la := a.(*Adapter)
	if la.apiKey != "lin_api_xyz" {
		t.Errorf("apiKey = %q, want lin_api_xyz", la.apiKey)
	}
	if la.teamKey != "ENG" {
		t.Errorf("teamKey = %q", la.teamKey)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		t    string
		want providers.Status
	}{
		{"backlog", providers.StatusOpen},
		{"unstarted", providers.StatusOpen},
		{"triage", providers.StatusOpen},
		{"started", providers.StatusInProgress},
		{"completed", providers.StatusClosed},
		{"canceled", providers.StatusClosed},
	}
	for _, c := range cases {
		if got := fromLinearStateType(c.t); got != c.want {
			t.Errorf("fromLinearStateType(%q) = %v, want %v", c.t, got, c.want)
		}
	}
}

func TestStatusToLinearTypes_BlockedOpenInProgress(t *testing.T) {
	if len(statusToLinearTypes(providers.StatusInProgress)) != 1 {
		t.Errorf("InProgress should map to one type")
	}
	if len(statusToLinearTypes(providers.StatusOpen)) < 2 {
		t.Errorf("Open should map to multiple types (backlog, unstarted, triage)")
	}
	if len(statusToLinearTypes(providers.StatusClosed)) != 2 {
		t.Errorf("Closed should map to completed + canceled")
	}
}

func TestLinearToProvider_TypicalShape(t *testing.T) {
	raw := `{
		"id": "uuid-1",
		"identifier": "ENG-42",
		"title": "fix thing",
		"description": "body",
		"priority": 1,
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-02T00:00:00Z",
		"state": { "name": "In Progress", "type": "started" },
		"assignee": { "name": "Alice" },
		"team": { "key": "ENG" },
		"parent": { "identifier": "ENG-1" },
		"labels": { "nodes": [{"name": "bug"}, {"name": "p0"}] },
		"relations": { "nodes": [{"type": "blocks", "relatedIssue": {"identifier": "ENG-50"}}] },
		"inverseRelations": { "nodes": [{"type": "blocks", "issue": {"identifier": "ENG-2"}}] }
	}`
	var l linearIssue
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := linearToProvider(l)
	if got.ID != "ENG-42" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Status != providers.StatusInProgress {
		t.Errorf("Status = %v", got.Status)
	}
	if got.Parent != "ENG-1" {
		t.Errorf("Parent = %q", got.Parent)
	}
	if len(got.Labels) != 2 {
		t.Errorf("Labels = %v", got.Labels)
	}
	if len(got.Blocks) != 1 || got.Blocks[0] != "ENG-50" {
		t.Errorf("Blocks = %v", got.Blocks)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0] != "ENG-2" {
		t.Errorf("BlockedBy = %v", got.BlockedBy)
	}
}

// gqlMockServer captures a sequence of canned responses indexed by query
// substring; tests can assert on the requests received.
type gqlMockServer struct {
	t           *testing.T
	server      *httptest.Server
	responses   map[string]string
	requests    []map[string]interface{}
	requestBody []byte
}

func newGQLMock(t *testing.T) *gqlMockServer {
	m := &gqlMockServer{t: t, responses: map[string]string{}}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.requestBody = body
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		m.requests = append(m.requests, req)

		query, _ := req["query"].(string)
		for needle, resp := range m.responses {
			if strings.Contains(query, needle) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(resp))
				return
			}
		}
		http.Error(w, "no canned response for query: "+query, http.StatusInternalServerError)
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *gqlMockServer) on(querySubstring, response string) {
	m.responses[querySubstring] = response
}

func TestShow_RoundtripsThroughGraphQL(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`issue(id:`, `{
		"data": {
			"issue": {
				"id": "uuid-1",
				"identifier": "ENG-42",
				"title": "demo",
				"description": "body",
				"state": { "name": "Todo", "type": "unstarted" },
				"team": { "key": "ENG" },
				"labels": { "nodes": [] },
				"relations": { "nodes": [] },
				"inverseRelations": { "nodes": [] },
				"children": { "nodes": [] }
			}
		}
	}`)

	a, _ := New(map[string]interface{}{
		"api_key":  "test",
		"team_key": "ENG",
		"endpoint": mock.server.URL,
	})
	got, err := a.Show(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "ENG-42" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Status != providers.StatusOpen {
		t.Errorf("Status = %v", got.Status)
	}
}

func TestShow_NotFoundMapsToProviderError(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`issue(id:`, `{ "data": { "issue": { "id": "" } } }`)

	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	_, err := a.Show(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected ErrNotFound-style error, got %v", err)
	}
}

func TestList_BuildsFilterFromStatus(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`issues(filter`, `{
		"data": {
			"issues": {
				"nodes": [{
					"id": "u",
					"identifier": "ENG-1",
					"title": "t",
					"state": { "name": "s", "type": "started" },
					"team": { "key": "ENG" },
					"labels": { "nodes": [] },
					"relations": { "nodes": [] },
					"inverseRelations": { "nodes": [] },
					"children": { "nodes": [] }
				}]
			}
		}
	}`)

	a, _ := New(map[string]interface{}{"api_key": "test", "team_key": "ENG", "endpoint": mock.server.URL})
	got, err := a.List(context.Background(), providers.ListFilter{
		Status: []providers.Status{providers.StatusInProgress},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}

	last := mock.requests[len(mock.requests)-1]
	vars, _ := last["variables"].(map[string]interface{})
	filter, _ := vars["filter"].(map[string]interface{})
	state, _ := filter["state"].(map[string]interface{})
	typeFilter, _ := state["type"].(map[string]interface{})
	in, _ := typeFilter["in"].([]interface{})
	if len(in) != 1 || in[0] != "started" {
		t.Errorf("expected state.type.in = [started], got %v", in)
	}
}

func TestPrime_AggregatesCounts(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`issues(filter`, `{
		"data": {
			"issues": {
				"nodes": [
					{"id":"u1","identifier":"ENG-1","title":"a","state":{"type":"unstarted","name":"Todo"},"team":{"key":"ENG"},"labels":{"nodes":[]},"relations":{"nodes":[]},"inverseRelations":{"nodes":[]},"children":{"nodes":[]}},
					{"id":"u2","identifier":"ENG-2","title":"b","state":{"type":"started","name":"Doing"},"team":{"key":"ENG"},"labels":{"nodes":[]},"relations":{"nodes":[]},"inverseRelations":{"nodes":[]},"children":{"nodes":[]}},
					{"id":"u3","identifier":"ENG-3","title":"c","state":{"type":"completed","name":"Done"},"team":{"key":"ENG"},"labels":{"nodes":[]},"relations":{"nodes":[]},"inverseRelations":{"nodes":[]},"children":{"nodes":[]}}
				]
			}
		}
	}`)
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	out, err := a.Prime(context.Background(), providers.PrimeOptions{})
	if err != nil {
		t.Fatalf("Prime: %v", err)
	}
	for _, want := range []string{"Total: 3", "In Progress: 1", "Closed: 1", "Open: 1", "ENG-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("Prime output missing %q:\n%s", want, out)
		}
	}
}

func TestResolveStateID_HonorsStatusOverride(t *testing.T) {
	mock := newGQLMock(t)
	// a generic Linear org-like Product team: two "started" states, "Started" and "Delivered".
	mock.on(`teams(filter: { key`, `{ "data": { "teams": { "nodes": [{"id": "team-uuid", "key": "PRO"}] } } }`)
	mock.on(`workflowStates(filter`, `{
		"data": {
			"workflowStates": {
				"nodes": [
					{"id": "delivered-uuid", "name": "Delivered", "type": "started"},
					{"id": "started-uuid",   "name": "Started",   "type": "started"}
				]
			}
		}
	}`)
	mock.on(`issueUpdate(`, `{ "data": { "issueUpdate": { "success": true } } }`)

	a, _ := New(map[string]interface{}{
		"api_key":  "test",
		"team_key": "PRO",
		"endpoint": mock.server.URL,
		"status_overrides": map[string]interface{}{
			"in_progress": "Started",
		},
	})
	if err := a.(*Adapter).Close(context.Background(), "PRO-1"); err != nil {
		// Close uses StatusClosed -> first-by-type; Override is for InProgress.
		// We just need the call path; verifying override below.
		_ = err
	}

	// Set status to InProgress -- should pick "Started", not "Delivered".
	id, err := a.(*Adapter).resolveStateID(context.Background(), providers.StatusInProgress)
	if err != nil {
		t.Fatalf("resolveStateID: %v", err)
	}
	if id != "started-uuid" {
		t.Errorf("override ignored: got %q, want started-uuid (Started)", id)
	}
}

func TestResolveStateID_OverrideMissingNameErrors(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`teams(filter: { key`, `{ "data": { "teams": { "nodes": [{"id": "team-uuid", "key": "PRO"}] } } }`)
	mock.on(`workflowStates(filter`, `{ "data": { "workflowStates": { "nodes": [
		{"id": "x", "name": "Backlog", "type": "unstarted"}
	] } } }`)

	a, _ := New(map[string]interface{}{
		"api_key": "test", "team_key": "PRO", "endpoint": mock.server.URL,
		"status_overrides": map[string]interface{}{"in_progress": "DoesNotExist"},
	})
	_, err := a.(*Adapter).resolveStateID(context.Background(), providers.StatusInProgress)
	if err == nil || !strings.Contains(err.Error(), "DoesNotExist") {
		t.Errorf("expected error referencing missing override name, got %v", err)
	}
}

func TestLooksLikeUUID(t *testing.T) {
	cases := map[string]bool{
		"00000000-0000-4000-8000-00000000abcd": true,
		"9c16c0c6-8618-41f3-b202-465b4c1b4a5d": true,
		"":                                     false,
		"EXM-100":                              false,
		"Acme Platform":                        false,
		"00000000-0000-4000-8000-00000000ABCD": false, // upper-case hex
		"00000000-0000-4000-8000-00000000abc":  false, // too short
	}
	for in, want := range cases {
		if got := looksLikeUUID(in); got != want {
			t.Errorf("looksLikeUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveProjectID_PassesThroughUUID(t *testing.T) {
	mock := newGQLMock(t)
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	got, err := a.(*Adapter).resolveProjectID(context.Background(), "00000000-0000-4000-8000-00000000abcd")
	if err != nil {
		t.Fatalf("resolveProjectID: %v", err)
	}
	if got != "00000000-0000-4000-8000-00000000abcd" {
		t.Errorf("UUID should pass through unchanged, got %q", got)
	}
	if len(mock.requests) != 0 {
		t.Errorf("UUID input should not hit the API, got %d requests", len(mock.requests))
	}
}

func TestResolveProjectID_LooksUpByName(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`projects(filter: { name`, `{
		"data": { "projects": { "nodes": [
			{"id": "00000000-0000-4000-8000-00000000abcd", "name": "Acme Platform"}
		] } }
	}`)
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	got, err := a.(*Adapter).resolveProjectID(context.Background(), "Acme Platform")
	if err != nil {
		t.Fatalf("resolveProjectID: %v", err)
	}
	if got != "00000000-0000-4000-8000-00000000abcd" {
		t.Errorf("expected resolved UUID, got %q", got)
	}
}

func TestResolveProjectID_NameMissErrors(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`projects(filter: { name`, `{ "data": { "projects": { "nodes": [] } } }`)
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	_, err := a.(*Adapter).resolveProjectID(context.Background(), "Nope")
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestResolveMilestoneID_ScopedToProject(t *testing.T) {
	mock := newGQLMock(t)
	mock.on(`project(id:`, `{
		"data": { "project": { "projectMilestones": { "nodes": [
			{"id": "9c16c0c6-8618-41f3-b202-465b4c1b4a5d", "name": "POC"},
			{"id": "11111111-1111-1111-1111-111111111111", "name": "GA"}
		] } } }
	}`)
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	got, err := a.(*Adapter).resolveMilestoneID(context.Background(), "00000000-0000-4000-8000-00000000abcd", "POC")
	if err != nil {
		t.Fatalf("resolveMilestoneID: %v", err)
	}
	if got != "9c16c0c6-8618-41f3-b202-465b4c1b4a5d" {
		t.Errorf("got %q, want POC's UUID", got)
	}
}

func TestLinearToProvider_SurfacesProjectAndMilestone(t *testing.T) {
	// Real shape from a generic Linear org EXM-100.
	raw := `{
		"id": "uuid-exm-100",
		"identifier": "EXM-100",
		"title": "Bug",
		"state": {"name": "Accepted", "type": "completed"},
		"team": {"key": "PRO"},
		"project": {"id": "00000000-0000-4000-8000-00000000abcd", "name": "Acme Platform"},
		"projectMilestone": {"id": "9c16c0c6-8618-41f3-b202-465b4c1b4a5d", "name": "POC"},
		"labels": {"nodes": [{"name": "example-poc"}, {"name": "bug"}]},
		"relations": {"nodes": []},
		"inverseRelations": {"nodes": []},
		"children": {"nodes": []}
	}`
	var l linearIssue
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := linearToProvider(l)
	if got.Project != "Acme Platform" {
		t.Errorf("Project = %q", got.Project)
	}
	if got.Milestone != "POC" {
		t.Errorf("Milestone = %q", got.Milestone)
	}
	if got.Extras["project_id"] != "00000000-0000-4000-8000-00000000abcd" {
		t.Errorf("Extras.project_id = %v", got.Extras["project_id"])
	}
}

func TestCreate_PassesProjectAndMilestoneToInput(t *testing.T) {
	mock := newGQLMock(t)
	// Resolution lookups (project name -> id, milestone name within project -> id)
	mock.on(`projects(filter: { name`, `{ "data": { "projects": { "nodes": [
		{"id": "proj-uuid", "name": "Acme Platform"}
	] } } }`)
	mock.on(`project(id:`, `{ "data": { "project": { "projectMilestones": { "nodes": [
		{"id": "milestone-uuid", "name": "POC"}
	] } } } }`)
	mock.on(`teams(filter: { key`, `{ "data": { "teams": { "nodes": [{"id": "team-uuid", "key": "PRO"}] } } }`)
	mock.on(`issueLabels(first`, `{ "data": { "issueLabels": { "nodes": [] } } }`)
	mock.on(`issueCreate(`, `{
		"data": { "issueCreate": { "success": true, "issue": {
			"id": "u", "identifier": "PRO-200", "title": "x",
			"state": {"name":"Backlog","type":"unstarted"}, "team": {"key":"PRO"},
			"project": {"id":"proj-uuid","name":"Acme Platform"},
			"projectMilestone": {"id":"milestone-uuid","name":"POC"},
			"labels": {"nodes": []}, "relations": {"nodes": []}, "inverseRelations": {"nodes": []}, "children": {"nodes": []}
		} } }
	}`)

	a, _ := New(map[string]interface{}{"api_key": "test", "team_key": "PRO", "endpoint": mock.server.URL})
	out, err := a.Create(context.Background(), providers.CreateIssueInput{
		Title:     "x",
		Project:   "Acme Platform",
		Milestone: "POC",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Project != "Acme Platform" || out.Milestone != "POC" {
		t.Errorf("returned issue missing project/milestone: %+v", out)
	}

	// Find the issueCreate request and verify project/milestone in the input.
	var createReq map[string]interface{}
	for _, r := range mock.requests {
		if q, _ := r["query"].(string); strings.Contains(q, "issueCreate(") {
			createReq = r
			break
		}
	}
	if createReq == nil {
		t.Fatal("no issueCreate call recorded")
	}
	vars := createReq["variables"].(map[string]interface{})
	input := vars["input"].(map[string]interface{})
	if input["projectId"] != "proj-uuid" {
		t.Errorf("projectId = %v, want proj-uuid", input["projectId"])
	}
	if input["projectMilestoneId"] != "milestone-uuid" {
		t.Errorf("projectMilestoneId = %v, want milestone-uuid", input["projectMilestoneId"])
	}
}

// TestIntegration_LiveLinearReadOnly exercises the adapter against the real
// Linear GraphQL endpoint. Skipped unless LINEAR_API_KEY is set in the env.
// LINEAR_TEAM_KEY (default "PRO") and LINEAR_TEST_ISSUE_ID (e.g. "EXM-100")
// scope what is read. The test is intentionally read-only -- no writes against
// the user's real workspace.
func TestIntegration_LiveLinearReadOnly(t *testing.T) {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		t.Skip("LINEAR_API_KEY not set; skipping live Linear verification")
	}
	teamKey := os.Getenv("LINEAR_TEAM_KEY")
	if teamKey == "" {
		teamKey = "PRO"
	}
	issueID := os.Getenv("LINEAR_TEST_ISSUE_ID")

	a, err := New(map[string]interface{}{
		"api_key":  apiKey,
		"team_key": teamKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("List", func(t *testing.T) {
		issues, err := a.List(context.Background(), providers.ListFilter{Limit: 5})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		t.Logf("List returned %d issues from team %s", len(issues), teamKey)
		for _, i := range issues {
			if i.ID == "" || i.Title == "" {
				t.Errorf("issue missing required field: %+v", i)
			}
		}
	})

	if issueID != "" {
		t.Run("Show", func(t *testing.T) {
			got, err := a.Show(context.Background(), issueID)
			if err != nil {
				t.Fatalf("Show(%s): %v", issueID, err)
			}
			t.Logf("Show(%s) -> %s [%s] project=%q milestone=%q",
				got.ID, got.Title, got.Status, got.Project, got.Milestone)
			if got.ID != issueID {
				t.Errorf("ID = %q, want %q", got.ID, issueID)
			}
		})
	}

	t.Run("Prime", func(t *testing.T) {
		out, err := a.Prime(context.Background(), providers.PrimeOptions{})
		if err != nil {
			t.Fatalf("Prime: %v", err)
		}
		if !strings.Contains(out, "Project Status") {
			t.Errorf("Prime output missing header:\n%s", out)
		}
		t.Logf("Prime output:\n%s", out)
	})
}

// TestIntegration_LiveLinearFullCycle exercises the WRITE paths against a
// real Linear workspace. Skipped without LINEAR_API_KEY. Auto-cleans every
// issue it creates via t.Cleanup -> issueDelete, even on partial failure, so
// the user's workspace is left exactly as it started.
//
// Set LINEAR_VERIFY_PROJECT (UUID or name) and LINEAR_VERIFY_MILESTONE if you
// want create/update to exercise the project + milestone resolution paths.
// Otherwise the test issues land in the team's default backlog.
func TestIntegration_LiveLinearFullCycle(t *testing.T) {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		t.Skip("LINEAR_API_KEY not set; skipping live full-cycle verification")
	}
	teamKey := os.Getenv("LINEAR_TEAM_KEY")
	if teamKey == "" {
		teamKey = "PRO"
	}
	verifyProject := os.Getenv("LINEAR_VERIFY_PROJECT")
	verifyMilestone := os.Getenv("LINEAR_VERIFY_MILESTONE")

	a, err := New(map[string]interface{}{
		"api_key":  apiKey,
		"team_key": teamKey,
		// a generic Linear org's Product team has two `started` states (Started, Delivered);
		// override so set-status -> InProgress lands in "Started" deterministically.
		"status_overrides": map[string]interface{}{
			"in_progress": "Started",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	la := a.(*Adapter)
	ctx := context.Background()

	// --- Create primary test issue ---
	primary, err := a.Create(ctx, providers.CreateIssueInput{
		Title:     "pvg-adapter-verification — safe to delete",
		Body:      "## Description\n\nAuto-created by pvg's TestIntegration_LiveLinearFullCycle. Will be deleted on test completion.\n",
		Labels:    nil, // skip labels to avoid creating new ones in the workspace
		Project:   verifyProject,
		Milestone: verifyMilestone,
	})
	if err != nil {
		t.Fatalf("Create primary: %v", err)
	}
	t.Cleanup(func() { mustDeleteLinearIssue(t, la, primary.ID, primary.Extras["uuid"]) })
	t.Logf("Created primary: %s [%s] %q (project=%q milestone=%q)",
		primary.ID, primary.Status, primary.Title, primary.Project, primary.Milestone)

	if primary.ID == "" {
		t.Fatal("primary issue missing ID")
	}
	if primary.Title == "" {
		t.Fatal("primary issue missing Title")
	}
	if verifyProject != "" && primary.Project == "" {
		t.Errorf("LINEAR_VERIFY_PROJECT set but Issue.Project came back empty")
	}
	if verifyMilestone != "" && primary.Milestone == "" {
		t.Errorf("LINEAR_VERIFY_MILESTONE set but Issue.Milestone came back empty")
	}

	// --- Show: verify the issue is fully populated when re-fetched ---
	got, err := a.Show(ctx, primary.ID)
	if err != nil {
		t.Fatalf("Show after Create: %v", err)
	}
	if got.Title != primary.Title {
		t.Errorf("Show Title = %q, want %q", got.Title, primary.Title)
	}

	// --- Update title + body ---
	newTitle := primary.Title + " (renamed)"
	newBody := got.Body + "\n\nUpdated by pvg verification.\n"
	updated, err := a.Update(ctx, primary.ID, providers.UpdateIssueInput{
		Title: &newTitle,
		Body:  &newBody,
	})
	if err != nil {
		t.Fatalf("Update title+body: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("after Update Title = %q, want %q", updated.Title, newTitle)
	}

	// --- Update status to InProgress (verifies status_overrides path) ---
	inProg := providers.StatusInProgress
	stateUpdated, err := a.Update(ctx, primary.ID, providers.UpdateIssueInput{Status: &inProg})
	if err != nil {
		t.Fatalf("Update status -> in_progress: %v", err)
	}
	t.Logf("After in_progress Update: Status=%v, native_state=%v", stateUpdated.Status, stateUpdated.Extras["state"])
	if stateUpdated.Status != providers.StatusInProgress {
		t.Errorf("Status not in_progress after update: %v", stateUpdated.Status)
	}
	if stateUpdated.Extras["state"] != "Started" {
		t.Errorf("status_overrides did not pick 'Started' (got %q)", stateUpdated.Extras["state"])
	}

	// --- Add a comment, then list it back ---
	commentBody := "verification comment from pvg"
	if _, err := a.AddComment(ctx, primary.ID, commentBody); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	comments, err := a.ListComments(ctx, primary.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	found := false
	for _, c := range comments {
		if strings.Contains(c.Body, commentBody) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("appended comment not found in ListComments; got %d comments", len(comments))
	}

	// --- Create a second issue and Link it as a blocker ---
	blocker, err := a.Create(ctx, providers.CreateIssueInput{
		Title:   "pvg-adapter-verification BLOCKER — safe to delete",
		Body:    "Auto-created blocker. Deleted with primary.\n",
		Project: verifyProject,
	})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	t.Cleanup(func() { mustDeleteLinearIssue(t, la, blocker.ID, blocker.Extras["uuid"]) })

	if err := a.Link(ctx, blocker.ID, primary.ID, providers.LinkBlocks); err != nil {
		t.Fatalf("Link blocker -> primary: %v", err)
	}
	primaryAfterLink, err := a.Show(ctx, primary.ID)
	if err != nil {
		t.Fatalf("Show after Link: %v", err)
	}
	if !contains(primaryAfterLink.BlockedBy, blocker.ID) {
		t.Errorf("primary.BlockedBy %v missing %s", primaryAfterLink.BlockedBy, blocker.ID)
	}
	blockerAfterLink, err := a.Show(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("Show blocker after Link: %v", err)
	}
	if !contains(blockerAfterLink.Blocks, primary.ID) {
		t.Errorf("blocker.Blocks %v missing %s", blockerAfterLink.Blocks, primary.ID)
	}

	// --- Close primary, then Reopen ---
	if err := a.Close(ctx, primary.ID); err != nil {
		t.Fatalf("Close primary: %v", err)
	}
	closedPrim, _ := a.Show(ctx, primary.ID)
	if closedPrim.Status != providers.StatusClosed {
		t.Errorf("after Close Status = %v, want closed", closedPrim.Status)
	}

	if err := a.Reopen(ctx, primary.ID); err != nil {
		t.Fatalf("Reopen primary: %v", err)
	}
	reopened, _ := a.Show(ctx, primary.ID)
	if reopened.Status == providers.StatusClosed {
		t.Errorf("after Reopen Status still closed")
	}

	t.Log("Full cycle PASS — primary + blocker will be deleted by t.Cleanup")
}

// mustDeleteLinearIssue archives + permanently deletes a Linear issue via
// direct GraphQL. The provider abstraction has no Delete op (issues are
// closed, not deleted, in the canonical model), but for cleanup of test data
// we go straight to issueDelete.
func mustDeleteLinearIssue(t *testing.T, a *Adapter, identifier string, uuidExtra interface{}) {
	t.Helper()
	uuid, _ := uuidExtra.(string)
	if uuid == "" {
		t.Logf("WARN: no UUID for %s in Extras; cannot delete", identifier)
		return
	}
	var resp struct {
		IssueDelete struct {
			Success bool `json:"success"`
		} `json:"issueDelete"`
	}
	const mut = `mutation($id: String!) { issueDelete(id: $id) { success } }`
	if err := a.gql(context.Background(), mut, map[string]interface{}{"id": uuid}, &resp); err != nil {
		t.Logf("WARN: issueDelete(%s) failed: %v -- delete manually", identifier, err)
		return
	}
	if !resp.IssueDelete.Success {
		t.Logf("WARN: issueDelete(%s) returned success=false -- delete manually", identifier)
		return
	}
	t.Logf("Deleted %s (uuid=%s)", identifier, uuid)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestUnauthorized_PropagatesAsErrUnauthorized(t *testing.T) {
	mock := newGQLMock(t)
	mock.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	a, _ := New(map[string]interface{}{"api_key": "test", "endpoint": mock.server.URL})
	_, err := a.Show(context.Background(), "anything")
	if err != providers.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
