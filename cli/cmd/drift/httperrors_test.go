package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A COMMAND THAT REPORTS AN HTTP FAILURE REPORTS IT THROUGH THE ONE RENDERER.
//
// common.APIError is the one thing that gives a failure its code line, the note
// that a 5xx is the platform's and not the caller's, a quota's numbers, a
// throttle's wait and the maintenance wording. A function that reads the status
// of an answer and writes its own sentence has none of them: it prints what the
// platform's body happened to say, or nothing, and a retention script reading its
// exit status learns less than a person reading its words.
//
// Read from the source, because a printed string is not something a command tree
// can be asked about, as errorconvention_test.go says of a handler that prints
// the error it returns. Every non-test Go file under cmd/ is parsed, and each
// function that calls one of common's Do* request functions and reads a
// StatusCode is held to two rules:
//
//   - it names common.CheckResponse, common.NewAPIError or common.APIError, which
//     is how a failure reaches the renderer;
//   - it never throws away the error common.CheckResponse returns, as in
//     `_, _ = common.CheckResponse(resp, op)`, which turns a refused request into
//     a command that exits 0 and says nothing.
//
// The second rule holds for every function, whether or not it reads a status. A
// function that is exempt from both is listed in testdata/httperrors-allowlist.txt
// with its reason, and the list can only shrink.
//
// THE UNIT IS A DECLARATION. A function declaration is one function, and so is a
// package-level variable that holds a function literal; the literals inside
// either belong to it, which is how a cobra command's RunE is read with the
// constructor that builds it.
//
// WHAT THIS CANNOT SEE, AND WHY. It sees a request made by calling common's Do*
// functions in the same declaration that reads the status. Outside it:
//
//   - A request made through a helper that hands back the response, such as
//     postSQLAdmin, and a status read in a function the response was passed to.
//     Neither calls a Do* function where it reads the status.
//   - A failure written by hand beside one that goes through the renderer. A
//     function that names common.CheckResponse passes, whatever else it returns:
//     the rule asks for the renderer to be in the function, not for every error
//     in it to come from there.
//   - Two commands built by one declaration. They are read as one function, so
//     one that renders can stand in for one that does not.
//   - Requests that do not go to the platform's API, made with an http.Client of
//     the function's own (the SDK's source, the status page, an announcements
//     feed). They have no code to carry.

// httpErrCommonPath is the import path of the package the rules are about.
const httpErrCommonPath = "github.com/ondrift/cloud/cli/common"

// httpErrAllowlistFile is the list of functions exempt from the rules, read from
// the package directory the test runs in.
const httpErrAllowlistFile = "testdata/httperrors-allowlist.txt"

// httpFunc is what the scan learns about one function.
type httpFunc struct {
	Name string // the package directory, a dot and the name: "cmd/slice.mintStepUpGrant"
	File string // from the module root, with forward slashes
	Line int    // the line the declaration starts on

	Requests    bool  // it calls one of common's Do* request functions
	ReadsStatus bool  // it reads a StatusCode
	Renders     bool  // it names common.CheckResponse, common.NewAPIError or common.APIError
	Discards    []int // the lines where it throws away common.CheckResponse's error
}

// violations returns what is wrong with the function, one sentence for each rule
// it breaks, or nothing.
func (f httpFunc) violations() []string {
	var out []string
	if f.Requests && f.ReadsStatus && !f.Renders {
		out = append(out, "reads the status of an answer from the platform and builds its own failure: it names "+
			"neither common.CheckResponse, common.NewAPIError nor common.APIError")
	}
	for _, line := range f.Discards {
		out = append(out, fmt.Sprintf("throws away the error common.CheckResponse returns, at line %d", line))
	}
	return out
}

// httpErrAdvice follows the problems of a tree, once, so that each problem can be
// one line.
const httpErrAdvice = "A failure written by hand has no code line, no note that a 5xx is the platform's and none " +
	"of a quota's numbers, and an error thrown away makes a refused request exit 0.\n" +
	"Return the error common.CheckResponse gives, or build the failure with common.NewAPIError when the body is " +
	"already read. A read that cannot fail the command, a best-effort probe, is listed in " + httpErrAllowlistFile +
	" with its reason."

