package channel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Staleness cache for the session-start nudge path only. Setup and update
// always fetch fresh; they call SaveCache afterwards so the nudge stays in
// sync without its own network round-trip.

const (
	cacheManifestFile = "stable.json"
	cacheStampFile    = "fetched-at"
	// CacheMaxAge is how old the cached manifest may be before the nudge
	// path refreshes it.
	CacheMaxAge = 24 * time.Hour
	// nudgeTimeout bounds the nudge refresh so the session-start hook never
	// stalls on a slow network.
	nudgeTimeout = 1500 * time.Millisecond
)

// Overridable for tests.
var (
	userHomeDir = os.UserHomeDir
	now         = time.Now
)

// CacheDir returns the cache directory (~/.cache/paivot).
func CacheDir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "paivot"), nil
}

// SaveCache writes a freshly fetched manifest plus a fetched-at stamp.
func SaveCache(m Manifest, raw []byte) error {
	dir, err := CacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, cacheManifestFile), raw, 0o644); err != nil {
		return err
	}
	stamp := now().UTC().Format(time.RFC3339)
	return os.WriteFile(filepath.Join(dir, cacheStampFile), []byte(stamp+"\n"), 0o644)
}

// LoadCache returns the cached manifest and when it was fetched.
func LoadCache() (Manifest, time.Time, error) {
	dir, err := CacheDir()
	if err != nil {
		return Manifest{}, time.Time{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, cacheManifestFile))
	if err != nil {
		return Manifest{}, time.Time{}, err
	}
	m, err := Parse(raw)
	if err != nil {
		return Manifest{}, time.Time{}, err
	}
	stampRaw, err := os.ReadFile(filepath.Join(dir, cacheStampFile))
	if err != nil {
		return Manifest{}, time.Time{}, err
	}
	stamp, err := time.Parse(time.RFC3339, strings.TrimSpace(string(stampRaw)))
	if err != nil {
		return Manifest{}, time.Time{}, err
	}
	return m, stamp, nil
}

// ForNudge returns the channel manifest for the nudge path. When the cache
// is fresh (younger than CacheMaxAge) it is returned as-is. When stale or
// missing, a short-timeout refresh from "main" is attempted; on network
// failure the stale cached copy is returned if one exists. The bool reports
// whether a usable manifest was obtained -- the nudge silently skips on
// false, never blocking the hook.
func ForNudge() (Manifest, bool) {
	cached, fetchedAt, cacheErr := LoadCache()
	if cacheErr == nil && now().Sub(fetchedAt) < CacheMaxAge {
		return cached, true
	}

	client := &http.Client{Timeout: nudgeTimeout}
	fresh, raw, err := fetchRaw(client, "")
	if err != nil {
		if cacheErr == nil {
			return cached, true // stale but usable; never block the hook
		}
		return Manifest{}, false
	}
	_ = SaveCache(fresh, raw)
	return fresh, true
}
