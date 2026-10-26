// contract_test.go — the documented handler contract IS the generated one.
//
// Two statements of one contract are edited in different places by different
// changes: the wrapper this package generates, and `sdk/README.md`, which the
// README itself calls "the single source of truth for all six SDKs". Nothing
// compared them, and they drifted twice before — the Node handler contract in
// the docs was not the one the wrapper generated, and scheduling a function
// worked by neither documented route. Both were fixed by hand, and nothing
// stopped either recurring.
//
// WHY THIS IS NOT TWO FILES AGREEING WITH EACH OTHER. The table below is not
// the contract; the WRAPPER is, because the wrapper is what runs. Every
// assertion reads the embedded template — the same bytes `drift apply`
// ships — and derives what a handler must look like from it. The README is then
// held to that. A change to the wrapper alone fails here, a change to the README
// alone fails here, and a change to both together passes, which is the whole
// point.
//
// The honest limit: this pins the SHAPE (how many arguments, in what order, and
// how the result is read back), not the runtime. A wrapper that passes the right
// arguments to a handler that then cannot run is out of scope — issue #70 names
// deploying the documented example as the stronger check, and this is the cheaper
// half it also names.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmePath is a SIBLING of this module, and that is safe here in a way it is
// not in general: `cli/` and `sdk/` live in one repository (cloud/public), so
// the file is always present in a checkout that can run this test.
const readmePath = "../../../../../sdk/README.md"

// resultShape is how a wrapper reads what the handler returned.
type resultShape int

const (
	// positional: the wrapper reads result[0], result[1], … or destructures in
	// order. The handler returns a sequence — an array or a tuple.
	positional resultShape = iota
	// byKey: the wrapper reads result["status"] and friends. The handler returns
	// a map, a hash, a dict.
	byKey
)

func (s resultShape) String() string {
	if s == byKey {
		return "by key (a hash/map)"
	}
	return "positional (an array/tuple)"
}

// wrapper is one generated entry point, and the facts this file checks about it.
type wrapper struct {
	lang string
	// tmpl is the embedded template — the bytes the deploy actually ships.
	tmpl string
	// call is the argument list the wrapper passes to the user's handler, as it
	// appears in the template. This is ASSERTED against the template rather than
	// trusted: see TestWrapperPassesTheArgumentsThisFileClaims.
	call string
	// shape is how the wrapper reads the handler's return value.
	shape resultShape
	// runsTheHandlerItself records that the generated wrapper calls the SDK's
	// run() entry point. Where it does, a user who also calls run() in their own
	// file has it executed at import time, before the wrapper's own call.
	runsTheHandlerItself bool
}

// every generated wrapper, both method classes.
func wrappers() []wrapper {
	return []wrapper{
		{"node", wrapperGetNode, "req", positional, true},
		{"node", wrapperPostNode, "body, req", positional, true},
		{"python", wrapperGetPython, "req", positional, true},
		{"python", wrapperPostPython, "body, req", positional, true},
		{"ruby", wrapperGetRuby, "req", positional, true},
		{"ruby", wrapperPostRuby, "body, req", positional, true},
		{"php", wrapperGetPHP, "$req", positional, true},
		{"php", wrapperPostPHP, "$body, $req", positional, true},
		{"rust", wrapperGetRust, "req", positional, true},
		{"rust", wrapperPostRust, "body, req", positional, true},
	}
}

// callRe finds the one call to the user's handler. {{FUNC}} is the placeholder
// the generator substitutes, so it marks the call site unambiguously — no
// language parsing, and nothing else in a template matches it.
var callRe = regexp.MustCompile(`\{\{FUNC\}\}\(([^)]*)\)`)

func handlerCall(t *testing.T, w wrapper) string {
	t.Helper()
	m := callRe.FindStringSubmatch(w.tmpl)
	if m == nil {
		t.Fatalf("%s: the template never calls {{FUNC}}, so nothing here is checking a handler at all", w.lang)
	}
	return strings.TrimSpace(m[1])
}

