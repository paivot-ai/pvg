package settings

import "testing"

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
