package providers

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// MirrorLogger receives structured records of mirror call attempts. Failures
// are logged but never bubble up as errors -- mirrors are best-effort. Pass
// nil to use the default logger which writes one line per failure to stderr.
type MirrorLogger interface {
	MirrorAttempt(record MirrorRecord)
}

// MirrorRecord describes one mirror invocation attempt.
type MirrorRecord struct {
	Adapter   string        // e.g., "linear"
	Operation string        // e.g., "Create"
	Duration  time.Duration
	Err       error // nil on success
}

// BacklogRouter fans backlog operations across one primary adapter and zero
// or more mirror adapters. Reads always come from the primary. Writes go to
// the primary first; on success, they fan out to mirrors best-effort and
// mirror failures are logged but never returned.
type BacklogRouter struct {
	primary BacklogAdapter
	mirrors []BacklogAdapter
	log     MirrorLogger
}

// NewBacklogRouter wires a primary and mirrors. The router itself satisfies
// BacklogAdapter, so callers (and the CLI) treat it the same as any single
// adapter.
func NewBacklogRouter(primary BacklogAdapter, mirrors []BacklogAdapter, logger MirrorLogger) *BacklogRouter {
	if primary == nil {
		panic("providers.NewBacklogRouter: nil primary")
	}
	if logger == nil {
		logger = defaultMirrorLogger
	}
	return &BacklogRouter{primary: primary, mirrors: mirrors, log: logger}
}

// Primary returns the underlying primary adapter, useful for capability
// inspection or escape hatches.
func (r *BacklogRouter) Primary() BacklogAdapter { return r.primary }

// Mirrors returns the underlying mirror adapters in declaration order.
func (r *BacklogRouter) Mirrors() []BacklogAdapter { return append([]BacklogAdapter(nil), r.mirrors...) }

func (r *BacklogRouter) Name() string                 { return r.primary.Name() }
func (r *BacklogRouter) Capabilities() CapabilitySet  { return r.primary.Capabilities() }

// --- Reads pass straight through to the primary. ---

func (r *BacklogRouter) Show(ctx context.Context, id string) (Issue, error) {
	return r.primary.Show(ctx, id)
}

func (r *BacklogRouter) List(ctx context.Context, f ListFilter) ([]Issue, error) {
	return r.primary.List(ctx, f)
}

func (r *BacklogRouter) ListComments(ctx context.Context, id string) ([]Comment, error) {
	return r.primary.ListComments(ctx, id)
}

func (r *BacklogRouter) Ready(ctx context.Context, f ReadyFilter) ([]Issue, error) {
	return r.primary.Ready(ctx, f)
}

func (r *BacklogRouter) Blocked(ctx context.Context) ([]Issue, error) {
	return r.primary.Blocked(ctx)
}

func (r *BacklogRouter) Prime(ctx context.Context, o PrimeOptions) (string, error) {
	// Prime is intentionally primary-only; AI context dumps are not mirrored.
	return r.primary.Prime(ctx, o)
}

// --- Writes hit the primary first; on success, fan out to mirrors. ---

func (r *BacklogRouter) Create(ctx context.Context, in CreateIssueInput) (Issue, error) {
	out, err := r.primary.Create(ctx, in)
	if err != nil {
		return Issue{}, err
	}
	r.fanOut(ctx, "Create", func(a BacklogAdapter) error {
		_, e := a.Create(ctx, in)
		return e
	})
	return out, nil
}

func (r *BacklogRouter) Update(ctx context.Context, id string, in UpdateIssueInput) (Issue, error) {
	out, err := r.primary.Update(ctx, id, in)
	if err != nil {
		return Issue{}, err
	}
	r.fanOut(ctx, "Update", func(a BacklogAdapter) error {
		_, e := a.Update(ctx, id, in)
		return e
	})
	return out, nil
}

func (r *BacklogRouter) Close(ctx context.Context, id string) error {
	if err := r.primary.Close(ctx, id); err != nil {
		return err
	}
	r.fanOut(ctx, "Close", func(a BacklogAdapter) error { return a.Close(ctx, id) })
	return nil
}

func (r *BacklogRouter) Reopen(ctx context.Context, id string) error {
	if err := r.primary.Reopen(ctx, id); err != nil {
		return err
	}
	r.fanOut(ctx, "Reopen", func(a BacklogAdapter) error { return a.Reopen(ctx, id) })
	return nil
}

func (r *BacklogRouter) AddComment(ctx context.Context, id, body string) (Comment, error) {
	out, err := r.primary.AddComment(ctx, id, body)
	if err != nil {
		return Comment{}, err
	}
	r.fanOut(ctx, "AddComment", func(a BacklogAdapter) error {
		_, e := a.AddComment(ctx, id, body)
		return e
	})
	return out, nil
}

func (r *BacklogRouter) Link(ctx context.Context, from, to string, kind LinkKind) error {
	if err := r.primary.Link(ctx, from, to, kind); err != nil {
		return err
	}
	r.fanOut(ctx, "Link", func(a BacklogAdapter) error { return a.Link(ctx, from, to, kind) })
	return nil
}

