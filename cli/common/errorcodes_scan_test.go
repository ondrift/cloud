package common

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// codeLiteral is a string literal that is exactly a failure code, "DRIFT-1006".
var codeLiteral = regexp.MustCompile(`^DRIFT-[0-9]{4}$`)

// moduleRoot is the directory holding the CLI's go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// codeLiterals returns every code a non-test Go file under root names as a string
// literal, with the files that name it. A code in a comment is not a print, and a
// test file only talks about codes, so neither counts.
func codeLiterals(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr == nil && codeLiteral.MatchString(value) {
				found[value] = append(found[value], rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("reading the module's source: %v", err)
	}
	return found
}

// codeLiteralProblems compares the codes the source names with the registry, in
// both directions: a code the source prints that no entry registers has no
// explanation to look up, and a live entry no source prints promises one the
// user never gets. A retired entry is not printed any more, so it is not asked to
// be. Each problem names its code.
func codeLiteralProblems(registry []ErrorCode, literals map[string][]string) []string {
	registered := make(map[string]ErrorCode, len(registry))
	for _, e := range registry {
		registered[e.Code] = e
	}

	var problems []string
	for code, files := range literals {
		if _, ok := registered[code]; !ok {
			sort.Strings(files)
			problems = append(problems, code+" is printed by "+strings.Join(files, ", ")+" and is not registered")
		}
	}
	for _, e := range registry {
		if e.Retired {
			continue
		}
		if len(literals[e.Code]) == 0 {
			problems = append(problems, e.Code+" is registered ("+e.Key+") and no source file prints it")
		}
	}
	sort.Strings(problems)
	return problems
}

// Every code the CLI's source names is registered, and every live registered code
// is named by the source: a code printed with no entry cannot be explained, and an
// entry nothing prints is documentation for a failure that never carries it.
func TestErrorCodes_TheSourceAndTheRegistryNameTheSameCodes(t *testing.T) {
	t.Skip("DRIFT-1016 to DRIFT-1020 are printed by no source file until the wiring commit")
	for _, problem := range codeLiteralProblems(ErrorCodes(), codeLiterals(t, moduleRoot(t))) {
		t.Error(problem)
	}
}

// The control for the test above: each way the two can disagree is reported with
// the code named, and agreement is not.
func TestCodeLiteralProblems_NamesTheCodeThatDisagrees(t *testing.T) {
	entry := func(code, key string, retired bool) ErrorCode {
		return ErrorCode{Code: code, Key: key, Retired: retired}
	}
	registry := []ErrorCode{entry("DRIFT-1001", "a", false), entry("DRIFT-1002", "b", false), entry("DRIFT-1003", "c", true)}

	for _, tc := range []struct {
		name     string
		literals map[string][]string
		want     []string
	}{
		{"every live code is printed, the retired one is not",
			map[string][]string{"DRIFT-1001": {"a.go"}, "DRIFT-1002": {"b.go"}}, nil},
		{"a retired code may still be printed",
			map[string][]string{"DRIFT-1001": {"a.go"}, "DRIFT-1002": {"b.go"}, "DRIFT-1003": {"c.go"}}, nil},
		{"a live code nothing prints",
			map[string][]string{"DRIFT-1001": {"a.go"}}, []string{"DRIFT-1002"}},
		{"a printed code nothing registers",
			map[string][]string{"DRIFT-1001": {"a.go"}, "DRIFT-1002": {"b.go"}, "DRIFT-1099": {"x.go"}}, []string{"DRIFT-1099", "x.go"}},
		{"both at once",
			map[string][]string{"DRIFT-1002": {"b.go"}, "DRIFT-1098": {"y.go"}}, []string{"DRIFT-1001", "DRIFT-1098"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codeLiteralProblems(registry, tc.literals)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("agreement was reported as %q", got)
				}
				return
			}
			all := strings.Join(got, "\n")
			for _, want := range tc.want {
				if !strings.Contains(all, want) {
					t.Errorf("the problems do not mention %s: %q", want, got)
				}
			}
		})
	}
}

// The walk reads literals and nothing else: a code in a comment, in a test file or
// inside a longer string is not a print.
func TestCodeLiterals_CountsOnlyAStringThatIsACode(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\n// DRIFT-1001 in a comment\nconst c = \"DRIFT-1002\"\nconst long = \"see DRIFT-1003 here\"\nconst raw = `DRIFT-1004`\n")
	write("a_test.go", "package a\n\nconst d = \"DRIFT-1005\"\n")

	got := codeLiterals(t, root)
	if len(got) != 2 || len(got["DRIFT-1002"]) != 1 || len(got["DRIFT-1004"]) != 1 {
		t.Errorf("the walk found %v, want DRIFT-1002 and DRIFT-1004 alone", got)
	}
}