// THE ARGUMENTS THE WRAPPER ACTUALLY PASSES, read off the template.
//
// This is what makes the rest of the file mean something: the `call` field is
// a claim, and this is where the claim meets the bytes. Change a template's
// call and this fails before any README check runs.
func TestWrapperPassesTheArgumentsThisFileClaims(t *testing.T) {
	for _, w := range wrappers() {
		got := handlerCall(t, w)
		if got != w.call {
			t.Errorf("%s: the wrapper calls the handler with (%s), but this file says (%s).\n"+
				"If the wrapper is right, update the table in contract_test.go AND the\n"+
				"Arguments rule in sdk/README.md — they are one contract.", w.lang, got, w.call)
		}
	}
}

// A BODY-LESS METHOD PASSES ONE ARGUMENT; A BODY METHOD PASSES TWO, BODY FIRST.
//
// Derived from the templates rather than restated: the GET wrapper's call must
// be exactly the POST wrapper's call minus its first argument, in every
// language. That is the rule the README states in prose, checked against the
// thing that implements it.
func TestBodyMethodsTakeTheBodyFirstAndTheRequestSecond(t *testing.T) {
	byLang := map[string][]string{}
	for _, w := range wrappers() {
		byLang[w.lang] = append(byLang[w.lang], handlerCall(t, w))
	}
	for lang, calls := range byLang {
		if len(calls) != 2 {
			t.Fatalf("%s: expected a body-less and a body wrapper, got %d", lang, len(calls))
		}
		bodyless, withBody := calls[0], calls[1]
		parts := strings.Split(withBody, ",")
		if len(parts) != 2 {
			t.Errorf("%s: the body wrapper passes %q; the contract is two arguments, body first", lang, withBody)
			continue
		}
		if rest := strings.TrimSpace(parts[1]); rest != bodyless {
			t.Errorf("%s: the body wrapper passes (%s) and the body-less one passes (%s).\n"+
				"The second argument of the first must be the only argument of the second,\n"+
				"or a handler cannot be written against both.", lang, withBody, bodyless)
		}
	}
}

// HOW THE WRAPPER READS THE RESULT, read off the template.
//
// A wrapper that indexes with a number wants a sequence; one that indexes with
// a name wants a map. Nothing else can be inferred from the outside, and it is
// exactly what a handler author has to get right.
var (
	numericIndex = regexp.MustCompile(`result\[[0-3]\]|\$result\[[0-3]\]`)
	namedIndex   = regexp.MustCompile(`result\[['"](status|message|payload)['"]\]`)
	destructure  = regexp.MustCompile(`(?:const|let)\s*[\[(]\s*status\s*,|let \(status`)
)

func observedShape(w wrapper) resultShape {
	if namedIndex.MatchString(w.tmpl) {
		return byKey
	}
	return positional
}

func TestWrapperReadsTheResultTheWayThisFileClaims(t *testing.T) {
	for _, w := range wrappers() {
		got := observedShape(w)
		if got != w.shape {
			t.Errorf("%s: the wrapper reads the handler's result %s, not %s", w.lang, got, w.shape)
		}
		// A positional read has to be visible as one, or `observedShape` is
		// returning the default and proving nothing.
		if got == positional && !numericIndex.MatchString(w.tmpl) && !destructure.MatchString(w.tmpl) {
			t.Errorf("%s: the wrapper is claimed to read the result positionally, but the\n"+
				"template neither indexes it by number nor destructures it. This check is\n"+
				"passing by default rather than by observation.", w.lang)
		}
	}
}

// ---- the README, held to the wrappers above --------------------------------

func readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(readmePath))
	if err != nil {
		// NOT a skip. A skip prints green, and the whole purpose of this file is
		// that the two statements cannot drift unnoticed.
		t.Fatalf("cannot read the documented contract at %s: %v\n"+
			"cli/ and sdk/ are one repository; if that stopped being true, this\n"+
			"check needs a new home rather than a skip.", readmePath, err)
	}
	return string(b)
}

// THE ARGUMENT RULE, as the README states it in prose.
func TestReadmeStatesTheArgumentRuleTheWrappersImplement(t *testing.T) {
	doc := readme(t)
	// Both spellings the wrappers use, reduced to the language-neutral form the
	// README writes them in.
	for _, want := range []string{"`(req)`", "`(body, req)`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("sdk/README.md never states %s as a handler's arguments.\n"+
				"Every wrapper passes exactly that; the README is the documented half\n"+
				"and has to say so.", want)
		}
	}
}

