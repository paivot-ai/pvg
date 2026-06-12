package main

import "testing"

func TestValidateGitRef(t *testing.T) {
	valid := []string{
		"main",
		"epic/PRA-ru13",
		"v1.2.3",
		"HEAD~1",
		"main^",
		"abc123def",
		"origin/main",
	}
	for _, ref := range valid {
		if err := validateGitRef(ref); err != nil {
			t.Errorf("validateGitRef(%q) = %v, want nil", ref, err)
		}
	}

	invalid := []string{
		"--upload-pack=x",
		"-x",
		"a b",
		"a;b",
		"a$(x)",
		"",
	}
	for _, ref := range invalid {
		if err := validateGitRef(ref); err == nil {
			t.Errorf("validateGitRef(%q) = nil, want error", ref)
		}
	}
}
