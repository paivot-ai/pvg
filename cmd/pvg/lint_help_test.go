package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestLintHelpListsAcceptanceCriteria(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	runErr := runLint([]string{"--help"})

	if err := w.Close(); err != nil {
		t.Fatalf("close help pipe: %v", err)
	}
	os.Stderr = oldStderr
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read help pipe: %v", err)
	}
	if runErr != nil {
		t.Fatalf("runLint(--help): %v", runErr)
	}
	help := string(got)
	if !strings.Contains(help, "acceptance-criteria") {
		t.Fatalf("lint help omits acceptance-criteria:\n%s", help)
	}
	external := strings.Index(help, "external-integration")
	acceptance := strings.Index(help, "acceptance-criteria")
	atomicity := strings.Index(help, "atomicity")
	if external < 0 || acceptance < 0 || atomicity < 0 || !(external < acceptance && acceptance < atomicity) {
		t.Fatalf("lint help does not list acceptance-criteria between external-integration and atomicity:\n%s", help)
	}
}
