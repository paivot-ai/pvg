package channel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleManifest = `{
  "schema": 1,
  "channel": "stable",
  "updated": "2026-06-11",
  "tools": {
    "pvg": {"repo": "paivot-ai/pvg", "version": "v1.55.0"},
    "nd": {"repo": "paivot-ai/nd", "version": "v0.10.20"},
    "vlt": {"repo": "paivot-ai/vlt", "version": "v0.11.0"}
  },
  "plugins": {
    "paivot-graph": {"marketplace": "paivot-ai/paivot-graph", "version": "1.55.0"},
    "nd": {"marketplace": "paivot-ai/nd", "version": "0.10.20"}
  },
  "skills": {
    "vlt-skill": {"repo": "paivot-ai/vlt", "version": "v0.11.0"}
  }
}`

func withRawBase(t *testing.T, base string) {
	t.Helper()
	old := rawBase
	rawBase = base
	t.Cleanup(func() { rawBase = old })
}

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = old })
	return home
}

func TestURL_RefDefaultsToMain(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"", "https://raw.githubusercontent.com/paivot-ai/paivot-graph/main/channel/stable.json"},
		{"main", "https://raw.githubusercontent.com/paivot-ai/paivot-graph/main/channel/stable.json"},
		{"v1.54.0", "https://raw.githubusercontent.com/paivot-ai/paivot-graph/v1.54.0/channel/stable.json"},
		{"abc1234", "https://raw.githubusercontent.com/paivot-ai/paivot-graph/abc1234/channel/stable.json"},
	}
	for _, tt := range tests {
		if got := URL(tt.ref); got != tt.want {
			t.Errorf("URL(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestFetch_ParsesManifest(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleManifest))
	}))
	defer server.Close()
	withRawBase(t, server.URL)
	t.Setenv("GITHUB_TOKEN", "tok123")

	m, err := Fetch("")
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if gotPath != "/paivot-ai/paivot-graph/main/channel/stable.json" {
		t.Errorf("fetched path = %q", gotPath)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
	if m.Channel != "stable" || m.Schema != 1 {
		t.Errorf("manifest header = %+v", m)
	}
	if m.Tools["pvg"].Version != "v1.55.0" || m.Tools["pvg"].Repo != "paivot-ai/pvg" {
		t.Errorf("tools.pvg = %+v", m.Tools["pvg"])
	}
	if m.Plugins["nd"].Marketplace != "paivot-ai/nd" || m.Plugins["nd"].Version != "0.10.20" {
		t.Errorf("plugins.nd = %+v", m.Plugins["nd"])
	}
	if m.Skills["vlt-skill"].Version != "v0.11.0" {
		t.Errorf("skills.vlt-skill = %+v", m.Skills["vlt-skill"])
	}
}

func TestFetch_AtRefBuildsRefURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(sampleManifest))
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	if _, err := Fetch("v1.50.0"); err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if gotPath != "/paivot-ai/paivot-graph/v1.50.0/channel/stable.json" {
		t.Errorf("fetched path = %q", gotPath)
	}
}

func TestFetch_HTTPErrorFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	if _, err := Fetch(""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}

func TestParse_RejectsBadSchemaAndIncompletePins(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"wrong schema", `{"schema":2,"tools":{"pvg":{"repo":"a/b","version":"v1"}}}`, "schema 2"},
		{"no tools", `{"schema":1}`, "no tools"},
		{"tool missing version", `{"schema":1,"tools":{"pvg":{"repo":"a/b"}}}`, "tools.pvg"},
		{"plugin missing marketplace", `{"schema":1,"tools":{"pvg":{"repo":"a/b","version":"v1"}},"plugins":{"nd":{"version":"1"}}}`, "plugins.nd"},
		{"skill missing repo", `{"schema":1,"tools":{"pvg":{"repo":"a/b","version":"v1"}},"skills":{"s":{"version":"v1"}}}`, "skills.s"},
		{"not json", `nope`, "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParse_IgnoresUnknownFields(t *testing.T) {
	doc := `{"schema":1,"future":"field","tools":{"pvg":{"repo":"a/b","version":"v1","extra":42}}}`
	if _, err := Parse([]byte(doc)); err != nil {
		t.Fatalf("unknown fields must be ignored, got %v", err)
	}
}

