package gates

import "testing"

func TestParseExcludes(t *testing.T) {
	got := parseExcludes("vendor/, ,node_modules/,*.pb.go,")
	want := []string{"vendor/", "node_modules/", "*.pb.go"}
	if len(got) != len(want) {
		t.Fatalf("parseExcludes: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseExcludes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsExcluded(t *testing.T) {
	patterns := parseExcludes("vendor/,node_modules/,*.generated.*,*.pb.go,migrations/,*.min.*,dist/")

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"top-level vendor dir", "vendor/foo/bar.go", true},
		{"nested vendor dir", "pkg/vendor/x.go", true},
		{"node_modules nested", "a/node_modules/lib/index.js", true},
		{"pb.go basename glob", "api/service.pb.go", true},
		{"generated middle glob", "x/types.generated.ts", true},
		{"min basename glob", "static/app.min.js", true},
		{"migrations substring", "db/migrations/0001_init.sql", true},
		{"dist dir", "dist/bundle.js", true},
		{"normal go file", "internal/gates/gates.go", false},
		{"normal py file", "src/app.py", false},
		{"vendor as substring not dir", "myvendorlib.go", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExcluded(tc.path, patterns); got != tc.want {
				t.Errorf("isExcluded(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestApplyExcludes(t *testing.T) {
	patterns := parseExcludes("vendor/,*.pb.go")
	in := []string{"a.go", "vendor/b.go", "c.pb.go", "d.go"}
	got := applyExcludes(in, patterns)
	want := []string{"a.go", "d.go"}
	if len(got) != len(want) {
		t.Fatalf("applyExcludes: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("applyExcludes[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// No patterns => identity.
	if got := applyExcludes(in, nil); len(got) != len(in) {
		t.Errorf("applyExcludes with no patterns should be identity")
	}
}