// THE RETURN SHAPE, per language, held to what the wrapper does with it.
//
// This is the assertion that catches a README describing a shape the wrapper
// cannot consume — a handler written to the documented shape then returns
// something the generated entry point reads as empty, and the failure surfaces
// as a 200 full of nulls rather than as an error.
func TestReadmeReturnShapeMatchesWhatEachWrapperReads(t *testing.T) {
	doc := readme(t)

	// The README names the shape per language in one sentence: "It's a tuple in
	// Go/Python/Rust, an array in Node/PHP, a hash in Ruby."
	line := ""
	for _, l := range strings.Split(doc, "\n") {
		if strings.Contains(l, "**Return**") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("sdk/README.md no longer carries a **Return** rule; the contract has lost its documented half")
	}

	claimed := map[string]resultShape{}
	for _, lang := range []string{"Python", "Node", "Ruby", "PHP", "Rust"} {
		idx := strings.Index(line, lang)
		if idx < 0 {
			t.Errorf("the **Return** rule never mentions %s", lang)
			continue
		}
		// The shape word is whichever of the three appears most recently before
		// the language is named.
		best, shape := -1, positional
		for word, s := range map[string]resultShape{"tuple": positional, "array": positional, "hash": byKey, "map": byKey} {
			if at := strings.LastIndex(line[:idx], word); at > best {
				best, shape = at, s
			}
		}
		if best < 0 {
			t.Errorf("the **Return** rule names %s without naming a shape for it", lang)
			continue
		}
		claimed[strings.ToLower(lang)] = shape
	}

	for _, w := range wrappers() {
		want, ok := claimed[w.lang]
		if !ok {
			continue
		}
		got := observedShape(w)
		if got != want {
			t.Errorf("%s: sdk/README.md documents a return read %s, but the generated\n"+
				"wrapper reads it %s.\n"+
				"A handler written to the README would have its status, message and\n"+
				"payload read as empty, and the caller would see a well-formed reply\n"+
				"with nothing in it.", w.lang, want, got)
		}
	}
}

// THE README MUST NOT TELL A USER TO CALL run() THEMSELVES where the generated
// wrapper already does.
//
// The wrapper `require`s the user's file and then calls run(). A user file that
// also calls run() at top level has it executed at import time — in deployed
// mode that reads the request envelope off stdin before the wrapper's own call
// ever happens, and the function answers nothing.
func TestReadmeDoesNotTellUsersToCallRunWhereTheWrapperDoes(t *testing.T) {
	doc := readme(t)
	runCall := map[string]*regexp.Regexp{
		"ruby":   regexp.MustCompile(`(?m)^\s*Drift\.run\(`),
		"python": regexp.MustCompile(`(?m)^\s*drift\.run\(`),
		"node":   regexp.MustCompile(`(?m)^\s*drift\.run\(`),
	}
	for _, w := range wrappers() {
		re, checked := runCall[w.lang]
		if !checked || !w.runsTheHandlerItself {
			continue
		}
		section := languageSection(doc, w.lang)
		if section == "" {
			continue
		}
		if re.MatchString(section) {
			t.Errorf("%s: the generated wrapper already calls run(), and sdk/README.md\n"+
				"still shows a handler calling it too. Required by the wrapper, that line\n"+
				"runs at import time and consumes the request before the wrapper does.\n"+
				"The %s SDK's own header says: \"You do NOT call run yourself.\"", w.lang, w.lang)
		}
	}
}

// languageSection returns the README between one `### <Lang>` heading and the
// next heading of the same level, so a match cannot be attributed to the wrong
// language.
func languageSection(doc, lang string) string {
	titles := map[string]string{"node": "Node.js", "ruby": "Ruby", "python": "Python", "php": "PHP", "rust": "Rust"}
	head := fmt.Sprintf("### %s", titles[lang])
	start := strings.Index(doc, head)
	if start < 0 {
		return ""
	}
	rest := doc[start+len(head):]
	if end := strings.Index(rest, "\n### "); end >= 0 {
		return rest[:end]
	}
	return rest
}
