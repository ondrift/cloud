package file

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codeLine is the line a coded failure ends with.
func codeLine(code string) string {
	return "  " + code + " · drift doctor explain " + code
}

// A machine that has never fetched the Driftfile format cannot lint, and says so
// with the code that explains it. HOME is empty and the platform serves nothing.
func TestLint_NoSchemaOnTheMachineIsRefusedWithItsCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := lintFixture(t, "GetPing")
	platform := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(platform.Close)

	_, err := runLint(t, platform.URL, path)
	if err == nil {
		t.Fatal("lint with no schema on the machine passed")
	}
	if !strings.Contains(err.Error(), "no copy of the Driftfile format") {
		t.Fatalf("the refusal changed its words:\n%v", err)
	}
	if n := strings.Count(err.Error(), codeLine("DRIFT-1008")); n != 1 {
		t.Errorf("lint prints DRIFT-1008's line %d times, want once:\n%v", n, err)
	}
}

// A Driftfile that does not compile is printed as the file's name, a blank line,
// every error, and one code line after them. The wrapper that adds the name does
// not add a second code.
func TestLint_AnInvalidDriftfilePrintsItsNameItsErrorsAndOneCode(t *testing.T) {
	t.Skip("DRIFT-1009 is printed by no source file yet")
	withServedSchema(t)
	dir := lintFixture(t, "GetPing")
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte("slice \"lintdemo\" {\n  first_unknown = 1\n  second_unknown = 2\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil {
		t.Fatal("a Driftfile with two unknown arguments linted clean")
	}
	rendered := err.Error()
	if n := strings.Count(rendered, codeLine("DRIFT-1009")); n != 1 {
		t.Fatalf("lint prints DRIFT-1009's line %d times, want once:\n%s", n, rendered)
	}

	name := strings.Index(rendered, "Driftfile\n\n")
	first := strings.Index(rendered, "first_unknown")
	second := strings.Index(rendered, "second_unknown")
	code := strings.Index(rendered, "DRIFT-1009")
	if name < 0 || first < name || second < first || code < second {
		t.Errorf("the name, the two errors and the code are not in that order:\n%s", rendered)
	}
	if !strings.HasSuffix(rendered, codeLine("DRIFT-1009")) {
		t.Errorf("the code's line is not the last line:\n%s", rendered)
	}
}
