package providers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingBacklog tracks every method call so tests can assert read-vs-write
// routing precisely.
type recordingBacklog struct {
	mu        sync.Mutex
	name      string
	calls     []string
	failOn    map[string]error
	createOut Issue
}

func newRecordingBacklog(name string) *recordingBacklog {
	return &recordingBacklog{name: name, failOn: map[string]error{}}
}

func (r *recordingBacklog) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, op)
}

func (r *recordingBacklog) calledOps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingBacklog) Name() string                { return r.name }
func (r *recordingBacklog) Capabilities() CapabilitySet { return NewCapabilitySet() }

func (r *recordingBacklog) Create(_ context.Context, _ CreateIssueInput) (Issue, error) {
	r.record("Create")
	if e, ok := r.failOn["Create"]; ok {
		return Issue{}, e
	}
	return r.createOut, nil
}
func (r *recordingBacklog) Show(_ context.Context, _ string) (Issue, error) {
	r.record("Show")
	return r.createOut, r.failOn["Show"]
}
func (r *recordingBacklog) List(_ context.Context, _ ListFilter) ([]Issue, error) {
	r.record("List")
	return nil, r.failOn["List"]
}
func (r *recordingBacklog) Update(_ context.Context, _ string, _ UpdateIssueInput) (Issue, error) {
	r.record("Update")
	return r.createOut, r.failOn["Update"]
}
func (r *recordingBacklog) Close(_ context.Context, _, _ string) error {
	r.record("Close")
	return r.failOn["Close"]
}
func (r *recordingBacklog) Reopen(_ context.Context, _ string) error {
	r.record("Reopen")
	return r.failOn["Reopen"]
}
func (r *recordingBacklog) AddComment(_ context.Context, _, _ string) (Comment, error) {
	r.record("AddComment")
	return Comment{}, r.failOn["AddComment"]
}
func (r *recordingBacklog) ListComments(_ context.Context, _ string) ([]Comment, error) {
	r.record("ListComments")
	return nil, r.failOn["ListComments"]
}
func (r *recordingBacklog) Link(_ context.Context, _, _ string, _ LinkKind) error {
	r.record("Link")
	return r.failOn["Link"]
}
func (r *recordingBacklog) Unlink(_ context.Context, _, _ string, _ LinkKind) error {
	r.record("Unlink")
	return r.failOn["Unlink"]
}
func (r *recordingBacklog) Ready(_ context.Context, _ ReadyFilter) ([]Issue, error) {
	r.record("Ready")
	return nil, r.failOn["Ready"]
}
func (r *recordingBacklog) Blocked(_ context.Context) ([]Issue, error) {
	r.record("Blocked")
	return nil, r.failOn["Blocked"]
}
func (r *recordingBacklog) Prime(_ context.Context, _ PrimeOptions) (string, error) {
	r.record("Prime")
	return "", r.failOn["Prime"]
}

// captureLogger collects MirrorRecords for assertions.
type captureLogger struct {
	mu      sync.Mutex
	records []MirrorRecord
}

func (c *captureLogger) MirrorAttempt(r MirrorRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
}

func (c *captureLogger) snapshot() []MirrorRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]MirrorRecord, len(c.records))
	copy(out, c.records)
	return out
}

func TestBacklogRouter_ReadsHitPrimaryOnly(t *testing.T) {
	primary := newRecordingBacklog("primary")
	mirror := newRecordingBacklog("mirror")
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{mirror}, logger)

	if _, err := r.Show(context.Background(), "X-1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if _, err := r.List(context.Background(), ListFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := r.Ready(context.Background(), ReadyFilter{}); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := r.Prime(context.Background(), PrimeOptions{}); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	if len(mirror.calledOps()) != 0 {
		t.Errorf("mirror should never see reads, got %v", mirror.calledOps())
	}
	if len(logger.snapshot()) != 0 {
		t.Errorf("no mirror records expected on reads, got %v", logger.snapshot())
	}
}

func TestBacklogRouter_WritesFanOutToMirror(t *testing.T) {
	primary := newRecordingBacklog("primary")
	mirror := newRecordingBacklog("mirror")
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{mirror}, logger)

	if _, err := r.Create(context.Background(), CreateIssueInput{Title: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.Close(context.Background(), "X-1", ""); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.Link(context.Background(), "X-1", "X-2", LinkBlocks); err != nil {
		t.Fatalf("Link: %v", err)
	}

	wantPrimary := []string{"Create", "Close", "Link"}
	if got := primary.calledOps(); !equalStrings(got, wantPrimary) {
		t.Errorf("primary calls = %v, want %v", got, wantPrimary)
	}
	if got := mirror.calledOps(); !equalStrings(got, wantPrimary) {
		t.Errorf("mirror calls = %v, want %v", got, wantPrimary)
	}

	records := logger.snapshot()
	if len(records) != 3 {
		t.Fatalf("expected 3 mirror records, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Adapter != "mirror" {
			t.Errorf("rec.Adapter = %q, want mirror", rec.Adapter)
		}
		if rec.Err != nil {
			t.Errorf("rec.Err = %v, want nil", rec.Err)
		}
	}
}

func TestBacklogRouter_PrimaryFailureSkipsMirrors(t *testing.T) {
	primary := newRecordingBacklog("primary")
	primary.failOn["Create"] = errors.New("boom")

	mirror := newRecordingBacklog("mirror")
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{mirror}, logger)

	if _, err := r.Create(context.Background(), CreateIssueInput{Title: "x"}); err == nil {
		t.Fatal("expected primary error to bubble up")
	}
	if got := mirror.calledOps(); len(got) != 0 {
		t.Errorf("mirror should not be called on primary failure, got %v", got)
	}
	if got := logger.snapshot(); len(got) != 0 {
		t.Errorf("no mirror records expected on primary failure, got %v", got)
	}
}

