package guard

import (
	"reflect"
	"testing"
)

func TestBashWriteTargets(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		// Reads contribute nothing.
		{"grep", `grep -n "arch" ARCHITECTURE.md`, nil},
		{"awk then grep", `awk '/pattern/' design/BUILD.md; grep -n "x" design/ARCHITECTURE.md`, nil},
		{"cat", `cat BUSINESS.md`, nil},
		{"sed read", `sed -n '1,20p' ARCHITECTURE.md`, nil},
		{"head tail", `head -5 DESIGN.md | tail -1`, nil},
		{"stderr redirect", `ls DESIGN.md 2>/dev/null`, []string{"/dev/null"}},
		{"fd duplication", `make build ARCHITECTURE.md 2>&1 | tee build.log`, []string{"build.log"}},

		// Redirects.
		{"redirect", `cat content.txt > BUSINESS.md`, []string{"BUSINESS.md"}},
		{"append", `echo x >> docs/DESIGN.md`, []string{"docs/DESIGN.md"}},
		{"quoted target", `echo x > "my dir/DESIGN.md"`, []string{"my dir/DESIGN.md"}},

		// Heredoc bodies are content, not targets.
		{
			"heredoc to other file",
			"cat > design/DECISIONS.md <<'EOF'\nSupersedes BUSINESS.md and DESIGN.md.\nEOF",
			[]string{"design/DECISIONS.md"},
		},
		{
			"heredoc to guarded file",
			"cat > ARCHITECTURE.md <<EOF\nbody\nEOF",
			[]string{"ARCHITECTURE.md"},
		},

		// Write utilities.
		{"tee", `echo x | tee ARCHITECTURE.md`, []string{"ARCHITECTURE.md"}},
		{"cp dest only", `cp ARCHITECTURE.md /tmp/backup.md`, []string{"/tmp/backup.md"}},
		{"cp into artifact", `cp /tmp/draft.md DESIGN.md`, []string{"DESIGN.md"}},
		{"mv both operands", `mv BUSINESS.md old/BUSINESS.md`, []string{"BUSINESS.md", "old/BUSINESS.md"}},
		{"rm", `rm -f DESIGN.md`, []string{"DESIGN.md"}},
		{"sed in place", `sed -i 's/a/b/' ARCHITECTURE.md`, []string{"s/a/b/", "ARCHITECTURE.md"}},
		{"perl in place", `perl -pi -e 's/a/b/' BUSINESS.md`, []string{"s/a/b/", "BUSINESS.md"}},
		{"dd", `dd if=/tmp/x of=DESIGN.md`, []string{"DESIGN.md"}},
		{"env prefix", `FOO=bar cp /tmp/x BUSINESS.md`, []string{"BUSINESS.md"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bashWriteTargets(tt.command)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("bashWriteTargets(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestStripHeredocBodies(t *testing.T) {
	command := "cat > notes.md <<'EOF'\nmentions ARCHITECTURE.md\nEOF\necho done"
	got := stripHeredocBodies(command)
	want := "cat > notes.md <<'EOF'\necho done"
	if got != want {
		t.Fatalf("stripHeredocBodies = %q, want %q", got, want)
	}
}

func TestStripHeredocBodies_HereStringUntouched(t *testing.T) {
	command := "grep x <<<ARCHITECTURE.md\necho done"
	if got := stripHeredocBodies(command); got != command {
		t.Fatalf("here-string body stripped: got %q, want %q", got, command)
	}
}
