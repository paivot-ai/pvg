package settings

import (
	"os"
	"testing"
)

// TestDefaults_MachineryFirstKeys pins the defaults of the settings a
// machinery-first delivery adds. Each one must default to OFF/empty: an
// existing project that never heard of them behaves exactly as before.
func TestDefaults_MachineryFirstKeys(t *testing.T) {
	cases := map[string]string{
		"design.machinery":         "off",
		"design.red_gates":         "",
		"verify.command":           "",
		"review.command":           "",
		"hard_tdd.preauthorized":   "false",
		"lint.paths_exist.exclude": "",
	}
	for key, want := range cases {
		got, ok := defaults[key]
		if !ok {
			t.Errorf("%s missing from defaults", key)
			continue
		}
		if got != want {
			t.Errorf("%s default = %q, want %q", key, got, want)
		}
		if Default(key) != want {
			t.Errorf("Default(%q) = %q, want %q", key, Default(key), want)
		}
	}
}

func TestHardTDDPreauthorizedValidation(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/.settings.yaml"

	if err := setSettings(dir, path, []string{"hard_tdd.preauthorized=yes"}); err == nil {
		t.Fatal("a non-boolean must be rejected")
	}
	for _, v := range []string{"true", "false"} {
		if err := setSettings(dir, path, []string{"hard_tdd.preauthorized=" + v}); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	sett := LoadFile(path)
	if sett["hard_tdd.preauthorized"] != "false" {
		t.Fatalf("round trip: %v", sett)
	}
}

// TestVerifyAndReviewCommandsRoundTrip: the project verification workflow
// (run inline by the developer) and the review workflow (run by the
// dispatcher at the epic gate) are free-form command strings.
func TestVerifyAndReviewCommandsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/.settings.yaml"

	if err := setSettings(dir, path, []string{"verify.command=/phx:verify", "review.command=/phx:review"}); err != nil {
		t.Fatal(err)
	}
	sett := LoadFile(path)
	if sett["verify.command"] != "/phx:verify" || sett["review.command"] != "/phx:review" {
		t.Fatalf("round trip: %v", sett)
	}
}

// TestHandWrittenQuotedValuesAreHonored: the settings file is plain YAML and
// a user editing it by hand writes quoted scalars. A quoted value that
// compares unequal to every expected literal would silently resolve to the
// default -- the substrate off, a boolean gate unset -- which is exactly the
// "silently ignored" failure this work exists to remove.
func TestHandWrittenQuotedValuesAreHonored(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/.settings.yaml"
	body := "design.machinery: \"on\"\nlint.brownfield: 'false'\nhard_tdd.preauthorized: \"true\"\nlint.quality_gates: @spec|Ash\\.Policy\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sett := LoadFile(path)
	want := map[string]string{
		"design.machinery":       "on",
		"lint.brownfield":        "false",
		"hard_tdd.preauthorized": "true",
		"lint.quality_gates":     `@spec|Ash\.Policy`,
	}
	for k, v := range want {
		if sett[k] != v {
			t.Errorf("%s = %q, want %q", k, sett[k], v)
		}
	}
}

func TestUnquoteValueLeavesUnmatchedQuotesAlone(t *testing.T) {
	cases := map[string]string{
		`"on"`:      "on",
		`'off'`:     "off",
		`"on`:       `"on`,
		`on"`:       `on"`,
		`"`:         `"`,
		``:          ``,
		`"a" | "b"`: `a" | "b`,
	}
	for in, want := range cases {
		if got := unquoteValue(in); got != want {
			t.Errorf("unquoteValue(%q) = %q, want %q", in, got, want)
		}
	}
}
