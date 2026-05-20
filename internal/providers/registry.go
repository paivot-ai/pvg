package providers

import (
	"fmt"
	"sort"
	"sync"
)

// BacklogFactory builds a BacklogAdapter from an opaque adapter-specific config
// map (the same shape that comes out of paivotcfg.AdapterRef.Config).
type BacklogFactory func(config map[string]interface{}) (BacklogAdapter, error)

// NotesFactory builds a NotesAdapter from an opaque adapter-specific config map.
type NotesFactory func(config map[string]interface{}) (NotesAdapter, error)

var (
	registryMu       sync.RWMutex
	backlogFactories = map[string]BacklogFactory{}
	notesFactories   = map[string]NotesFactory{}
)

// RegisterBacklog registers a backlog adapter factory under name. Adapter
// packages call this from their init() functions. Re-registering the same name
// panics, since the conflict is always a programming error.
func RegisterBacklog(name string, f BacklogFactory) {
	if name == "" {
		panic("providers.RegisterBacklog: empty name")
	}
	if f == nil {
		panic("providers.RegisterBacklog: nil factory for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := backlogFactories[name]; exists {
		panic("providers.RegisterBacklog: duplicate name " + name)
	}
	backlogFactories[name] = f
}

// RegisterNotes registers a notes adapter factory under name.
func RegisterNotes(name string, f NotesFactory) {
	if name == "" {
		panic("providers.RegisterNotes: empty name")
	}
	if f == nil {
		panic("providers.RegisterNotes: nil factory for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := notesFactories[name]; exists {
		panic("providers.RegisterNotes: duplicate name " + name)
	}
	notesFactories[name] = f
}

// BuildBacklog instantiates the named backlog adapter with the given config.
// Returns ErrUnknownAdapter if the name was never registered.
func BuildBacklog(name string, config map[string]interface{}) (BacklogAdapter, error) {
	registryMu.RLock()
	f, ok := backlogFactories[name]
	registryMu.RUnlock()
	if !ok {
		return nil, &UnknownAdapterError{Kind: "backlog", Name: name, Available: backlogNames()}
	}
	return f(config)
}

// BuildNotes instantiates the named notes adapter with the given config.
func BuildNotes(name string, config map[string]interface{}) (NotesAdapter, error) {
	registryMu.RLock()
	f, ok := notesFactories[name]
	registryMu.RUnlock()
	if !ok {
		return nil, &UnknownAdapterError{Kind: "notes", Name: name, Available: notesNames()}
	}
	return f(config)
}

// RegisteredBacklogNames returns the sorted list of registered backlog names.
func RegisteredBacklogNames() []string {
	return backlogNames()
}

// RegisteredNotesNames returns the sorted list of registered notes names.
func RegisteredNotesNames() []string {
	return notesNames()
}

func backlogNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(backlogFactories))
	for k := range backlogFactories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func notesNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(notesFactories))
	for k := range notesFactories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UnknownAdapterError describes an attempt to build an adapter whose name is
// not registered. The available list helps users (and tests) recover.
type UnknownAdapterError struct {
	Kind      string // "backlog" | "notes"
	Name      string
	Available []string
}

func (e *UnknownAdapterError) Error() string {
	return fmt.Sprintf("unknown %s adapter %q (available: %v)", e.Kind, e.Name, e.Available)
}

// ResetForTesting clears all registered adapters. Tests that register adapters
// inline must call this in their setup to avoid bleeding state across packages.
func ResetForTesting() {
	registryMu.Lock()
	defer registryMu.Unlock()
	backlogFactories = map[string]BacklogFactory{}
	notesFactories = map[string]NotesFactory{}
}
