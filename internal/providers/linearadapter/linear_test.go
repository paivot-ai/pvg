package linearadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