// httpErrCommonAlias is the name a file gives the common package, or "" if it
// does not import it by a name a selector can use.
func httpErrCommonAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != httpErrCommonPath {
			continue
		}
		switch {
		case imp.Name == nil:
			return "common"
		case imp.Name.Name == "_" || imp.Name.Name == ".":
			return ""
		}
		return imp.Name.Name
	}
	return ""
}

// httpErrFuncName names a function declaration: a method is Type.method.
func httpErrFuncName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	typ := d.Recv.List[0].Type
	for {
		switch t := typ.(type) {
		case *ast.StarExpr:
			typ = t.X
		case *ast.IndexExpr:
			typ = t.X
		case *ast.IndexListExpr:
			typ = t.X
		case *ast.Ident:
			return t.Name + "." + d.Name.Name
		default:
			return d.Name.Name
		}
	}
}

// httpErrHoldsFunction reports whether a variable's value contains a function
// literal.
func httpErrHoldsFunction(v *ast.ValueSpec) bool {
	found := false
	for _, value := range v.Values {
		ast.Inspect(value, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				found = true
			}
			return !found
		})
	}
	return found
}

// httpErrFacts fills in what the function holds, reading everything under node.
func httpErrFacts(f *httpFunc, fset *token.FileSet, node ast.Node, alias string) {
	member := func(e ast.Expr) string {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == alias {
			return sel.Sel.Name
		}
		return ""
	}
	callsCheckResponse := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		return ok && member(call.Fun) == "CheckResponse"
	}

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			switch name := member(x.Fun); {
			case strings.HasPrefix(name, "Do"):
				f.Requests = true
			case name == "CheckResponse", name == "NewAPIError":
				f.Renders = true
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "StatusCode" {
				f.ReadsStatus = true
			}
			if member(x) == "APIError" {
				f.Renders = true
			}
		case *ast.ExprStmt:
			if callsCheckResponse(x.X) {
				f.Discards = append(f.Discards, fset.Position(x.Pos()).Line)
			}
		case *ast.AssignStmt:
			if len(x.Lhs) == 2 && len(x.Rhs) == 1 && callsCheckResponse(x.Rhs[0]) {
				if id, ok := x.Lhs[1].(*ast.Ident); ok && id.Name == "_" {
					f.Discards = append(f.Discards, fset.Position(x.Pos()).Line)
				}
			}
		}
		return true
	})
}

// httpErrFuncsIn returns the functions of one source file, in source order. pkg
// is the file's package directory and file is its path from the module root. Source
// that does not parse is an error and not an empty answer.
func httpErrFuncsIn(pkg, file, src string) ([]httpFunc, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	alias := httpErrCommonAlias(parsed)

	var funcs []httpFunc
	add := func(name string, at token.Pos, body ast.Node) {
		f := httpFunc{Name: pkg + "." + name, File: file, Line: fset.Position(at).Line}
		httpErrFacts(&f, fset, body, alias)
		funcs = append(funcs, f)
	}
	for _, decl := range parsed.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				add(httpErrFuncName(d), d.Pos(), d.Body)
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok && httpErrHoldsFunction(v) {
					add(v.Names[0].Name, v.Pos(), v)
				}
			}
		}
	}
	return funcs, nil
}

// httpErrFuncsOfTree returns the functions of every non-test Go file under cmd/ of
// the module rooted at root, in path order. A directory named testdata holds
// fixtures the go tool does not build, so it is not read.
func httpErrFuncsOfTree(root string) ([]httpFunc, error) {
	var funcs []httpFunc
	err := filepath.WalkDir(filepath.Join(root, "cmd"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p) // #nosec G304 -- this repo's own source
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		found, err := httpErrFuncsIn(path.Dir(rel), rel, string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		funcs = append(funcs, found...)
		return nil
	})
	return funcs, err
}

// httpAllowEntry is one line of the allowlist.
type httpAllowEntry struct {
	Line   int    // the line in the file, counted from 1
	Func   string // the package directory, a dot and the name, as httpFunc.Name writes it
	Reason string // why the function builds no failure; empty when the line gives none
}

