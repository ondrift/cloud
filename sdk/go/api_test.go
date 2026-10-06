package drift

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The exported surface of package drift is pinned. Every declaration a user
// can reach, with its signature, struct fields and tags, is rendered and
// compared against testdata/drift_api.golden. A change to that file is a change
// to the SDK's public promise, and is made on purpose or not at all.
//
// Reachable means more than exported: an unexported type is part of the
// surface when an exported name leads to it (drift.Backbone is a backboneNS,
// whose Secret field is a secretNS, whose Get method is called by users), so
// its exported fields and methods are rendered too. Comments, function bodies,
// variable initialisers and layout are not part of the surface.
func TestExportedAPIIsUnchanged(t *testing.T) {
	got, err := renderAPI(".")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "drift_api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the exported API of package drift changed.\n\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// The renderer sees what the pin claims to see: an added exported function, a
// changed signature, a new field and a newly reachable method each change the
// rendering, and a comment or a body does not.
func TestRenderAPISeesTheSurface(t *testing.T) {
	base := `package p
type hidden struct{ Name string; secret int }
func (hidden) Do() error { return nil }
func (hidden) undo() {}
type unreached struct{ X int }
func (unreached) Do() {}
// V is reachable.
var V hidden
func F(a int) string { return "" }
`
	render := func(src string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := renderAPI(dir)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := "func (hidden) Do() error\nfunc F(a int) string\ntype hidden struct{Name string}\nvar V hidden\n"
	if got := render(base); got != want {
		t.Fatalf("rendering:\n%s\nwant:\n%s", got, want)
	}
	same := strings.Replace(base, `return ""`, `return "changed body"`, 1)
	same = strings.Replace(same, "// V is reachable.", "// V is reachable, reworded.\n\n", 1)
	same = strings.Replace(same, "struct{ Name string; secret int }", "struct {\n\tName string\n\n\tsecret int\n}", 1)
	if got := render(same); got != want {
		t.Fatalf("a body, comment or layout change altered the rendering:\n%s", got)
	}
	for name, src := range map[string]string{
		"added func":       base + "func G() {}\n",
		"changed sig":      strings.Replace(base, "F(a int)", "F(a int64)", 1),
		"added field":      strings.Replace(base, "Name string;", "Name string; Extra bool;", 1),
		"reached a type":   base + "var W unreached\n",
		"dropped a method": strings.Replace(base, "func (hidden) Do() error { return nil }", "", 1),
	} {
		if render(src) == want {
			t.Errorf("%s: the rendering did not change", name)
		}
	}
}

// renderAPI renders the reachable surface of the package in dir, one
// declaration per line, sorted, so moving code changes nothing.
func renderAPI(dir string) (string, error) {
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return "", err
	}
	types := map[string]*ast.TypeSpec{}
	methods := map[string][]*ast.FuncDecl{}
	var roots []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				d.Body = nil
				if d.Recv == nil {
					roots = append(roots, printNode(fset, d))
					continue
				}
				for _, field := range d.Recv.List {
					field.Names = nil
				}
				recv := baseTypeName(d.Recv.List[0].Type)
				methods[recv] = append(methods[recv], d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						keepExportedFields(s)
						types[s.Name.Name] = s
					case *ast.ValueSpec:
						if e := renderValue(fset, d.Tok, s); e != "" {
							roots = append(roots, e)
						}
					}
				}
			}
		}
	}

	// Walk from every exported name to the types it mentions.
	reached := map[string]bool{}
	var queue []string
	reach := func(text string) {
		for name := range types {
			if !reached[name] && mentions(text, name) {
				reached[name] = true
				queue = append(queue, name)
			}
		}
	}
	for name := range types {
		if ast.IsExported(name) {
			reached[name] = true
			queue = append(queue, name)
		}
	}
	for _, r := range roots {
		reach(r)
	}
	var entries []string
	entries = append(entries, roots...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		decl := "type " + printNode(fset, types[name])
		entries = append(entries, decl)
		reach(decl)
		for _, m := range methods[name] {
			sig := printNode(fset, m)
			entries = append(entries, sig)
			reach(sig)
		}
	}
	for i, e := range entries {
		entries[i] = normalize(e)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n") + "\n", nil
}

// keepExportedFields drops a struct's unexported fields, which no user can
// name, so that only the reachable fields are rendered.
func keepExportedFields(s *ast.TypeSpec) {
	st, ok := s.Type.(*ast.StructType)
	if !ok {
		return
	}
	var kept []*ast.Field
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			if embeddedExported(field.Type) {
				kept = append(kept, field)
			}
			continue
		}
		var names []*ast.Ident
		for _, n := range field.Names {
			if n.IsExported() {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			field.Names = names
			kept = append(kept, field)
		}
	}
	st.Fields.List = kept
}

// renderValue renders the exported names of a var or const spec, or "" when
// it has none. A variable renders with its type, a constant with its value.
func renderValue(fset *token.FileSet, tok token.Token, s *ast.ValueSpec) string {
	var names []*ast.Ident
	var values []ast.Expr
	for i, n := range s.Names {
		if !n.IsExported() {
			continue
		}
		names = append(names, n)
		if i < len(s.Values) {
			values = append(values, s.Values[i])
		}
	}
	if len(names) == 0 {
		return ""
	}
	s.Names = names
	if tok == token.VAR {
		s.Values = nil
	} else {
		s.Values = values
	}
	return tok.String() + " " + printNode(fset, s)
}

// baseTypeName is the type name of a method receiver, pointer or not.
func baseTypeName(t ast.Expr) string {
	switch e := t.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return baseTypeName(e.X)
	case *ast.IndexExpr:
		return baseTypeName(e.X)
	}
	return ""
}

// embeddedExported reports whether an embedded field's type name is exported.
func embeddedExported(t ast.Expr) bool {
	switch e := t.(type) {
	case *ast.Ident:
		return e.IsExported()
	case *ast.StarExpr:
		return embeddedExported(e.X)
	case *ast.SelectorExpr:
		return e.Sel.IsExported()
	}
	return false
}

// mentions reports whether text names the identifier name as a whole word.
func mentions(text, name string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(name)
		if (start == 0 || !identByte(text[start-1])) && (end == len(text) || !identByte(text[end])) {
			return true
		}
		i = end
	}
}

func identByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// normalize puts a declaration on one line with single spaces and no space
// inside braces, so layout, one-line versus multi-line structs, and the blank
// lines a removed comment leaves behind are not part of it.
func normalize(s string) string {
	s = strings.ReplaceAll(strings.Join(strings.Fields(s), " "), " {", "{")
	return strings.NewReplacer("; ", " ", "{ ", "{", " }", "}").Replace(s)
}

func printNode(fset *token.FileSet, node any) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, node)
	return b.String()
}
