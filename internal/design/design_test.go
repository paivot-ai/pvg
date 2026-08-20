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

// MachinerySetting resolves the effective value callers must feed Applies:
// unset means the default (off), so artifact presence alone never enables
// the substrate; explicit values pass through untouched.
func TestMachinerySetting(t *testing.T) {
	cases := []struct {
		sett map[string]string
		want string
	}{
		{nil, "off"},
		{map[string]string{}, "off"},
		{map[string]string{"design.machinery": ""}, "off"},
		{map[string]string{"design.machinery": "  "}, "off"},
		{map[string]string{"design.machinery": "on"}, "on"},
		{map[string]string{"design.machinery": "auto"}, "auto"},
		{map[string]string{"design.machinery": "off"}, "off"},
	}
	for _, c := range cases {
		if got := MachinerySetting(c.sett); got != c.want {
			t.Errorf("MachinerySetting(%v) = %q, want %q", c.sett, got, c.want)
		}
	}

	// The resolved default plus Applies: a machinery-managed repo with no
	// explicit setting must NOT have the substrate apply.
	managed := t.TempDir()
	write(t, filepath.Join(managed, "design", "domain.modelith.yaml"), "model: {}\n")
	if _, applies, reason := Applies(managed, MachinerySetting(nil)); applies {
		t.Fatalf("default must be off despite artifacts, got applies=true (%s)", reason)
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
	if len(ids) != 2 || ids["DEAL-eb0c40"] != "machines/Deal.oracle.md" {
		t.Fatalf("got %v", ids)
	}
}

const policyFixture = `# Generated authorization oracle: policy

Generated from domain.modelith.yaml + policy.relational.yaml by machinery alloy. DO NOT EDIT BY HAND.

## Decisions

| test id | stable id | verb | role | owner case | target | expectation | invariants |
|---|---|---|---|---|---|---|---|
| O-AUTHZ-01 | AUTHZ-a0788c | create | platform_admin | - | - | allow | rbac-view-only-read |
| O-AUTHZ-02 | AUTHZ-805492 | create | tenant_admin | - | - | allow | rbac-view-only-read |
`

const isolationFixture = `# Generated tenant-scoping oracle: isolation

## Decisions

| test id | stable id | reference | tenant case | expectation | invariants |
|---|---|---|---|---|---|
| O-TENANT-01 | TENANT-3d8e8e | ComplianceTask.person -> Person | same-tenant | allow | task-assignee-same-tenant |
`

// TestStableIDsIncludeFormalOracles: the formal decision tables under
// design/formal/ carry a "stable id" column exactly like the transition
// oracles, and machinery's Gt covers them, so pvg must key on them too.
func TestStableIDsIncludeFormalOracles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	write(t, filepath.Join(root, "design", "machines", "Deal.oracle.md"), oracleFixture)
	write(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), policyFixture)
	write(t, filepath.Join(root, "design", "formal", "Isolation.oracle.md"), isolationFixture)
	// a non-oracle formal artifact must not be picked up
	write(t, filepath.Join(root, "design", "formal", "Policy.als"), "sig Policy {}\n")
	cfg, _ := Load(root)

	files, err := OracleFiles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 oracle files (1 machine + 2 formal), got %v", files)
	}

	ids, err := StableIDs(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DEAL-eb0c40":   "machines/Deal.oracle.md",
		"DEAL-38ba11":   "machines/Deal.oracle.md",
		"AUTHZ-a0788c":  "formal/Policy.oracle.md",
		"AUTHZ-805492":  "formal/Policy.oracle.md",
		"TENANT-3d8e8e": "formal/Isolation.oracle.md",
	}
	if len(ids) != len(want) {
		t.Fatalf("got %v", ids)
	}
	for id, rel := range want {
		if ids[id] != rel {
			t.Errorf("%s: got %q, want %q", id, ids[id], rel)
		}
	}

	oracles, err := LoadOracles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string]Oracle{}
	for _, o := range oracles {
		byRel[o.Rel] = o
	}
	if o := byRel["formal/Policy.oracle.md"]; !o.Formal() || o.Kind != "authorization" || o.Name != "policy" {
		t.Fatalf("Policy identity: %+v", o)
	}
	if o := byRel["formal/Isolation.oracle.md"]; o.Kind != "tenant-scoping" || o.Name != "isolation" {
		t.Fatalf("Isolation identity: %+v", o)
	}
	if o := byRel["machines/Deal.oracle.md"]; o.Formal() || o.Name != "Deal" || o.Kind != "" {
		t.Fatalf("Deal (no heading) identity: %+v", o)
	}
}

func TestOracleSelectorsAndProseNaming(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "design", "domain.modelith.yaml"), "model: {}\n")
	write(t, filepath.Join(root, "design", "machines", "ErasureRequest.oracle.md"),
		"# Generated transition oracle: `erasureRequest`\n\n"+oracleFixture)
	write(t, filepath.Join(root, "design", "formal", "Policy.oracle.md"), policyFixture)
	write(t, filepath.Join(root, "design", "formal", "Isolation.oracle.md"), isolationFixture)
	cfg, _ := Load(root)
	oracles, err := LoadOracles(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	find := func(rel string) Oracle {
		for _, o := range oracles {
			if o.Rel == rel {
				return o
			}
		}
		t.Fatalf("missing %s", rel)
		return Oracle{}
	}
	erasure := find("machines/ErasureRequest.oracle.md")
	policy := find("formal/Policy.oracle.md")
	isolation := find("formal/Isolation.oracle.md")

	for _, sel := range []string{"erasureRequest", "ErasureRequest", "ErasureRequest.oracle.md", "machines/ErasureRequest.oracle.md", "machines/ErasureRequest"} {
		if !erasure.MatchesSelector(sel) {
			t.Errorf("selector %q must match the ErasureRequest oracle", sel)
		}
	}
	if erasure.MatchesSelector("Erasure") || erasure.MatchesSelector("") {
		t.Error("partial or empty selectors must not match")
	}
	if !policy.MatchesSelector("policy") || !policy.MatchesSelector("formal/Policy") {
		t.Error("formal selectors must resolve by name and path")
	}

	block := "ErasureRequest as the first lifecycle machine, conformant to its transition oracle; " +
		"DoD: P-authz-oracle and T-isolation-oracle green over every row of their committed oracle files."
	if !erasure.NamedIn(block) {
		t.Error("a transition oracle is named by its machine name as a whole token")
	}
	if !policy.NamedIn(block) {
		t.Error("P-authz-oracle names the Policy oracle through the authz kind alias")
	}
	if !policy.NamedIn("rows of Policy.oracle.md") || !policy.NamedIn("the authorization oracle is green") {
		t.Error("the file name and the kind + oracle phrase both name the Policy oracle")
	}
	if !isolation.NamedIn(block) {
		t.Error("T-isolation-oracle names the Isolation oracle")
	}
	prose := "the Ash policy decision core and tenant isolation posture, plus the ErasureRequests table"
	if policy.NamedIn(prose) || isolation.NamedIn(prose) {
		t.Error("bare prose words (policy, isolation) must not select formal oracles")
	}
	if erasure.NamedIn(prose) {
		t.Error("ErasureRequests is not the whole token ErasureRequest")
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
