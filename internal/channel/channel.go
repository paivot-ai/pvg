// Package channel fetches and caches the Paivot channel manifest.
//
// The manifest is published by CI in the paivot-graph repo at
// channel/stable.json and pins the versions of every distributed artifact:
// tool binaries (pvg, nd, vlt), Claude plugins (paivot-graph, nd), and
// skills (vlt-skill). Fetching at a git ref other than "main" enables
// rollback to any previously published pin set.
package channel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	// manifestRepo is the GitHub repo that publishes the channel manifest.
	manifestRepo = "paivot-ai/paivot-graph"
	// manifestPath is the manifest path inside manifestRepo.
	manifestPath = "channel/stable.json"
	// DefaultRef is the git ref used when none is given.
	DefaultRef = "main"

	fetchTimeout    = 10 * time.Second
	maxManifestSize = 1 << 20
)

// Overridable for tests.
var (
	rawBase    = "https://raw.githubusercontent.com"
	httpClient = &http.Client{Timeout: fetchTimeout}
)

// Pin pins one tool binary or skill to a release version in a GitHub repo.
type Pin struct {
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

// PluginPin pins one Claude plugin to a marketplace repo and version.
type PluginPin struct {
	Marketplace string `json:"marketplace"`
	Version     string `json:"version"`
}

// Manifest is the parsed channel manifest. Unknown fields are ignored.
type Manifest struct {
	Schema  int                  `json:"schema"`
	Channel string               `json:"channel"`
	Updated string               `json:"updated"`
	Tools   map[string]Pin       `json:"tools"`
	Plugins map[string]PluginPin `json:"plugins"`
	Skills  map[string]Pin       `json:"skills"`
}

// URL returns the raw GitHub URL of the manifest at the given git ref.
// An empty ref means DefaultRef ("main").
func URL(ref string) string {
	if ref == "" {
		ref = DefaultRef
	}
	return fmt.Sprintf("%s/%s/%s/%s", rawBase, manifestRepo, ref, manifestPath)
}

// Fetch downloads and validates the channel manifest at the given git ref.
// An empty ref means "main". A GITHUB_TOKEN environment variable, when set,
// is sent as a bearer token (needed for private repos and rate limits).
func Fetch(ref string) (Manifest, error) {
	m, _, err := fetchRaw(httpClient, ref)
	return m, err
}

// FetchRaw is Fetch but also returns the raw manifest bytes so callers can
// refresh the nudge cache (SaveCache) after a fresh fetch.
func FetchRaw(ref string) (Manifest, []byte, error) {
	return fetchRaw(httpClient, ref)
}

func fetchRaw(client *http.Client, ref string) (Manifest, []byte, error) {
	url := URL(ref)
	req, err := http.NewRequest(http.MethodGet, url, nil) // #nosec G107 -- URL built from package constants plus a git ref
	if err != nil {
		return Manifest{}, nil, err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("fetch channel manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, nil, fmt.Errorf("fetch channel manifest: HTTP %d from %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("read channel manifest: %w", err)
	}
	m, err := Parse(data)
	if err != nil {
		return Manifest{}, nil, err
	}
	return m, data, nil
}

// Parse decodes and validates raw manifest JSON.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse channel manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks the manifest schema and required pin fields.
func (m Manifest) Validate() error {
	if m.Schema != 1 {
		return fmt.Errorf("unsupported channel manifest schema %d (want 1)", m.Schema)
	}
	if len(m.Tools) == 0 {
		return errors.New("channel manifest has no tools")
	}
	for name, pin := range m.Tools {
		if pin.Repo == "" || pin.Version == "" {
			return fmt.Errorf("tools.%s: repo and version are required", name)
		}
	}
	for name, pin := range m.Plugins {
		if pin.Marketplace == "" || pin.Version == "" {
			return fmt.Errorf("plugins.%s: marketplace and version are required", name)
		}
	}
	for name, pin := range m.Skills {
		if pin.Repo == "" || pin.Version == "" {
			return fmt.Errorf("skills.%s: repo and version are required", name)
		}
	}
	return nil
}
