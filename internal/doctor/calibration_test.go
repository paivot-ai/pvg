package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paivot-ai/pvg/internal/settings"
)

// writeCalibrationSettings writes a .settings.yaml under a temp project root
// and returns the root.
func writeCalibrationSettings(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".vault", "knowledge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".settings.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// pinCalibrationVersion fixes the running-version and timeNow seams so
// staleness computations are deterministic, and restores them on cleanup.
func pinCalibrationVersion(t *testing.T, version string, now time.Time) {
	t.Helper()
	oldVer := settings.BuildVersion
	oldNow := timeNow
	settings.BuildVersion = version
	timeNow = func() time.Time { return now }
	t.Cleanup(func() {
		settings.BuildVersion = oldVer
		timeNow = oldNow
	})
}

func TestCheckCalibrationStaleness_NoOverridesPass(t *testing.T) {
	root := writeCalibrationSettings(t, "staleness_days: 15\nauto_capture: true\n")

	f := checkCalibrationStaleness(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
	if !strings.Contains(f.Message, "no gates.*/model.* overrides") {
		t.Errorf("unexpected message: %s", f.Message)
	}
}

func TestCheckCalibrationStaleness_EmptyModelLineNotAnOverride(t *testing.T) {
	// An empty model.* line means no override, so nothing to stamp.
	root := writeCalibrationSettings(t, "model.developer:\n")

	f := checkCalibrationStaleness(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
	if !strings.Contains(f.Message, "no gates.*/model.* overrides") {
		t.Errorf("unexpected message: %s", f.Message)
	}
}

func TestCheckCalibrationStaleness_UnstampedWarn(t *testing.T) {
	root := writeCalibrationSettings(t, "gates.file_loc.max: 250\nmodel.developer: sonnet\n")

	f := checkCalibrationStaleness(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn, got %s: %s", f.Status, f.Message)
	}
	if !strings.Contains(f.Message, "2 gates.*/model.* override(s) present but never stamped") {
		t.Errorf("unexpected message: %s", f.Message)
	}
	if f.Fixable {
		t.Error("calibration-staleness must not be fixable")
	}
}

func TestCheckCalibrationStaleness_MalformedDateWarn(t *testing.T) {
	root := writeCalibrationSettings(t, "gates.file_loc.max: 250\ncalibration.stamped: last-tuesday\ncalibration.pvg: v1.62.0\n")

	f := checkCalibrationStaleness(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn, got %s: %s", f.Status, f.Message)
	}
	if !strings.Contains(f.Message, `malformed calibration.stamped "last-tuesday"`) {
		t.Errorf("unexpected message: %s", f.Message)
	}
}

func TestCheckCalibrationStaleness_VersionMismatchWarn(t *testing.T) {
	root := writeCalibrationSettings(t, "gates.file_loc.max: 250\ncalibration.stamped: 2026-07-01\ncalibration.pvg: v1.60.0\n")
	pinCalibrationVersion(t, "v1.62.0", time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))

	f := checkCalibrationStaleness(root)
	if f.Status != StatusWarn {
		t.Fatalf("expected warn, got %s: %s", f.Status, f.Message)
	}
	want := "settings calibrated 2026-07-01 (31 day(s) ago) under pvg v1.60.0; running v1.62.0; re-review gates.*/model.* settings"
	if f.Message != want {
		t.Errorf("message = %q, want %q", f.Message, want)
	}
}

func TestCheckCalibrationStaleness_CurrentVersionPass(t *testing.T) {
	root := writeCalibrationSettings(t, "model.developer: sonnet\ncalibration.stamped: 2026-07-01\ncalibration.pvg: v1.62.0\n")
	pinCalibrationVersion(t, "v1.62.0", time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))

	f := checkCalibrationStaleness(root)
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
	want := "gates/model calibration stamped 2026-07-01 under current pvg v1.62.0"
	if f.Message != want {
		t.Errorf("message = %q, want %q", f.Message, want)
	}
}

func TestCheckCalibrationStaleness_MissingSettingsFilePass(t *testing.T) {
	// No settings file at all: built-in defaults are in effect.
	f := checkCalibrationStaleness(t.TempDir())
	if f.Status != StatusPass {
		t.Fatalf("expected pass, got %s: %s", f.Status, f.Message)
	}
}
