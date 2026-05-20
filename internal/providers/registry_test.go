package providers

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeBacklog is a minimal BacklogAdapter used only for registry-shape testing.
type fakeBacklog struct {
	name string
}

func (f *fakeBacklog) Name() string                                            { return f.name }
func (f *fakeBacklog) Capabilities() CapabilitySet                             { return NewCapabilitySet() }
func (f *fakeBacklog) Create(context.Context, CreateIssueInput) (Issue, error) { return Issue{}, nil }
func (f *fakeBacklog) Show(context.Context, string) (Issue, error)             { return Issue{}, nil }
func (f *fakeBacklog) List(context.Context, ListFilter) ([]Issue, error)       { return nil, nil }
func (f *fakeBacklog) Update(context.Context, string, UpdateIssueInput) (Issue, error) {
	return Issue{}, nil
}
func (f *fakeBacklog) Close(context.Context, string) error  { return nil }
func (f *fakeBacklog) Reopen(context.Context, string) error { return nil }
func (f *fakeBacklog) AddComment(context.Context, string, string) (Comment, error) {
	return Comment{}, nil
}
func (f *fakeBacklog) ListComments(context.Context, string) ([]Comment, error) { return nil, nil }
func (f *fakeBacklog) Link(context.Context, string, string, LinkKind) error    { return nil }
func (f *fakeBacklog) Unlink(context.Context, string, string, LinkKind) error  { return nil }
func (f *fakeBacklog) Ready(context.Context, ReadyFilter) ([]Issue, error)     { return nil, nil }
func (f *fakeBacklog) Blocked(context.Context) ([]Issue, error)                { return nil, nil }
func (f *fakeBacklog) Prime(context.Context, PrimeOptions) (string, error)     { return "", nil }

// fakeNotes is a minimal NotesAdapter used only for registry-shape testing.
type fakeNotes struct {
	name string
}

func (f *fakeNotes) Name() string                                             { return f.name }
func (f *fakeNotes) Capabilities() CapabilitySet                              { return NewCapabilitySet() }
func (f *fakeNotes) Search(context.Context, string, int) ([]SearchHit, error) { return nil, nil }
func (f *fakeNotes) Create(context.Context, CreateNoteInput) (Note, error)    { return Note{}, nil }
func (f *fakeNotes) Read(context.Context, NoteRef) (Note, error)              { return Note{}, nil }
func (f *fakeNotes) Append(context.Context, NoteRef, string) (Note, error)    { return Note{}, nil }
func (f *fakeNotes) List(context.Context, string) ([]NoteRef, error)          { return nil, nil }
func (f *fakeNotes) GetProperty(context.Context, NoteRef, string) (interface{}, error) {
	return nil, nil
}
func (f *fakeNotes) SetProperty(context.Context, NoteRef, string, interface{}) error { return nil }

func TestRegisterAndBuild_Backlog(t *testing.T) {
	ResetForTesting()

	RegisterBacklog("fake", func(_ map[string]interface{}) (BacklogAdapter, error) {
		return &fakeBacklog{name: "fake"}, nil
	})

	got, err := BuildBacklog("fake", nil)
	if err != nil {
		t.Fatalf("BuildBacklog: %v", err)
	}
	if got.Name() != "fake" {
		t.Errorf("Name() = %q, want fake", got.Name())
	}
}

func TestRegisterAndBuild_Notes(t *testing.T) {
	ResetForTesting()

	RegisterNotes("fake", func(_ map[string]interface{}) (NotesAdapter, error) {
		return &fakeNotes{name: "fake"}, nil
	})

	got, err := BuildNotes("fake", nil)
	if err != nil {
		t.Fatalf("BuildNotes: %v", err)
	}
	if got.Name() != "fake" {
		t.Errorf("Name() = %q, want fake", got.Name())
	}
}

func TestBuildBacklog_UnknownAdapter(t *testing.T) {
	ResetForTesting()

	_, err := BuildBacklog("nope", nil)
	var unknown *UnknownAdapterError
	if !errors.As(err, &unknown) {
		t.Fatalf("got %T, want *UnknownAdapterError", err)
	}
	if unknown.Kind != "backlog" || unknown.Name != "nope" {
		t.Errorf("err = %+v, want kind=backlog name=nope", unknown)
	}
}

func TestBuildNotes_UnknownAdapter(t *testing.T) {
	ResetForTesting()

	_, err := BuildNotes("nope", nil)
	var unknown *UnknownAdapterError
	if !errors.As(err, &unknown) {
		t.Fatalf("got %T, want *UnknownAdapterError", err)
	}
}

func TestRegisterBacklog_DuplicatePanics(t *testing.T) {
	ResetForTesting()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()

	RegisterBacklog("dup", func(_ map[string]interface{}) (BacklogAdapter, error) {
		return &fakeBacklog{name: "dup"}, nil
	})
	RegisterBacklog("dup", func(_ map[string]interface{}) (BacklogAdapter, error) {
		return &fakeBacklog{name: "dup"}, nil
	})
}

func TestRegisterBacklog_EmptyNamePanics(t *testing.T) {
	ResetForTesting()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty name")
		}
	}()

	RegisterBacklog("", func(_ map[string]interface{}) (BacklogAdapter, error) {
		return &fakeBacklog{}, nil
	})
}

func TestRegisterBacklog_NilFactoryPanics(t *testing.T) {
	ResetForTesting()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil factory")
		}
	}()

	RegisterBacklog("nilf", nil)
}

func TestRegisteredNamesAreSorted(t *testing.T) {
	ResetForTesting()
	for _, n := range []string{"zoo", "alpha", "mu"} {
		name := n
		RegisterBacklog(name, func(_ map[string]interface{}) (BacklogAdapter, error) {
			return &fakeBacklog{name: name}, nil
		})
	}
	got := RegisteredBacklogNames()
	want := []string{"alpha", "mu", "zoo"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RegisteredBacklogNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCapabilitySet(t *testing.T) {
	cs := NewCapabilitySet(CapDefer, CapArchive)
	if !cs.Has(CapDefer) || !cs.Has(CapArchive) {
		t.Errorf("CapabilitySet missing expected entries: %+v", cs)
	}
	if cs.Has(CapCycles) {
		t.Errorf("CapabilitySet has unexpected entry CapCycles")
	}
}

func TestStatusValues_AreStable(t *testing.T) {
	// Wire-format guard: agents and configs may pin to these literal strings.
	cases := map[Status]string{
		StatusOpen:       "open",
		StatusInProgress: "in_progress",
		StatusBlocked:    "blocked",
		StatusDeferred:   "deferred",
		StatusClosed:     "closed",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("Status %v = %q, want %q", got, string(got), want)
		}
	}
}

func TestIssueZeroTimes(t *testing.T) {
	// Sanity: zero-value timestamps mean "unknown" and never crash callers.
	var i Issue
	if !i.CreatedAt.Equal(time.Time{}) {
		t.Errorf("expected zero CreatedAt, got %v", i.CreatedAt)
	}
}
