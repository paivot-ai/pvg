package paivotcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteToolchain_CreatesFileWhenMissing(t *testing.T) {
	root := t.TempDir()
	tc := Toolchain{Channel: "stable", Pvg: "v1.55.0", Nd: "v0.10.20", Vlt: "v0.11.0"}

	if err := WriteToolchain(root, tc); err != nil {
		t.Fatalf("WriteToolchain() error: %v", err)
	}

	got, err := LoadToolchain(root)
	if err != nil {
		t.Fatalf("LoadToolchain() error: %v", err)
	}
	if got == nil || *got != tc {
		t.Fatalf("round-trip = %+v, want %+v", got, tc)
	}
}

func TestWriteToolchain_PreservesExistingConfigAndComments(t *testing.T) {
	root := t.TempDir()
	existing := `# Provider abstraction config -- see README.
backlog:
  primary:
    adapter: nd
    config:
      vault: .vault # the nd vault

notes:
  primary:
    adapter: vlt
    config:
      vault: Claude
`
	dir := filepath.Join(root, ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	tc := Toolchain{Channel: "stable", Pvg: "v1.55.0", Nd: "v0.10.20", Vlt: "v0.11.0"}
	if err := WriteToolchain(root, tc); err != nil {
		t.Fatalf("WriteToolchain() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{
		"# Provider abstraction config -- see README.",
		"# the nd vault",
		"toolchain:",
		"pvg: v1.55.0",
		"nd: v0.10.20",
		"vlt: v0.11.0",
		"channel: stable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config missing %q after pin write:\n%s", want, out)
		}
	}

	// The adapter config must still load and route as before.
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error after pin write: %v", err)
	}
	if cfg.Backlog.Primary.Adapter != "nd" || cfg.Notes.Primary.Adapter != "vlt" {
		t.Errorf("adapters changed by pin write: %+v", cfg)
	}
	if cfg.Toolchain == nil || cfg.Toolchain.Pvg != "v1.55.0" {
		t.Errorf("Load() did not surface toolchain: %+v", cfg.Toolchain)
	}
}

func TestWriteToolchain_ReplacesExistingPin(t *testing.T) {
	root := t.TempDir()
	if err := WriteToolchain(root, Toolchain{Channel: "stable", Pvg: "v1.54.0"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteToolchain(root, Toolchain{Channel: "stable", Pvg: "v1.55.0", Nd: "v0.10.20"}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadToolchain(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pvg != "v1.55.0" || got.Nd != "v0.10.20" {
		t.Fatalf("pin not replaced: %+v", got)
	}

	data, _ := os.ReadFile(filepath.Join(root, ConfigDir, ConfigFile))
	if strings.Count(string(data), "toolchain:") != 1 {
		t.Fatalf("duplicate toolchain blocks:\n%s", data)
	}
}

func TestLoadToolchain_MissingFileReturnsNil(t *testing.T) {
	got, err := LoadToolchain(t.TempDir())
	if err != nil {
		t.Fatalf("LoadToolchain() error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil pin, got %+v", got)
	}
}

func TestLoadToolchain_NoBlockReturnsNil(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("backlog:\n  primary:\n    adapter: nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadToolchain(root)
	if err != nil {
		t.Fatalf("LoadToolchain() error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil pin, got %+v", got)
	}
}
