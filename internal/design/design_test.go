package design

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const oracleFixture = `# Generated transition oracle

## State entry / exit actions

| state | kind | entry | exit |
|---|---|---|---|
| Lead | atomic | - | - |

## Transitions

| test id | stable id | source | trigger | guard | target | actions |
|---|---|---|---|---|---|---|
| T-DEAL-01 | DEAL-eb0c40 | Lead | on:advanceStage | guardCanAdvance | persisting | setPendingAdvance |
| T-DEAL-02 | DEAL-38ba11 | Lead | on:advanceStage | - | (internal) | recordAdvanceDenied |
`

func TestLoadDetection(t *testing.T) {
	t.Run("unmanaged", func(t *testing.T) {
		if _, ok := Load(t.TempDir()); ok {
			t.Fatal("bare dir must not be managed")
		}
	})
	t.Run("conventional design dir", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
		cfg, ok := Load(root)
		if !ok || cfg.Dir != "design" {
			t.Fatalf("got %+v ok=%v", cfg, ok)
		}
	})
	t.Run("explicit config", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, ConfigName), `{"design":"blueprint","gates":"g2,g4","impl":"."}`)
		cfg, ok := Load(root)
		if !ok || cfg.Dir != "blueprint" || cfg.Gates != "g2,g4" || cfg.Impl != "." {
			t.Fatalf("got %+v ok=%v", cfg, ok)
		}
	})
	t.Run("hooks false opts out", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, ConfigName), `{"hooks": false}`)
		if _, ok := Load(root); ok {
			t.Fatal("hooks:false must disable the substrate")
		}
	})
	t.Run("corrupt config stays managed with defaults", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, ConfigName), `{not json`)
		cfg, ok := Load(root)
		if !ok || cfg.Dir != "design" {
			t.Fatalf("a typo degrades loudly at check time, never to off: %+v ok=%v", cfg, ok)
		}
	})
}

func TestApplies(t *testing.T) {
	managed := t.TempDir()
	write(t, filepath.Join(managed, "design", "domain.modelith.yaml"), "model: {}\n")
	unmanaged := t.TempDir()

	cases := []struct {
		root    string
		setting string
		want    bool
	}{
		{managed, "off", false},
		{unmanaged, "on", true}, // on promises: the check will fail loudly
		{managed, "auto", true},
		{managed, "", true},
		{unmanaged, "auto", false},
		{unmanaged, "", false},
	}
	for _, c := range cases {
		if _, got, _ := Applies(c.root, c.setting); got != c.want {
			t.Errorf("Applies(setting=%q managed=%v) = %v, want %v", c.setting, c.root == managed, got, c.want)
		}
	}
}

func TestOracleRowsParsesStableIDColumn(t *testing.T) {
	rows := OracleRows(oracleFixture)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %v", len(rows), rows)
	}
	for _, id := range []string{"DEAL-eb0c40", "DEAL-38ba11"} {
		if rows[id] == "" {
			t.Fatalf("missing id %s", id)
		}
	}
	// the state table above the transitions table carries no stable id column
	if _, ok := rows["Lead"]; ok {
		t.Fatal("state-table rows must not be picked up")
	}
}

func TestStableIDs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	write(t, filepath.Join(root, "design", "machines", "Deal.oracle.md"), oracleFixture)
	cfg, _ := Load(root)
	ids, err := StableIDs(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids["DEAL-eb0c40"] != "Deal.oracle.md" {
		t.Fatalf("got %v", ids)
	}
}

func TestTokenIn(t *testing.T) {
	cases := []struct {
		token, text string
		want        bool
	}{
		{"DEAL-eb0c40", "test keys on DEAL-eb0c40 here", true},
		{"DEAL-eb0c40", "prefix-DEAL-eb0c40", false},
		{"DEAL-eb0c40", "DEAL-eb0c40x", false},
		{"DEAL-eb0c40", "(DEAL-eb0c40)", true},
		{"DEAL-eb0c40", "no match", false},
		{"DEAL-eb0c40", "xDEAL-eb0c40 then DEAL-eb0c40", true},
	}
	for _, c := range cases {
		if got := TokenIn(c.token, c.text); got != c.want {
			t.Errorf("TokenIn(%q, %q) = %v, want %v", c.token, c.text, got, c.want)
		}
	}
}

func TestRunCheck(t *testing.T) {
	origExec, origLook := execCommand, lookPath
	defer func() { execCommand, lookPath = origExec, origLook }()

	cfg := Config{Dir: "design", Gates: "g2,g4", Impl: "."}

	t.Run("missing binary fails, never skips", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		res := RunCheck(t.TempDir(), cfg)
		if res.Passed {
			t.Fatal("a declared design promise must not pass without the tool")
		}
	})
	t.Run("green check passes with full args", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "/bin/machinery", nil }
		var gotArgs []string
		execCommand = func(name string, args ...string) *exec.Cmd {
			gotArgs = args
			return exec.Command("true")
		}
		res := RunCheck(t.TempDir(), cfg)
		if !res.Passed {
			t.Fatalf("expected pass, got %+v", res)
		}
		want := "check design --gate g2,g4 --impl ."
		if got := "check " + gotArgs[1] + " " + gotArgs[2] + " " + gotArgs[3] + " " + gotArgs[4] + " " + gotArgs[5]; got != want {
			t.Fatalf("args = %v", gotArgs)
		}
	})
	t.Run("red check fails and carries output", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "/bin/machinery", nil }
		execCommand = func(string, ...string) *exec.Cmd { return exec.Command("false") }
		if res := RunCheck(t.TempDir(), cfg); res.Passed {
			t.Fatal("expected fail")
		}
	})
}