func TestBacklogRouter_MirrorFailureLoggedNotReturned(t *testing.T) {
	primary := newRecordingBacklog("primary")

	flakyMirror := newRecordingBacklog("flaky")
	flakyMirror.failOn["Create"] = errors.New("mirror is down")

	healthyMirror := newRecordingBacklog("healthy")

	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{flakyMirror, healthyMirror}, logger)

	out, err := r.Create(context.Background(), CreateIssueInput{Title: "x"})
	if err != nil {
		t.Fatalf("router should not return mirror failure: %v", err)
	}
	_ = out

	if got := healthyMirror.calledOps(); len(got) != 1 {
		t.Errorf("healthy mirror should still be called when flaky one fails, got %v", got)
	}

	records := logger.snapshot()
	if len(records) != 2 {
		t.Fatalf("expected 2 mirror records, got %d", len(records))
	}
	if records[0].Err == nil || records[1].Err != nil {
		t.Errorf("expected first record to have err and second to be nil, got %+v", records)
	}
}

func TestBacklogRouter_MirrorPanicCaught(t *testing.T) {
	primary := newRecordingBacklog("primary")
	panicMirror := &panickingBacklog{name: "panic"}
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{panicMirror}, logger)

	out, err := r.Create(context.Background(), CreateIssueInput{Title: "x"})
	if err != nil {
		t.Fatalf("panic in mirror must not bubble: %v", err)
	}
	_ = out

	records := logger.snapshot()
	if len(records) != 1 || records[0].Err == nil {
		t.Fatalf("expected one failure record, got %+v", records)
	}
}

func TestBacklogRouter_NoMirrorsBehavesLikePassthrough(t *testing.T) {
	primary := newRecordingBacklog("primary")
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, nil, logger)

	if _, err := r.Create(context.Background(), CreateIssueInput{Title: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(logger.snapshot()) != 0 {
		t.Errorf("no mirrors -> no records, got %v", logger.snapshot())
	}
}

func TestBacklogRouter_PrimaryAccessor(t *testing.T) {
	primary := newRecordingBacklog("primary")
	mirror := newRecordingBacklog("mirror")
	r := NewBacklogRouter(primary, []BacklogAdapter{mirror}, nil)

	if r.Primary() != primary {
		t.Errorf("Primary() returned wrong adapter")
	}
	if got := r.Mirrors(); len(got) != 1 || got[0] != mirror {
		t.Errorf("Mirrors() = %v, want [mirror]", got)
	}
}

func TestNewBacklogRouter_NilPrimaryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil primary")
		}
	}()
	NewBacklogRouter(nil, nil, nil)
}

// --- NotesRouter parity tests ---

type recordingNotes struct {
	mu     sync.Mutex
	name   string
	calls  []string
	failOn map[string]error
}

func newRecordingNotes(name string) *recordingNotes {
	return &recordingNotes{name: name, failOn: map[string]error{}}
}

func (r *recordingNotes) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, op)
}

func (r *recordingNotes) calledOps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingNotes) Name() string                { return r.name }
func (r *recordingNotes) Capabilities() CapabilitySet { return NewCapabilitySet() }
func (r *recordingNotes) Search(_ context.Context, _ string, _ int) ([]SearchHit, error) {
	r.record("Search")
	return nil, r.failOn["Search"]
}
func (r *recordingNotes) Create(_ context.Context, _ CreateNoteInput) (Note, error) {
	r.record("Create")
	return Note{}, r.failOn["Create"]
}
func (r *recordingNotes) Read(_ context.Context, _ NoteRef) (Note, error) {
	r.record("Read")
	return Note{}, r.failOn["Read"]
}
func (r *recordingNotes) Append(_ context.Context, _ NoteRef, _ string) (Note, error) {
	r.record("Append")
	return Note{}, r.failOn["Append"]
}
func (r *recordingNotes) List(_ context.Context, _ string) ([]NoteRef, error) {
	r.record("List")
	return nil, r.failOn["List"]
}
func (r *recordingNotes) GetProperty(_ context.Context, _ NoteRef, _ string) (interface{}, error) {
	r.record("GetProperty")
	return nil, r.failOn["GetProperty"]
}
func (r *recordingNotes) SetProperty(_ context.Context, _ NoteRef, _ string, _ interface{}) error {
	r.record("SetProperty")
	return r.failOn["SetProperty"]
}

