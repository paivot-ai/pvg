package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsIntegrationTestPath(t *testing.T) {
	yes := []string{
		"test/integration/checkout_test.exs",
		"test/e2e/signup_test.exs",
		"test/hextropian/accounts_integration_test.exs",
		"tests/acceptance/flow_spec.rb",
		"src/__tests__/e2e/login.test.ts",
		"internal/api/e2e_test.go",
	}
	for _, p := range yes {
		if !IsIntegrationTestPath(p) {
			t.Errorf("%s must be scanned as an integration/e2e test", p)
		}
	}
	no := []string{
		"test/hextropian/accounts_test.exs", // unit test: mocks allowed
		"lib/hextropian/accounts.ex",        // production code
		"docs/integration-guide.md",         // not a test file
		"test/support/data_case.ex",         // helper, not a test
	}
	for _, p := range no {
		if IsIntegrationTestPath(p) {
			t.Errorf("%s must NOT be in the integration/e2e scan", p)
		}
	}
}

// TestCheckMocksElixirVocabulary is the G13 contract: the Elixir mocking
// vocabulary (Mox, Mimic, meck) is caught in integration and e2e tests, and
// unit tests are left alone.
func TestCheckMocksElixirVocabulary(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "test", "integration", "billing_test.exs"), `
defmodule BillingIntegrationTest do
  use Hextropian.DataCase
  import Mox

  test "charges" do
    expect(PaymentMock, :charge, fn _ -> :ok end)
  end
end
`)
	writeFile(t, filepath.Join(dir, "test", "e2e", "erasure_test.exs"), `
defmodule ErasureE2ETest do
  use HextropianWeb.ConnCase
  setup :set_mimic_global

  test "cascade" do
    Mimic.copy(Storage)
    :meck.new(Notifier)
  end
end
`)
	// A unit test with the same vocabulary: allowed, never scanned.
	writeFile(t, filepath.Join(dir, "test", "hextropian", "billing_test.exs"), `
import Mox
expect(PaymentMock, :charge, fn _ -> :ok end)
`)
	// A clean integration test.
	writeFile(t, filepath.Join(dir, "test", "integration", "tenants_test.exs"), `
defmodule TenantsIntegrationTest do
  use Hextropian.DataCase
  test "isolation" do
    assert Repo.all(Tenant) != []
  end
end
`)

	res, err := CheckMocks([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("mocks in integration/e2e tests must fail the check")
	}
	if res.FilesScanned != 3 {
		t.Errorf("expected the 3 integration/e2e files scanned (unit test excluded), got %d", res.FilesScanned)
	}
	var patterns []string
	for _, h := range res.Hits {
		patterns = append(patterns, h.Pattern)
		if strings.Contains(h.File, filepath.Join("test", "hextropian")) {
			t.Errorf("unit test was scanned: %s", h.File)
		}
	}
	joined := strings.Join(patterns, " ")
	for _, want := range []string{"Mox", "Mimic", "meck"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected an %s hit, got %v", want, patterns)
		}
	}

	out := FormatMocksText(res)
	if !strings.Contains(out, "MOCK CHECK: FAILED") || !strings.Contains(out, "billing_test.exs") {
		t.Errorf("report must name the offending file: %s", out)
	}
}

func TestCheckMocksMultiLanguageAndCleanPass(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tests", "integration", "test_api.py"), "from unittest.mock import MagicMock\n")
	writeFile(t, filepath.Join(dir, "e2e", "checkout.test.ts"), "jest.mock('./api');\n")
	writeFile(t, filepath.Join(dir, "internal", "api", "e2e_test.go"), "import \"github.com/stretchr/testify/mock\"\n")
	res, err := CheckMocks([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || len(res.Hits) != 3 {
		t.Fatalf("expected one hit per language, got %+v", res.Hits)
	}

	clean := t.TempDir()
	writeFile(t, filepath.Join(clean, "test", "integration", "real_test.exs"), "assert Repo.aggregate(Tenant, :count) == 1\n")
	res, err = CheckMocks([]string{clean})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.FilesScanned != 1 {
		t.Fatalf("a real-dependency integration test passes: %+v", res)
	}
	if !strings.Contains(FormatMocksText(res), "MOCK CHECK: PASSED") {
		t.Error("clean report")
	}
}

// TestCheckMocksEmptyScanIsNotSilentlyReassuring: zero integration tests is
// a pass by construction, so the report says so out loud.
func TestCheckMocksEmptyScanIsNotSilentlyReassuring(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "lib", "app.ex"), "defmodule App do\nend\n")
	res, err := CheckMocks([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.FilesScanned != 0 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(FormatMocksText(res), "an empty scan proves nothing") {
		t.Errorf("report must flag an empty scan: %s", FormatMocksText(res))
	}
}
