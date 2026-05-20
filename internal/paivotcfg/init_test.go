package paivotcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_CreatesConfigAndGitignore(t *testing.T) {
	dir := t.TempDir()

	res, err := Init(dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !res.ConfigCreated {
		t.Errorf("expected ConfigCreated=true on fresh dir")
	}
	if !res.GitignoreCreated {
		t.Errorf("expected GitignoreCreated=true on fresh dir")
	}

	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"backlog:", "notes:", "adapter: nd", "adapter: vlt"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("default config missing %q\n%s", want, body)
		}
	}

	gi, err := os.ReadFile(res.GitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "secrets.yaml") {
		t.Errorf("gitignore missing secrets.yaml entry: %s", gi)
	}
}

func TestInit_LoadsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after Init: %v", err)
	}
	if cfg.Backlog.Primary.Adapter != "nd" {
		t.Errorf("backlog adapter = %q", cfg.Backlog.Primary.Adapter)
	}
	if cfg.Notes.Primary.Adapter != "vlt" {
		t.Errorf("notes adapter = %q", cfg.Notes.Primary.Adapter)
	}
	if len(cfg.Backlog.Mirrors) != 0 {
		t.Errorf("default config should yield zero backlog mirrors, got %v", cfg.Backlog.Mirrors)
	}
}

func TestInit_IsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := Init(dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init #1: %v", err)
	}
	if !first.ConfigCreated {
		t.Fatal("first call should create config")
	}

	if err := os.WriteFile(first.ConfigPath, []byte("custom: true\n"), 0o644); err != nil {
		t.Fatalf("mutate config: %v", err)
	}

	second, err := Init(dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init #2: %v", err)
	}
	if second.ConfigCreated {
		t.Errorf("idempotent Init must not overwrite existing config")
	}
	body, _ := os.ReadFile(second.ConfigPath)
	if string(body) != "custom: true\n" {
		t.Errorf("user edit was clobbered: %s", body)
	}
}

func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfgPath := filepath.Join(dir, ConfigDir, ConfigFile)
	if err := os.WriteFile(cfgPath, []byte("custom: true\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	res, err := Init(dir, InitOptions{Force: true})
	if err != nil {
		t.Fatalf("Init force: %v", err)
	}
	if !res.ConfigCreated {
		t.Errorf("expected ConfigCreated=true with --force")
	}
	body, _ := os.ReadFile(res.ConfigPath)
	if !strings.Contains(string(body), "adapter: nd") {
		t.Errorf("force did not restore default content: %s", body)
	}
}

func TestInit_WithLinearMirror_UncommentsMirrorBlock(t *testing.T) {
	dir := t.TempDir()
	res, err := Init(dir, InitOptions{WithLinearMirror: true})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, _ := os.ReadFile(res.ConfigPath)
	if !strings.Contains(string(body), "mirrors:\n    - adapter: linear") {
		t.Errorf("expected uncommented Linear mirror, got:\n%s", body)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Backlog.Mirrors) != 1 || cfg.Backlog.Mirrors[0].Adapter != "linear" {
		t.Errorf("expected one linear mirror, got %+v", cfg.Backlog.Mirrors)
	}
}

func TestInit_WithConfluenceMirror_UncommentsNotesMirror(t *testing.T) {
	dir := t.TempDir()
	res, err := Init(dir, InitOptions{WithConfluenceMirror: true})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, _ := os.ReadFile(res.ConfigPath)
	if !strings.Contains(string(body), "mirrors:\n    - adapter: confluence") {
		t.Errorf("expected uncommented Confluence mirror, got:\n%s", body)
	}
}

func TestInit_RejectsEmptyProjectRoot(t *testing.T) {
	if _, err := Init("", InitOptions{}); err == nil {
		t.Error("expected error on empty projectRoot")
	}
}