func TestNotesRouter_WritesFanOut(t *testing.T) {
	primary := newRecordingNotes("primary")
	mirror := newRecordingNotes("mirror")
	logger := &captureLogger{}
	r := NewNotesRouter(primary, []NotesAdapter{mirror}, logger)

	if _, err := r.Create(context.Background(), CreateNoteInput{Title: "n"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.SetProperty(context.Background(), NoteRef{Path: "n.md"}, "k", "v"); err != nil {
		t.Fatalf("SetProperty: %v", err)
	}

	if got := mirror.calledOps(); len(got) != 2 {
		t.Errorf("mirror calls = %v, want 2", got)
	}
}

func TestNotesRouter_ReadsHitPrimaryOnly(t *testing.T) {
	primary := newRecordingNotes("primary")
	mirror := newRecordingNotes("mirror")
	r := NewNotesRouter(primary, []NotesAdapter{mirror}, nil)

	if _, err := r.Read(context.Background(), NoteRef{Path: "n.md"}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := r.Search(context.Background(), "q", 10); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := mirror.calledOps(); len(got) != 0 {
		t.Errorf("mirror should never see reads, got %v", got)
	}
}

func TestMirrorRecordIncludesDuration(t *testing.T) {
	primary := newRecordingBacklog("primary")
	slow := &slowBacklog{name: "slow", delay: 10 * time.Millisecond}
	logger := &captureLogger{}
	r := NewBacklogRouter(primary, []BacklogAdapter{slow}, logger)

	if _, err := r.Create(context.Background(), CreateIssueInput{Title: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	records := logger.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Duration < 5*time.Millisecond {
		t.Errorf("expected Duration to capture mirror latency, got %v", records[0].Duration)
	}
}

// --- minimal adapters used by panic and slow tests ---

type panickingBacklog struct{ name string }

func (p *panickingBacklog) Name() string                { return p.name }
func (p *panickingBacklog) Capabilities() CapabilitySet { return NewCapabilitySet() }
func (p *panickingBacklog) Create(context.Context, CreateIssueInput) (Issue, error) {
	panic("boom")
}
func (p *panickingBacklog) Show(context.Context, string) (Issue, error)       { panic("nope") }
func (p *panickingBacklog) List(context.Context, ListFilter) ([]Issue, error) { panic("nope") }
func (p *panickingBacklog) Update(context.Context, string, UpdateIssueInput) (Issue, error) {
	panic("nope")
}
func (p *panickingBacklog) Close(context.Context, string, string) error { panic("nope") }
func (p *panickingBacklog) Reopen(context.Context, string) error        { panic("nope") }
func (p *panickingBacklog) AddComment(context.Context, string, string) (Comment, error) {
	panic("nope")
}
func (p *panickingBacklog) ListComments(context.Context, string) ([]Comment, error) {
	panic("nope")
}
func (p *panickingBacklog) Link(context.Context, string, string, LinkKind) error   { panic("nope") }
func (p *panickingBacklog) Unlink(context.Context, string, string, LinkKind) error { panic("nope") }
func (p *panickingBacklog) Ready(context.Context, ReadyFilter) ([]Issue, error)    { panic("nope") }
func (p *panickingBacklog) Blocked(context.Context) ([]Issue, error)               { panic("nope") }
func (p *panickingBacklog) Prime(context.Context, PrimeOptions) (string, error)    { panic("nope") }

type slowBacklog struct {
	name  string
	delay time.Duration
}

func (s *slowBacklog) Name() string                { return s.name }
func (s *slowBacklog) Capabilities() CapabilitySet { return NewCapabilitySet() }
func (s *slowBacklog) Create(_ context.Context, _ CreateIssueInput) (Issue, error) {
	time.Sleep(s.delay)
	return Issue{}, nil
}
func (s *slowBacklog) Show(context.Context, string) (Issue, error)       { return Issue{}, nil }
func (s *slowBacklog) List(context.Context, ListFilter) ([]Issue, error) { return nil, nil }
func (s *slowBacklog) Update(context.Context, string, UpdateIssueInput) (Issue, error) {
	return Issue{}, nil
}
func (s *slowBacklog) Close(context.Context, string, string) error { return nil }
func (s *slowBacklog) Reopen(context.Context, string) error        { return nil }
func (s *slowBacklog) AddComment(context.Context, string, string) (Comment, error) {
	return Comment{}, nil
}
func (s *slowBacklog) ListComments(context.Context, string) ([]Comment, error) { return nil, nil }
func (s *slowBacklog) Link(context.Context, string, string, LinkKind) error    { return nil }
func (s *slowBacklog) Unlink(context.Context, string, string, LinkKind) error  { return nil }
func (s *slowBacklog) Ready(context.Context, ReadyFilter) ([]Issue, error)     { return nil, nil }
func (s *slowBacklog) Blocked(context.Context) ([]Issue, error)                { return nil, nil }
func (s *slowBacklog) Prime(context.Context, PrimeOptions) (string, error)     { return "", nil }

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