func (r *BacklogRouter) Unlink(ctx context.Context, from, to string, kind LinkKind) error {
	if err := r.primary.Unlink(ctx, from, to, kind); err != nil {
		return err
	}
	r.fanOut(ctx, "Unlink", func(a BacklogAdapter) error { return a.Unlink(ctx, from, to, kind) })
	return nil
}

// fanOut runs op on each mirror, recording success or failure. Mirror panics
// or errors never propagate; the primary's success is what the caller sees.
func (r *BacklogRouter) fanOut(ctx context.Context, op string, run func(BacklogAdapter) error) {
	for _, m := range r.mirrors {
		mirror := m
		start := time.Now()
		err := safeRun(func() error { return run(mirror) })
		r.log.MirrorAttempt(MirrorRecord{
			Adapter:   mirror.Name(),
			Operation: op,
			Duration:  time.Since(start),
			Err:       err,
		})
	}
}

// NotesRouter is the same composition pattern for notes adapters. Notes have
// no derived ops, so the structure is simpler.
type NotesRouter struct {
	primary NotesAdapter
	mirrors []NotesAdapter
	log     MirrorLogger
}

// NewNotesRouter wires a primary and mirrors.
func NewNotesRouter(primary NotesAdapter, mirrors []NotesAdapter, logger MirrorLogger) *NotesRouter {
	if primary == nil {
		panic("providers.NewNotesRouter: nil primary")
	}
	if logger == nil {
		logger = defaultMirrorLogger
	}
	return &NotesRouter{primary: primary, mirrors: mirrors, log: logger}
}

func (r *NotesRouter) Primary() NotesAdapter      { return r.primary }
func (r *NotesRouter) Mirrors() []NotesAdapter    { return append([]NotesAdapter(nil), r.mirrors...) }
func (r *NotesRouter) Name() string                { return r.primary.Name() }
func (r *NotesRouter) Capabilities() CapabilitySet { return r.primary.Capabilities() }

func (r *NotesRouter) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	return r.primary.Search(ctx, query, limit)
}

func (r *NotesRouter) Read(ctx context.Context, ref NoteRef) (Note, error) {
	return r.primary.Read(ctx, ref)
}

func (r *NotesRouter) List(ctx context.Context, folder string) ([]NoteRef, error) {
	return r.primary.List(ctx, folder)
}

func (r *NotesRouter) GetProperty(ctx context.Context, ref NoteRef, key string) (interface{}, error) {
	return r.primary.GetProperty(ctx, ref, key)
}

func (r *NotesRouter) Create(ctx context.Context, in CreateNoteInput) (Note, error) {
	out, err := r.primary.Create(ctx, in)
	if err != nil {
		return Note{}, err
	}
	r.fanOut(ctx, "Create", func(a NotesAdapter) error {
		_, e := a.Create(ctx, in)
		return e
	})
	return out, nil
}

func (r *NotesRouter) Append(ctx context.Context, ref NoteRef, body string) (Note, error) {
	out, err := r.primary.Append(ctx, ref, body)
	if err != nil {
		return Note{}, err
	}
	r.fanOut(ctx, "Append", func(a NotesAdapter) error {
		_, e := a.Append(ctx, ref, body)
		return e
	})
	return out, nil
}

func (r *NotesRouter) SetProperty(ctx context.Context, ref NoteRef, key string, value interface{}) error {
	if err := r.primary.SetProperty(ctx, ref, key, value); err != nil {
		return err
	}
	r.fanOut(ctx, "SetProperty", func(a NotesAdapter) error { return a.SetProperty(ctx, ref, key, value) })
	return nil
}

func (r *NotesRouter) fanOut(ctx context.Context, op string, run func(NotesAdapter) error) {
	for _, m := range r.mirrors {
		mirror := m
		start := time.Now()
		err := safeRun(func() error { return run(mirror) })
		r.log.MirrorAttempt(MirrorRecord{
			Adapter:   mirror.Name(),
			Operation: op,
			Duration:  time.Since(start),
			Err:       err,
		})
	}
}

// safeRun catches panics in a mirror call so one buggy adapter cannot crash
// the dispatcher.
func safeRun(fn func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("mirror panic: %v", rec)
		}
	}()
	return fn()
}

// stderrMirrorLogger writes one line per mirror attempt to stderr; only
// failures are emitted to keep dispatcher output quiet on the happy path.
type stderrMirrorLogger struct {
	out *log.Logger
}

func newStderrMirrorLogger(w io.Writer) *stderrMirrorLogger {
	return &stderrMirrorLogger{out: log.New(w, "[mirror] ", log.LstdFlags)}
}

func (s *stderrMirrorLogger) MirrorAttempt(rec MirrorRecord) {
	if rec.Err == nil {
		return
	}
	s.out.Printf("%s.%s failed after %s: %v", rec.Adapter, rec.Operation, rec.Duration, rec.Err)
}

var defaultMirrorLogger MirrorLogger = newStderrMirrorLogger(os.Stderr)