// loadHTTPAllowlist reads the allowlist: one `<function>  # <reason>` per line,
// the reason being whatever follows the first #. A blank line and a line that
// starts with # are ignored, and the line numbers still count them.
func loadHTTPAllowlist(text string) []httpAllowEntry {
	var entries []httpAllowEntry
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fn, reason, _ := strings.Cut(line, "#")
		entries = append(entries, httpAllowEntry{
			Line:   i + 1,
			Func:   strings.TrimSpace(fn),
			Reason: strings.TrimSpace(reason),
		})
	}
	return entries
}

// httpErrProblems returns one problem for each rule a function breaks that the
// allowlist does not excuse, and one for each line of the allowlist that is not
// true: it gives no reason, it names no function, it names one the scan no longer
// flags, or it repeats an earlier line.
func httpErrProblems(funcs []httpFunc, allowed []httpAllowEntry) []string {
	listed := map[string]bool{}
	for _, a := range allowed {
		listed[a.Func] = true
	}

	var problems []string
	exists, flagged := map[string]bool{}, map[string]bool{}
	for _, f := range funcs {
		exists[f.Name] = true
		violations := f.violations()
		if len(violations) == 0 {
			continue
		}
		flagged[f.Name] = true
		if listed[f.Name] {
			continue
		}
		for _, v := range violations {
			problems = append(problems, fmt.Sprintf("%s:%d %s %s", f.File, f.Line, f.Name, v))
		}
	}

	first := map[string]int{}
	for _, a := range allowed {
		if a.Reason == "" {
			problems = append(problems, fmt.Sprintf("%s line %d, %q, has no reason after the #. Say why it builds "+
				"no failure, or delete the line", httpErrAllowlistFile, a.Line, a.Func))
		}
		if earlier, again := first[a.Func]; again {
			problems = append(problems, fmt.Sprintf("%s line %d, %q, repeats line %d. Delete it",
				httpErrAllowlistFile, a.Line, a.Func, earlier))
			continue
		}
		first[a.Func] = a.Line
		switch {
		case !exists[a.Func]:
			problems = append(problems, fmt.Sprintf("%s line %d, %q, names no function under cmd/. Delete the line",
				httpErrAllowlistFile, a.Line, a.Func))
		case !flagged[a.Func]:
			problems = append(problems, fmt.Sprintf("%s line %d, %q, is not needed: the scan flags nothing in it. "+
				"Delete the line", httpErrAllowlistFile, a.Line, a.Func))
		}
	}
	return problems
}

// httpErrScan runs the rules over source held in memory, each file named by its
// path from the module root, with an allowlist given as text.
func httpErrScan(t *testing.T, sources map[string]string, allowlist string) []string {
	t.Helper()
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)

	var funcs []httpFunc
	for _, name := range names {
		found, err := httpErrFuncsIn(path.Dir(name), name, sources[name])
		if err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		funcs = append(funcs, found...)
	}
	return httpErrProblems(funcs, loadHTTPAllowlist(allowlist))
}

// THE TREE. The one test of the real source: nothing under cmd/ reports an HTTP
// failure beside the renderer, and nothing on the allowlist is there for no
// reason.
func TestNoCommandReportsAnHTTPFailureBesideTheRenderer(t *testing.T) {
	root := repoRoot(t)

	funcs, err := httpErrFuncsOfTree(root)
	if err != nil {
		t.Fatalf("reading the command tree: %v", err)
	}
	requests := 0
	for _, f := range funcs {
		if f.Requests {
			requests++
		}
	}
	if requests == 0 {
		t.Fatalf("the scan read %d functions and none of them calls a request: it is not reading the command tree", len(funcs))
	}

	text, err := os.ReadFile(httpErrAllowlistFile) // #nosec G304 -- this repo's own list
	if err != nil {
		t.Fatalf("reading the allowlist: %v", err)
	}
	if problems := httpErrProblems(funcs, loadHTTPAllowlist(string(text))); len(problems) > 0 {
		t.Errorf("%d problem(s) in how the commands report an HTTP failure:\n\n  %s\n\n%s",
			len(problems), strings.Join(problems, "\n  "), httpErrAdvice)
	}
}

