package vltadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paivot-ai/pvg/internal/providers"
)

func TestRegistration(t *testing.T) {
	a, err := providers.BuildNotes("vlt", map[string]interface{}{"vault": "Claude"})
	if err != nil {
		t.Fatalf("BuildNotes: %v", err)
	}
	if a.Name() != "vlt" {
		t.Errorf("Name = %q, want vlt", a.Name())
	}
}

func TestPathToTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"patterns/My Note.md", "My Note"},
		{"My Note.md", "My Note"},
		{"a/b/c.md", "c"},
		{"", ""},
		{"deep/nested/folder/Some Title.md", "Some Title"},
	}
	for _, c := range cases {
		if got := pathToTitle(c.in); got != c.want {
			t.Errorf("pathToTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitFrontmatter_StripsBlock(t *testing.T) {
	body := "---\ntype: pattern\nproject: paivot\n---\n# My Pattern\n\ncontent here"
	title, stripped := splitFrontmatter(body)
	if title != "My Pattern" {
		t.Errorf("title = %q, want My Pattern", title)
	}
	if strings.Contains(stripped, "type: pattern") {
		t.Errorf("frontmatter not stripped: %q", stripped)
	}
	if !strings.Contains(stripped, "content here") {
		t.Errorf("body content missing: %q", stripped)
	}
}

func TestSplitFrontmatter_NoFrontmatter(t *testing.T) {
	body := "# Just a Title\n\nbody"
	title, stripped := splitFrontmatter(body)
	if title != "Just a Title" {
		t.Errorf("title = %q", title)
	}
	if stripped != body {
		t.Errorf("body should be unchanged: %q vs %q", stripped, body)
	}
}

func TestSplitFrontmatter_FirstNonEmptyAsFallback(t *testing.T) {
	body := "first line\nsecond line"
	title, _ := splitFrontmatter(body)
	if title != "first line" {
		t.Errorf("title = %q, want first line", title)
	}
}

func TestPrefixVault_AddsVaultArg(t *testing.T) {
	a := &Adapter{vault: "Claude"}
	got := a.prefixVault([]string{"search", "query=x"})
	if got[0] != "vault=Claude" {
		t.Errorf("expected vault=Claude prefix, got %v", got)
	}
}

func TestPrefixVault_NoVaultLeavesUnchanged(t *testing.T) {
	a := &Adapter{}
	got := a.prefixVault([]string{"search", "query=x"})
	if got[0] == "vault=" || strings.HasPrefix(got[0], "vault=") {
		t.Errorf("expected no vault prefix, got %v", got)
	}
}

// --- Integration: requires vlt binary and a writable scratch vault ---

func TestIntegration_CreateReadAppendProperties(t *testing.T) {
	if _, err := exec.LookPath("vlt"); err != nil {
		t.Skip("vlt binary not on PATH; skipping integration")
	}
	vaultPath := setupScratchVault(t)

	a, err := New(map[string]interface{}{"vault": vaultPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	in := providers.CreateNoteInput{
		Ref:   providers.NoteRef{Path: "scratch/Adapter Smoke.md"},
		Title: "Adapter Smoke",
		Body:  "# Adapter Smoke\n\nfirst line\n",
		Properties: map[string]interface{}{
			"type":   "pattern",
			"status": "active",
		},
	}
	created, err := a.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Title != "Adapter Smoke" {
		t.Errorf("Title = %q", created.Title)
	}
	if created.Properties["type"] != "pattern" {
		t.Errorf("property type = %v, want pattern", created.Properties["type"])
	}

	if _, err := a.Append(ctx, in.Ref, "appended line\n"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := a.Read(ctx, in.Ref)
	if err != nil {
		t.Fatalf("Read after Append: %v", err)
	}
	if !strings.Contains(got.Body, "appended line") {
		t.Errorf("appended content missing from body: %q", got.Body)
	}

	if err := a.SetProperty(ctx, in.Ref, "confidence", "high"); err != nil {
		t.Fatalf("SetProperty: %v", err)
	}
	val, err := a.GetProperty(ctx, in.Ref, "confidence")
	if err != nil {
		t.Fatalf("GetProperty: %v", err)
	}
	if val != "high" {
		t.Errorf("confidence = %v, want high", val)
	}
}

func TestIntegration_SearchReturnsHits(t *testing.T) {
	if _, err := exec.LookPath("vlt"); err != nil {
		t.Skip("vlt binary not on PATH; skipping integration")
	}
	vaultPath := setupScratchVault(t)
	a, _ := New(map[string]interface{}{"vault": vaultPath})
	ctx := context.Background()

	for i, body := range []string{"alpha keyword body", "beta filler body", "gamma keyword body"} {
		in := providers.CreateNoteInput{
			Ref:   providers.NoteRef{Path: filepath.Join("notes", "n"+itoa(i)+".md")},
			Title: "n" + itoa(i),
			Body:  "# n" + itoa(i) + "\n\n" + body + "\n",
		}
		if _, err := a.Create(ctx, in); err != nil {
			t.Fatalf("seed Create %d: %v", i, err)
		}
	}

	hits, err := a.Search(ctx, "keyword", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) < 2 {
		t.Errorf("expected >=2 hits for 'keyword', got %d: %+v", len(hits), hits)
	}
}

func TestIntegration_ListFolderFiltersByPath(t *testing.T) {
	if _, err := exec.LookPath("vlt"); err != nil {
		t.Skip("vlt binary not on PATH; skipping integration")
	}
	vaultPath := setupScratchVault(t)
	a, _ := New(map[string]interface{}{"vault": vaultPath})
	ctx := context.Background()

	in := providers.CreateNoteInput{
		Ref:   providers.NoteRef{Path: "subdir/Hello.md"},
		Title: "Hello",
		Body:  "# Hello\n\nworld\n",
	}
	if _, err := a.Create(ctx, in); err != nil {
		t.Fatalf("Create: %v", err)
	}

	refs, err := a.List(ctx, "subdir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected at least one ref under subdir")
	}
	for _, r := range refs {
		if !strings.HasPrefix(r.Path, "subdir/") {
			t.Errorf("ref %q escaped folder filter", r.Path)
		}
	}
}

// --- helpers ---

func setupScratchVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".vlt-vault"), []byte(""), 0o644); err != nil {
		t.Fatalf("marker file: %v", err)
	}
	return dir
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for n := i; n > 0; n /= 10 {
		out = string(rune('0'+n%10)) + out
	}
	return out
}
