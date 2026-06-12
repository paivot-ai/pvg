package ndadapter

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/providers"
)

func TestRegistration(t *testing.T) {
	a, err := providers.BuildBacklog("nd", map[string]interface{}{"vault": "/tmp/x"})
	if err != nil {
		t.Fatalf("BuildBacklog: %v", err)
	}
	if a.Name() != "nd" {
		t.Errorf("Name = %q, want nd", a.Name())
	}
}

func TestNew_DefaultsVaultDir(t *testing.T) {
	a, err := New(map[string]interface{}{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := a.(*Adapter).vault
	if got != ".vault" {
		t.Errorf("vault default = %q, want .vault", got)
	}
}

func TestCapabilities_DeclaresExpectedFlags(t *testing.T) {
	a, _ := New(map[string]interface{}{})
	caps := a.Capabilities()
	for _, want := range []providers.Capability{providers.CapDefer, providers.CapArchive, providers.CapDoctor} {
		if !caps.Has(want) {
			t.Errorf("missing capability %s", want)
		}
	}
	for _, notWant := range []providers.Capability{providers.CapAttachments, providers.CapCycles} {
		if caps.Has(notWant) {
			t.Errorf("unexpected capability %s", notWant)
		}
	}
}

func TestStatusMapping_RoundTrip(t *testing.T) {
	cases := []struct {
		nd  string
		api providers.Status
	}{
		{"open", providers.StatusOpen},
		{"in_progress", providers.StatusInProgress},
		{"blocked", providers.StatusBlocked},
		{"deferred", providers.StatusDeferred},
		{"closed", providers.StatusClosed},
	}
	for _, c := range cases {
		if got := fromNdStatus(c.nd); got != c.api {
			t.Errorf("fromNdStatus(%q) = %v, want %v", c.nd, got, c.api)
		}
		if got := ndStatus(c.api); got != c.nd {
			t.Errorf("ndStatus(%v) = %q, want %q", c.api, got, c.nd)
		}
	}
}

func TestDecodeIssue_TypicalShape(t *testing.T) {
	raw := []byte(`{
		"ID": "VP-001",
		"Title": "Test issue",
		"Status": "in_progress",
		"Priority": 1,
		"Type": "task",
		"Assignee": "alice",
		"Labels": ["urgent", "ux"],
		"Parent": "VP-epic",
		"Blocks": ["VP-002"],
		"BlockedBy": ["VP-003"],
		"CreatedAt": "2026-01-01T00:00:00Z",
		"UpdatedAt": "2026-01-02T00:00:00Z",
		"FilePath": "issues/VP-001.md",
		"Body": "## Description\nbody"
	}`)
	got, err := decodeIssue(raw)
	if err != nil {
		t.Fatalf("decodeIssue: %v", err)
	}
	if got.ID != "VP-001" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Status != providers.StatusInProgress {
		t.Errorf("Status = %v", got.Status)
	}
	if got.Parent != "VP-epic" {
		t.Errorf("Parent = %q", got.Parent)
	}
	if len(got.Blocks) != 1 || got.Blocks[0] != "VP-002" {
		t.Errorf("Blocks = %v", got.Blocks)
	}
	if got.Extras["priority"].(int) != 1 {
		t.Errorf("priority extra = %v", got.Extras["priority"])
	}
}

func TestDecodeIssue_SurfacesWasBlockedByAndAllBlockedBy(t *testing.T) {
	// nd archives a satisfied blocker out of BlockedBy into WasBlockedBy when
	// the blocker closes. The normalized Issue must keep both, plus a deduped,
	// sorted lifetime union in AllBlockedBy. VP-003 overlaps both lists.
	raw := []byte(`{
		"ID": "VP-001",
		"Title": "Union issue",
		"Status": "open",
		"Blocks": ["VP-100"],
		"BlockedBy": ["VP-003", "VP-005"],
		"WasBlockedBy": ["VP-004", "VP-003", "VP-002"],
		"FilePath": "issues/VP-001.md"
	}`)
	got, err := decodeIssue(raw)
	if err != nil {
		t.Fatalf("decodeIssue: %v", err)
	}
	if want := []string{"VP-003", "VP-005"}; !equalStrings(got.BlockedBy, want) {
		t.Errorf("BlockedBy = %v, want %v", got.BlockedBy, want)
	}
	if want := []string{"VP-004", "VP-003", "VP-002"}; !equalStrings(got.WasBlockedBy, want) {
		t.Errorf("WasBlockedBy = %v, want %v", got.WasBlockedBy, want)
	}
	wantUnion := []string{"VP-002", "VP-003", "VP-004", "VP-005"}
	if !equalStrings(got.AllBlockedBy, wantUnion) {
		t.Errorf("AllBlockedBy = %v, want %v (deduped + sorted)", got.AllBlockedBy, wantUnion)
	}
}

func TestNdToProvider_AllBlockedByNilWhenNoEdges(t *testing.T) {
	got := ndToProvider(ndIssue{ID: "VP-1"})
	if got.AllBlockedBy != nil {
		t.Errorf("AllBlockedBy = %v, want nil when no blockers", got.AllBlockedBy)
	}
	if got.WasBlockedBy != nil {
		t.Errorf("WasBlockedBy = %v, want nil", got.WasBlockedBy)
	}
}

func TestDecodeIssueList_NullIsEmpty(t *testing.T) {
	got, err := decodeIssueList([]byte("null"))
	if err != nil {
		t.Fatalf("decodeIssueList(null): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestDecodeIssueList_EmptyIsEmpty(t *testing.T) {
	got, err := decodeIssueList([]byte(""))
	if err != nil {
		t.Fatalf("decodeIssueList(empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestDecodeComments_JSON(t *testing.T) {
	got, err := decodeComments([]byte(`[{
		"author": "alice",
		"body": "plain body",
		"created_at": "2026-05-20T07:09:23Z"
	}]`))
	if err != nil {
		t.Fatalf("decodeComments(JSON): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Author != "alice" || got[0].Body != "plain body" {
		t.Errorf("comment = %+v", got[0])
	}
}

func TestDecodeComments_NdMarkdownWithHeadingBody(t *testing.T) {
	raw := []byte(`## Comments

### 2026-05-20T07:09:23Z alice
## Comment Heading
Body keeps markdown.
### Body Subheading
This heading is part of the body.

### 2026-05-20T08:10:11Z bob
Second body
`)
	got, err := decodeComments(raw)
	if err != nil {
		t.Fatalf("decodeComments(markdown): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Author != "alice" {
		t.Errorf("first author = %q, want alice", got[0].Author)
	}
	for _, fragment := range []string{"## Comment Heading", "### Body Subheading", "This heading is part of the body."} {
		if !strings.Contains(got[0].Body, fragment) {
			t.Errorf("first body missing %q: %q", fragment, got[0].Body)
		}
	}
	if got[1].Author != "bob" || got[1].Body != "Second body" {
		t.Errorf("second comment = %+v", got[1])
	}
}

func TestDecodeComments_NdMarkdownNoComments(t *testing.T) {
	got, err := decodeComments([]byte("## Comments\n"))
	if err != nil {
		t.Fatalf("decodeComments(no comments): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestAppendListFilter_LabelsAndParent(t *testing.T) {
	args := appendListFilter([]string{"list", "--json"}, providers.ListFilter{
		Labels: []string{"a", "b"},
		Parent: "VP-epic",
	})
	got := strings.Join(args, " ")
	for _, fragment := range []string{"--label a", "--label b", "--parent VP-epic"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("args missing %q: %s", fragment, got)
		}
	}
}

func TestAppendListFilter_StatusOpenOmitsAll(t *testing.T) {
	args := appendListFilter(nil, providers.ListFilter{Status: []providers.Status{providers.StatusOpen}})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--status open") {
		t.Errorf("missing --status open: %s", got)
	}
	if strings.Contains(got, "--all") {
		t.Errorf("should not include --all when status=open: %s", got)
	}
}

func TestAppendListFilter_NoStatusIncludesAll(t *testing.T) {
	args := appendListFilter(nil, providers.ListFilter{})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--all") {
		t.Errorf("expected --all when status filter empty: %s", got)
	}
}

func TestAppendListFilter_TypeAndSort(t *testing.T) {
	args := appendListFilter([]string{"list", "--json"}, providers.ListFilter{
		Type: "epic",
		Sort: "priority",
	})
	got := strings.Join(args, " ")
	for _, fragment := range []string{"--type epic", "--sort priority"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("args missing %q: %s", fragment, got)
		}
	}
}

func TestAppendListFilter_OmitsTypeAndSortWhenEmpty(t *testing.T) {
	args := appendListFilter(nil, providers.ListFilter{})
	got := strings.Join(args, " ")
	if strings.Contains(got, "--type") || strings.Contains(got, "--sort") {
		t.Errorf("empty Type/Sort must not emit flags: %s", got)
	}
}

func TestClose_PassesReasonFlag(t *testing.T) {
	var calls [][]string
	old := execCommandContext
	execCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommandContext = old }()

	a, _ := New(map[string]interface{}{"vault": "/tmp/v"})
	if err := a.Close(context.Background(), "VP-1", "duplicate of VP-2"); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := []string{"nd", "--vault", "/tmp/v", "close", "VP-1", "--reason", "duplicate of VP-2"}
	if len(calls) != 1 || strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected nd call: got %#v want %#v", calls, want)
	}
}

func TestClose_OmitsReasonFlagWhenEmpty(t *testing.T) {
	var calls [][]string
	old := execCommandContext
	execCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	defer func() { execCommandContext = old }()

	a, _ := New(map[string]interface{}{"vault": "/tmp/v"})
	if err := a.Close(context.Background(), "VP-1", ""); err != nil {
		t.Fatalf("Close: %v", err)
	}

	want := []string{"nd", "--vault", "/tmp/v", "close", "VP-1"}
	if len(calls) != 1 || strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected nd call: got %#v want %#v", calls, want)
	}
}

// --- Integration: requires nd binary ---

func TestIntegration_CreateShowUpdateClose(t *testing.T) {
	if _, err := exec.LookPath("nd"); err != nil {
		t.Skip("nd binary not on PATH; skipping integration")
	}
	vault := initVault(t)

	a, err := New(map[string]interface{}{"vault": vault})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	created, err := a.Create(ctx, providers.CreateIssueInput{
		Title:  "Adapter test issue",
		Body:   "## Description\nbody body",
		Labels: []string{"adapter", "smoke"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty ID after Create")
	}

	got, err := a.Show(ctx, created.ID)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Title != "Adapter test issue" {
		t.Errorf("Title = %q", got.Title)
	}
	if !containsLabel(got.Labels, "adapter") {
		t.Errorf("missing label adapter: %v", got.Labels)
	}

	newTitle := "Adapter test issue (renamed)"
	updated, err := a.Update(ctx, created.ID, providers.UpdateIssueInput{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("after Update Title = %q, want %q", updated.Title, newTitle)
	}

	if err := a.Close(ctx, created.ID, ""); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, _ := a.Show(ctx, created.ID)
	if closed.Status != providers.StatusClosed {
		t.Errorf("after Close Status = %v, want closed", closed.Status)
	}

	if err := a.Reopen(ctx, created.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	reopened, _ := a.Show(ctx, created.ID)
	if reopened.Status == providers.StatusClosed {
		t.Errorf("after Reopen Status = %v, did not reopen", reopened.Status)
	}
}

func TestIntegration_LinkBlocksReadyBlocked(t *testing.T) {
	if _, err := exec.LookPath("nd"); err != nil {
		t.Skip("nd binary not on PATH; skipping integration")
	}
	vault := initVault(t)
	a, _ := New(map[string]interface{}{"vault": vault})
	ctx := context.Background()

	blocker, err := a.Create(ctx, providers.CreateIssueInput{Title: "Blocker"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	blocked, err := a.Create(ctx, providers.CreateIssueInput{Title: "Blocked"})
	if err != nil {
		t.Fatalf("Create blocked: %v", err)
	}

	if err := a.Link(ctx, blocker.ID, blocked.ID, providers.LinkBlocks); err != nil {
		t.Fatalf("Link Blocks: %v", err)
	}

	ready, err := a.Ready(ctx, providers.ReadyFilter{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, r := range ready {
		if r.ID == blocked.ID {
			t.Errorf("blocked issue %s should not appear in Ready", blocked.ID)
		}
	}

	blockedList, err := a.Blocked(ctx)
	if err != nil {
		t.Fatalf("Blocked: %v", err)
	}
	if !containsID(blockedList, blocked.ID) {
		t.Errorf("Blocked list missing %s: %v", blocked.ID, idsOf(blockedList))
	}
}

func TestIntegration_PrimeReturnsSummary(t *testing.T) {
	if _, err := exec.LookPath("nd"); err != nil {
		t.Skip("nd binary not on PATH; skipping integration")
	}
	vault := initVault(t)
	a, _ := New(map[string]interface{}{"vault": vault})
	ctx := context.Background()

	out, err := a.Prime(ctx, providers.PrimeOptions{})
	if err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if !strings.Contains(out, "Project Status") {
		t.Errorf("Prime output missing 'Project Status' header: %s", out)
	}
}

// --- helpers ---

func initVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("nd", "init", "--vault", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("nd init: %v: %s", err, out)
	}
	return dir
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func containsID(issues []providers.Issue, want string) bool {
	for _, i := range issues {
		if i.ID == want {
			return true
		}
	}
	return false
}

func idsOf(issues []providers.Issue) []string {
	ids := make([]string, len(issues))
	for i, x := range issues {
		ids[i] = x.ID
	}
	return ids
}
