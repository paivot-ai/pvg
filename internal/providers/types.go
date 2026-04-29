// Package providers defines the abstract interfaces that pvg uses to talk to
// backlog (issue tracker) and notes (knowledge base) backends.
//
// The two top-level interfaces are BacklogAdapter and NotesAdapter. Each
// adapter implementation -- nd, vlt, linear, jira, confluence, ... -- lives in
// its own subpackage and is registered at startup via providers.RegisterBacklog
// or providers.RegisterNotes.
//
// Routing across primary + mirrors is handled by the Router type (see
// router.go). Adapters never need to know whether they are acting as a primary
// or a mirror.
package providers

import (
	"context"
	"errors"
	"time"
)

// Status is the normalized lifecycle state of an issue. Each adapter declares
// its own native status vocabulary and maps it to/from this enum so agents and
// pvg subcommands can reason about state without knowing the backend.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDeferred   Status = "deferred"
	StatusClosed     Status = "closed"
)

// LinkKind names a relationship between two issues.
type LinkKind string

const (
	// LinkBlocks: from blocks to (i.e., to is blocked by from).
	LinkBlocks LinkKind = "blocks"
	// LinkChildOf: from is a child of to (epic/parent relationship).
	LinkChildOf LinkKind = "child_of"
)

// Capability flags optional adapter features. Required ops (declared in the
// BacklogAdapter / NotesAdapter interfaces themselves) are not capability-gated.
type Capability string

const (
	CapDefer       Capability = "defer"
	CapArchive     Capability = "archive"
	CapAttachments Capability = "attachments"
	CapCycles      Capability = "cycles"
	CapDoctor      Capability = "doctor"
)

// CapabilitySet is an unordered set of supported optional capabilities.
type CapabilitySet map[Capability]struct{}

// NewCapabilitySet constructs a set from the given capabilities.
func NewCapabilitySet(caps ...Capability) CapabilitySet {
	s := make(CapabilitySet, len(caps))
	for _, c := range caps {
		s[c] = struct{}{}
	}
	return s
}

// Has reports whether c is supported.
func (s CapabilitySet) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

// Issue is the normalized representation that flows across the abstraction.
// Adapters round-trip backend-specific fields via Extras, but agents and the
// pvg CLI only consume the typed fields.
type Issue struct {
	ID        string
	Title     string
	Body      string
	Status    Status
	Labels    []string
	Parent    string   // parent epic / issue ID, "" if none
	Children  []string // child issue IDs (set on read; ignored on write)
	Blocks    []string // IDs that this issue blocks
	BlockedBy []string // IDs that block this issue
	Assignee  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Extras    map[string]interface{} // adapter-native fields, opaque to callers
}

// Comment is a normalized comment on an issue.
type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
}

// CreateIssueInput is the typed payload for BacklogAdapter.Create.
type CreateIssueInput struct {
	Title     string
	Body      string
	Labels    []string
	Parent    string
	BlockedBy []string
	Assignee  string
	Extras    map[string]interface{}
}

// UpdateIssueInput is the typed payload for BacklogAdapter.Update. Pointer
// fields distinguish "leave unchanged" (nil) from "set to zero value" (non-nil
// pointer to zero).
type UpdateIssueInput struct {
	Title       *string
	Body        *string
	Status      *Status
	AddLabels   []string
	DropLabels  []string
	SetAssignee *string
}

// ListFilter narrows BacklogAdapter.List results.
type ListFilter struct {
	Status []Status
	Labels []string
	Parent string // "" means no filter; "-" can mean "no parent" if adapter supports it
	Limit  int
}

// ReadyFilter narrows BacklogAdapter.Ready results.
type ReadyFilter struct {
	Labels []string
	Limit  int
}

// PrimeOptions shapes BacklogAdapter.Prime output (an AI context blob).
type PrimeOptions struct {
	IncludeClosed bool
	IncludeBody   bool
	MaxIssues     int
}

// BacklogAdapter is the contract every issue-tracker backend must implement.
// All required ops appear here; optional ops are gated by Capabilities() and
// declared in the OptionalBacklog* interfaces.
type BacklogAdapter interface {
	Name() string
	Capabilities() CapabilitySet

	Create(ctx context.Context, in CreateIssueInput) (Issue, error)
	Show(ctx context.Context, id string) (Issue, error)
	List(ctx context.Context, f ListFilter) ([]Issue, error)
	Update(ctx context.Context, id string, in UpdateIssueInput) (Issue, error)
	Close(ctx context.Context, id string) error
	Reopen(ctx context.Context, id string) error

	AddComment(ctx context.Context, id, body string) (Comment, error)
	ListComments(ctx context.Context, id string) ([]Comment, error)

	Link(ctx context.Context, from, to string, kind LinkKind) error
	Unlink(ctx context.Context, from, to string, kind LinkKind) error

	Ready(ctx context.Context, f ReadyFilter) ([]Issue, error)
	Blocked(ctx context.Context) ([]Issue, error)
	Prime(ctx context.Context, o PrimeOptions) (string, error)
}

// OptionalBacklogDefer is implemented by adapters that report CapDefer.
type OptionalBacklogDefer interface {
	Defer(ctx context.Context, id string, until time.Time) error
	Undefer(ctx context.Context, id string) error
}

// OptionalBacklogArchive is implemented by adapters that report CapArchive.
type OptionalBacklogArchive interface {
	Archive(ctx context.Context) (string, error) // returns a path or URL describing the archive
}

// OptionalBacklogDoctor is implemented by adapters that report CapDoctor.
type OptionalBacklogDoctor interface {
	Doctor(ctx context.Context) ([]DoctorFinding, error)
}

// DoctorFinding is one consistency issue surfaced by Doctor.
type DoctorFinding struct {
	Severity string // "error" | "warning" | "info"
	Message  string
	IssueID  string // "" if the finding is vault-wide
}

// NoteRef identifies a note. Adapters interpret Path according to their own
// conventions (vlt: relative path within the vault; confluence: page ID or
// space/title pair encoded as a path).
type NoteRef struct {
	Path string
}

// Note is the normalized representation of a single note.
type Note struct {
	Ref        NoteRef
	Title      string
	Body       string                 // markdown
	Properties map[string]interface{} // frontmatter equivalents
	UpdatedAt  time.Time
}

// SearchHit is one search result.
type SearchHit struct {
	Ref     NoteRef
	Title   string
	Snippet string
	Score   float64
}

// CreateNoteInput is the typed payload for NotesAdapter.Create.
type CreateNoteInput struct {
	Ref        NoteRef
	Title      string
	Body       string
	Properties map[string]interface{}
}

// NotesAdapter is the contract every notes/knowledge-base backend must implement.
type NotesAdapter interface {
	Name() string
	Capabilities() CapabilitySet

	Search(ctx context.Context, query string, limit int) ([]SearchHit, error)
	Create(ctx context.Context, in CreateNoteInput) (Note, error)
	Read(ctx context.Context, ref NoteRef) (Note, error)
	Append(ctx context.Context, ref NoteRef, body string) (Note, error)
	List(ctx context.Context, folder string) ([]NoteRef, error)
	GetProperty(ctx context.Context, ref NoteRef, key string) (interface{}, error)
	SetProperty(ctx context.Context, ref NoteRef, key string, value interface{}) error
}

// Common error sentinels.
var (
	ErrNotFound     = errors.New("provider: not found")
	ErrUnsupported  = errors.New("provider: capability not supported by this adapter")
	ErrConflict     = errors.New("provider: write conflict")
	ErrUnauthorized = errors.New("provider: unauthorized")
)