// removeHandBuilt reads the status of the platform's answer and writes its own
// sentence from the body, which is the shape the rule exists to find.
const removeHandBuilt = `package fixture

import (
	"fmt"
	"io"
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

func remove(id string) error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing?id="+id, nil)
	if err != nil {
		return common.TransportError("remove thing", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed: %s", body)
	}
	return nil
}
`

// removeDiscarding asks CheckResponse and throws its error away.
const removeDiscarding = `package fixture

import (
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

func remove(id string) error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing?id="+id, nil)
	if err != nil {
		return common.TransportError("remove thing", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = common.CheckResponse(resp, "remove thing")
		return nil
	}
	return nil
}
`

// The scan fails on a function that reads a status and builds its own failure,
// naming it, and the same function with an allowlist line passes.
func TestHTTPErrorScan_FailsOnAHandBuiltFailureUnlessTheFunctionIsListed(t *testing.T) {
	sources := map[string]string{"cmd/fixture/remove.go": removeHandBuilt}

	problems := httpErrScan(t, sources, "")
	if len(problems) != 1 {
		t.Fatalf("a hand-built failure gave %d problems, want 1:\n%s", len(problems), strings.Join(problems, "\n"))
	}
	for _, want := range []string{"cmd/fixture/remove.go:", "cmd/fixture.remove", "builds its own failure"} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the problem does not say %q:\n%s", want, problems[0])
		}
	}

	listed := "cmd/fixture.remove  # a probe: its failure reads as an answer the caller can act on\n"
	if problems := httpErrScan(t, sources, listed); len(problems) != 0 {
		t.Errorf("the function is on the allowlist and still fails:\n%s", strings.Join(problems, "\n"))
	}
}

// The scan fails on a CheckResponse whose error is thrown away, in each way Go
// lets that be written, even though the function names CheckResponse.
func TestHTTPErrorScan_FailsOnADiscardedCheckResponseError(t *testing.T) {
	for name, discard := range map[string]string{
		"both results to blanks":  "_, _ = common.CheckResponse(resp, \"remove thing\")",
		"the body kept":           "body, _ := common.CheckResponse(resp, \"remove thing\")\n\t\t_ = body",
		"the call as a statement": "common.CheckResponse(resp, \"remove thing\")",
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(removeDiscarding, `_, _ = common.CheckResponse(resp, "remove thing")`, discard, 1)
			if !strings.Contains(src, discard) {
				t.Fatal("the fixture does not hold the discard it is meant to")
			}
			problems := httpErrScan(t, map[string]string{"cmd/fixture/remove.go": src}, "")
			if len(problems) != 1 || !strings.Contains(problems[0], "throws away the error common.CheckResponse returns") ||
				!strings.Contains(problems[0], "cmd/fixture.remove") {
				t.Fatalf("a discarded error gave %q, want one problem naming the function and the discard", problems)
			}
		})
	}
}

// The control for the two above: each way a function reaches the renderer passes,
// so the scan is not a rule that fails every function that reads a status.
func TestHTTPErrorScan_AcceptsWhatReachesTheRenderer(t *testing.T) {
	const head = `package fixture

import (
	"net/http"

	%s "github.com/ondrift/cloud/cli/common"
)

`
	for name, tc := range map[string]struct{ alias, body string }{
		"CheckResponse returned": {"common", `func remove() error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return common.TransportError("remove thing", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "remove thing")
		return e
	}
	return nil
}`},
		"CheckResponse kept": {"common", `func remove() error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	_, err = common.CheckResponse(resp, "remove thing")
	return err
}`},
		"NewAPIError": {"common", `func remove() error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return common.NewAPIError("remove thing", resp.StatusCode, resp.Header, nil)
	}
	return nil
}`},
		"an APIError by hand": {"common", `func remove() error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return &common.APIError{Op: "remove thing", Status: resp.StatusCode}
	}
	return nil
}`},
		"the package under another name": {"drift", `func remove() error {
	resp, err := drift.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, e := drift.CheckResponse(resp, "remove thing")
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return e
}`},
		"a status read with no request made here": {"common", `func probe(resp *http.Response) bool {
	return resp.StatusCode == http.StatusOK
}`},
		"a request whose status is never read": {"common", `func fetch() error {
	resp, err := common.DoRequest(http.MethodGet, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}`},
	} {
		t.Run(name, func(t *testing.T) {
			src := fmt.Sprintf(head, tc.alias) + tc.body + "\n"
			if problems := httpErrScan(t, map[string]string{"cmd/fixture/remove.go": src}, ""); len(problems) != 0 {
				t.Errorf("a function that reaches the renderer was flagged:\n%s", strings.Join(problems, "\n"))
			}
		})
	}
}

