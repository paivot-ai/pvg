package lifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paivot-ai/vlt"
)

// TestBoundedSearchReturnsResults: the ordinary path is untouched.
func TestBoundedSearchReturnsResults(t *testing.T) {
	want := []vlt.SearchResult{{Title: "Paivot", RelPath: "projects/Paivot.md"}}
	got, err := boundedSearch(time.Second, func() ([]vlt.SearchResult, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Paivot" {
		t.Fatalf("results not passed through: %+v", got)
	}
}

// TestBoundedSearchPassesErrorsThrough: a real search error still reaches the
// formatter, which degrades it, rather than being reported as a stall.
func TestBoundedSearchPassesErrorsThrough(t *testing.T) {
	_, err := boundedSearch(time.Second, func() ([]vlt.SearchResult, error) {
		return nil, errors.New("vault locked")
	})
	if err == nil || !strings.Contains(err.Error(), "vault locked") {
		t.Fatalf("search errors must pass through unchanged: %v", err)
	}
}

// TestBoundedSearchStallDegrades is the whole point: a search that never
// returns must not take the session start with it. The hook reports degraded
// mode and continues.
func TestBoundedSearchStallDegrades(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	start := time.Now()
	results, err := boundedSearch(20*time.Millisecond, func() ([]vlt.SearchResult, error) {
		<-blocked // a stalled vault walk: no error, no return
		return nil, nil
	})
	if err == nil {
		t.Fatal("a stalled search must be reported as an error, not waited on")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the deadline was not enforced: waited %s", elapsed)
	}
	if results != nil {
		t.Fatalf("a stalled search has no results: %+v", results)
	}
	if out := formatVaultSearchOutput(results, err); !strings.Contains(out, "degraded mode") {
		t.Errorf("the stall must degrade the session start, not fail it: %s", out)
	}
}