func TestSaveAndLoadCache_RoundTrip(t *testing.T) {
	home := withHome(t)

	m, err := Parse([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveCache(m, []byte(sampleManifest)); err != nil {
		t.Fatalf("SaveCache() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "paivot", "stable.json")); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}

	got, fetchedAt, err := LoadCache()
	if err != nil {
		t.Fatalf("LoadCache() error: %v", err)
	}
	if got.Tools["pvg"].Version != "v1.55.0" {
		t.Errorf("cached manifest = %+v", got.Tools)
	}
	if time.Since(fetchedAt) > time.Minute {
		t.Errorf("fetched-at stamp too old: %v", fetchedAt)
	}
}

func TestForNudge_FreshCacheSkipsNetwork(t *testing.T) {
	withHome(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(sampleManifest))
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	m, _ := Parse([]byte(sampleManifest))
	if err := SaveCache(m, []byte(sampleManifest)); err != nil {
		t.Fatal(err)
	}

	got, ok := ForNudge()
	if !ok {
		t.Fatal("ForNudge() = not ok with a fresh cache")
	}
	if got.Tools["pvg"].Version != "v1.55.0" {
		t.Errorf("manifest = %+v", got.Tools)
	}
	if requests != 0 {
		t.Errorf("fresh cache must not hit the network, got %d requests", requests)
	}
}

func TestForNudge_StaleCacheRefreshes(t *testing.T) {
	withHome(t)
	newer := strings.Replace(sampleManifest, "v1.55.0", "v1.56.0", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(newer))
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	// Seed a cache, then age it past CacheMaxAge.
	m, _ := Parse([]byte(sampleManifest))
	oldNow := now
	now = func() time.Time { return time.Now().Add(-25 * time.Hour) }
	if err := SaveCache(m, []byte(sampleManifest)); err != nil {
		t.Fatal(err)
	}
	now = oldNow
	t.Cleanup(func() { now = oldNow })

	got, ok := ForNudge()
	if !ok {
		t.Fatal("ForNudge() = not ok")
	}
	if got.Tools["pvg"].Version != "v1.56.0" {
		t.Errorf("stale cache must be refreshed, got pvg %s", got.Tools["pvg"].Version)
	}
	// The refresh must also rewrite the cache.
	cached, _, err := LoadCache()
	if err != nil || cached.Tools["pvg"].Version != "v1.56.0" {
		t.Errorf("cache not rewritten: %+v err=%v", cached.Tools, err)
	}
}

func TestForNudge_NetworkFailureFallsBackToStaleCache(t *testing.T) {
	withHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	m, _ := Parse([]byte(sampleManifest))
	oldNow := now
	now = func() time.Time { return time.Now().Add(-48 * time.Hour) }
	if err := SaveCache(m, []byte(sampleManifest)); err != nil {
		t.Fatal(err)
	}
	now = oldNow
	t.Cleanup(func() { now = oldNow })

	got, ok := ForNudge()
	if !ok {
		t.Fatal("stale cache must still be usable when refresh fails")
	}
	if got.Tools["pvg"].Version != "v1.55.0" {
		t.Errorf("manifest = %+v", got.Tools)
	}
}

func TestForNudge_NoCacheNoNetworkSkips(t *testing.T) {
	withHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	if _, ok := ForNudge(); ok {
		t.Fatal("ForNudge() must report not ok with no cache and no network")
	}
}

func TestFetch_RejectsInvalidManifestFromServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"schema": 99}`)
	}))
	defer server.Close()
	withRawBase(t, server.URL)

	if _, err := Fetch(""); err == nil {
		t.Fatal("invalid manifest must fail validation")
	}
}