// The unit is a declaration: a command's RunE is read with its constructor, a
// variable that holds a function is its own function, and a method is Type.method.
func TestHTTPErrorScan_NamesWhatItFlags(t *testing.T) {
	const src = `package fixture

import (
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

func newCmd() func() error {
	return func() error {
		resp, _ := common.DoRequest(http.MethodGet, "http://platform/thing", nil)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed: %d", resp.StatusCode)
		}
		return nil
	}
}

var probe = func() error {
	resp, _ := common.DoRequest(http.MethodGet, "http://platform/thing", nil)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed: %d", resp.StatusCode)
	}
	return nil
}

type client struct{}

func (c *client) remove() error {
	resp, _ := common.DoRequest(http.MethodGet, "http://platform/thing", nil)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed: %d", resp.StatusCode)
	}
	return nil
}
`
	problems := strings.Join(httpErrScan(t, map[string]string{"cmd/fixture/thing.go": src}, ""), "\n")
	for _, want := range []string{"cmd/fixture.newCmd", "cmd/fixture.probe", "cmd/fixture.client.remove"} {
		if !strings.Contains(problems, want) {
			t.Errorf("the scan does not name %s:\n%s", want, problems)
		}
	}
}

// The allowlist can only shrink: a line that is not true fails, so a function
// that was fixed is taken off it and a typo is not a quiet exemption.
func TestHTTPErrorScan_TheAllowlistCanOnlyShrink(t *testing.T) {
	const fine = `package fixture

import (
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

func remove() error {
	resp, err := common.DoRequest(http.MethodDelete, "http://platform/thing", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, e := common.CheckResponse(resp, "remove thing")
	return e
}
`
	for name, tc := range map[string]struct {
		sources   map[string]string
		allowlist string
		want      string
	}{
		"a function that is fine": {
			map[string]string{"cmd/fixture/remove.go": fine},
			"cmd/fixture.remove  # it used to build its own failure\n",
			"is not needed",
		},
		"a function that does not exist": {
			map[string]string{"cmd/fixture/remove.go": removeHandBuilt},
			"cmd/fixture.remove  # a probe\ncmd/fixture.removed  # a probe that was renamed\n",
			`"cmd/fixture.removed", names no function`,
		},
		"a line with no reason": {
			map[string]string{"cmd/fixture/remove.go": removeHandBuilt},
			"cmd/fixture.remove\n",
			"has no reason",
		},
		"a line with an empty reason": {
			map[string]string{"cmd/fixture/remove.go": removeHandBuilt},
			"cmd/fixture.remove  #   \n",
			"has no reason",
		},
		"a function listed twice": {
			map[string]string{"cmd/fixture/remove.go": removeHandBuilt},
			"cmd/fixture.remove  # a probe\ncmd/fixture.remove  # a probe again\n",
			"repeats line 1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			problems := httpErrScan(t, tc.sources, tc.allowlist)
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("the allowlist gave %q, want one problem saying %q", problems, tc.want)
			}
		})
	}
}

// Blank lines and comment lines are ignored and still counted, so a problem names
// the line a person will look at.
func TestLoadHTTPAllowlist_CountsEveryLine(t *testing.T) {
	got := loadHTTPAllowlist("# a comment\n\ncmd/a.f  # because\ncmd/b.g  # for a reason # with a hash\n")
	want := []httpAllowEntry{
		{Line: 3, Func: "cmd/a.f", Reason: "because"},
		{Line: 4, Func: "cmd/b.g", Reason: "for a reason # with a hash"},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}
