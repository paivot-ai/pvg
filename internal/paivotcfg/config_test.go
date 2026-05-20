package paivotcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := Defaults()
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoad_ParsesFullConfig(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, `
backlog:
  primary:
    adapter: nd
    config:
      vault: .vault
  mirrors:
    - adapter: linear
      config:
        team: ENG
        api_key_env: LINEAR_API_KEY
notes:
  primary:
    adapter: vlt
    config:
      vault: Claude
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Backlog.Primary.Adapter != "nd" {
		t.Errorf("backlog primary = %q, want nd", cfg.Backlog.Primary.Adapter)
	}
	if got := cfg.Backlog.Primary.Config["vault"]; got != ".vault" {
		t.Errorf("backlog primary vault = %v, want .vault", got)
	}
	if len(cfg.Backlog.Mirrors) != 1 || cfg.Backlog.Mirrors[0].Adapter != "linear" {
		t.Errorf("expected one linear mirror, got %+v", cfg.Backlog.Mirrors)
	}
	if cfg.Notes.Primary.Adapter != "vlt" {
		t.Errorf("notes primary = %q, want vlt", cfg.Notes.Primary.Adapter)
	}
}

func TestLoad_PartialConfigFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, `
backlog:
  primary:
    adapter: linear
    config:
      team: ENG
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Backlog.Primary.Adapter != "linear" {
		t.Errorf("backlog primary = %q, want linear", cfg.Backlog.Primary.Adapter)
	}
	// notes section was missing -- should fall back to defaults
	if cfg.Notes.Primary.Adapter != DefaultNotesAdapter {
		t.Errorf("notes primary = %q, want default %q", cfg.Notes.Primary.Adapter, DefaultNotesAdapter)
	}
}

func TestLoad_EnvInterpolation(t *testing.T) {
	t.Setenv("LINEAR_TEAM", "ENG-42")
	t.Setenv("LINEAR_TOKEN", "tok-abc")

	dir := t.TempDir()
	mustWriteConfig(t, dir, `
backlog:
  primary:
    adapter: linear
    config:
      team: ${LINEAR_TEAM}
      token: $LINEAR_TOKEN
      literal: not-a-var
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got := cfg.Backlog.Primary.Config
	if got["team"] != "ENG-42" {
		t.Errorf("team = %v, want ENG-42", got["team"])
	}
	if got["token"] != "tok-abc" {
		t.Errorf("token = %v, want tok-abc", got["token"])
	}
	if got["literal"] != "not-a-var" {
		t.Errorf("literal = %v, want not-a-var", got["literal"])
	}
}

func TestLoad_EmptyPrimaryAdapterFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, `
backlog:
  primary:
    adapter: ""
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Backlog.Primary.Adapter != DefaultBacklogAdapter {
		t.Errorf("backlog primary = %q, want default %q", cfg.Backlog.Primary.Adapter, DefaultBacklogAdapter)
	}
}

func TestLoad_RejectsMirrorWithoutAdapter(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, `
backlog:
  primary:
    adapter: nd
  mirrors:
    - config:
        team: ENG
`)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected validation error for mirror without adapter")
	}
}

func TestLoad_RejectsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "this is: not: valid: yaml: at: all: [")

	if _, err := Load(dir); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLocateProjectRoot_FindsPaivotDir(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ConfigDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got, err := LocateProjectRoot(nested)
	if err != nil {
		t.Fatalf("LocateProjectRoot() error: %v", err)
	}
	wantAbs, _ := filepath.Abs(root)
	if got != wantAbs {
		t.Errorf("LocateProjectRoot() = %q, want %q", got, wantAbs)
	}
}

func TestLocateProjectRoot_FindsGitDir(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := LocateProjectRoot(root)
	if err != nil {
		t.Fatalf("LocateProjectRoot() error: %v", err)
	}
	wantAbs, _ := filepath.Abs(root)
	if got != wantAbs {
		t.Errorf("LocateProjectRoot() = %q, want %q", got, wantAbs)
	}
}

func TestDefaults_ShapeIsStable(t *testing.T) {
	d := Defaults()
	if d.Backlog.Primary.Adapter != "nd" {
		t.Errorf("default backlog adapter = %q, want nd", d.Backlog.Primary.Adapter)
	}
	if d.Notes.Primary.Adapter != "vlt" {
		t.Errorf("default notes adapter = %q, want vlt", d.Notes.Primary.Adapter)
	}
	if len(d.Backlog.Mirrors) != 0 || len(d.Notes.Mirrors) != 0 {
		t.Errorf("defaults must have no mirrors")
	}
}

func mustWriteConfig(t *testing.T, projectRoot, body string) {
	t.Helper()
	dir := filepath.Join(projectRoot, ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
