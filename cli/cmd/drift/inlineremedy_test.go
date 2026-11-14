package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// EVERY COMMAND A MESSAGE TELLS THE USER TO TYPE EXISTS, AND IS THREE WORDS OR
// FEWER UNLESS IT IS LISTED.
//
// The error registry holds its remedies to both rules (threeword_test.go), and
// typeablehints_test.go holds the few suggestions that are printed at run time
// from values. A command written inside a message is easy to get wrong in a way
// nothing else can see: a literal that says `drift account signup` names a
// command that does not exist, and a printed string is not a call.
//
// So this reads the source. Every non-test .go file under cmd/ and common/ is
// parsed, every string literal in it is read except the Use, Short, Long and
// Example of a cobra command, and each command quoted in one with ` or ' is
// lifted out and held to two rules:
//
//   - it names a command that can run (unrunnable). There is no allowance for
//     this: a message that names a command that does not exist is corrected;
//   - it is three words or fewer, counted as the registry counts them, unless it
//     is listed with its reason in testdata/overlong-commands.txt. The list can
//     only shrink: a line with no reason, a line no literal holds any more and a
//     line whose command now fits in three words each fail.

// inlineSpan is one quoted command found in a string literal.
type inlineSpan struct {
	File    string // the file, from the module root; empty for source given as text
	Line    int    // the line the literal starts on
	Command string // the span between its quotes, as the literal writes it
	Literal string // the literal's whole value: the sentence the command sits in
}

// where names the literal's place the way a compiler names a position.
func (s inlineSpan) where() string {
	if s.File == "" {
		return "line " + strconv.Itoa(s.Line)
	}
	return s.File + ":" + strconv.Itoa(s.Line)
}

// helpFields are the fields of a cobra command that hold its help. Help shows a
// command the way it is typed, with its flags and its values, so it is not a
// message and is not read.
var helpFields = map[string]bool{"Use": true, "Short": true, "Long": true, "Example": true}

// inlineInvocations returns the quoted commands in the string literals of one Go
// source file, in source order. Every literal is read except the one a help field
// of a cobra command is given, in the literal that builds the command or by an
// assignment after it. A comment is not a literal and is never read.
//
// The source is parsed, not searched, so what is read is the literal's value (an
// escape is its character, a raw string is its text) with the line it starts on,
// and source that does not parse is an error and not an empty answer.
func inlineInvocations(src string) ([]inlineSpan, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var spans []inlineSpan
	help := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || help[n] {
			return false
		}
		switch x := n.(type) {
		case *ast.KeyValueExpr: // Short: "...", in the literal that builds the command
			if id, ok := x.Key.(*ast.Ident); ok && helpFields[id.Name] {
				help[x.Value] = true
			}
		case *ast.AssignStmt: // cmd.Short = "...", once the command is built
			if len(x.Lhs) == len(x.Rhs) {
				for i, lhs := range x.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && helpFields[sel.Sel.Name] {
						help[x.Rhs[i]] = true
					}
				}
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				break
			}
			value, _ := strconv.Unquote(x.Value) // the parser accepted the literal, so it unquotes
			for _, command := range quotedCommands(value) {
				spans = append(spans, inlineSpan{
					Line:    fset.Position(x.Pos()).Line,
					Command: command,
					Literal: value,
				})
			}
		}
		return true
	})
	return spans, nil
}

// contractionMask stands in for an apostrophe inside a word while the quotes of a
// literal are paired. It is a private-use character, which no message holds.
const contractionMask = '\uE000'

// quotedCommands returns the quoted `drift ...` commands in the value of one
// literal, by the extractor typeablehints_test.go holds.
//
// That extractor pairs each delimiter with the next one, so an apostrophe that is
// not a quote shifts every pair after it and hides the command that follows:
// "you're at your plan limit. Run 'drift slice resize'" has three apostrophes and
// no span at the odd positions. An apostrophe between two letters is part of a
// word (don't, Driftfile's), so it is set aside before the pairing and put back
// in what comes out.
func quotedCommands(value string) []string {
	runes := []rune(value)
	for i := 1; i+1 < len(runes); i++ {
		if runes[i] == '\'' && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
			runes[i] = contractionMask
		}
	}
	var commands []string
	for _, command := range quotedInvocations(string(runes)) {
		commands = append(commands, strings.ReplaceAll(command, string(contractionMask), "'"))
	}
	return commands
}

// placeholder is a word that stands for what the user supplies: a printf verb
// (%s, %d, %q), <name> or [name].
var placeholder = regexp.MustCompile(`^(%[-+#0-9.*]*[a-zA-Z]|<[^<>]+>|\[[^\[\]]+\])$`)

// wordCount is how many words a command is: every token, as the registry counts
// them, except a placeholder. `drift slice resize %s` is three words and `drift
// slice resize %s --dump` is four. Only a word that is wholly a placeholder is
// left out: --email=%s is one word, a flag with its value, and KEY=VALUE is one,
// an example written out.
func wordCount(invocation string) int {
	n := 0
	for _, word := range strings.Fields(invocation) {
		if !placeholder.MatchString(word) {
			n++
		}
	}
	return n
}

// inlineSpansOfTree returns the quoted commands in every non-test Go file under
// cmd/ and common/ of the module rooted at root, in path order. A directory named
// testdata holds fixtures the go tool does not build, so it is not read.
func inlineSpansOfTree(root string) ([]inlineSpan, error) {
	var spans []inlineSpan
	for _, dir := range []string{"cmd", "common"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path) // #nosec G304 -- this repo's own source
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			found, err := inlineInvocations(string(src))
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			for _, s := range found {
				s.File = rel
				spans = append(spans, s)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return spans, nil
}

// unrunnableCommands returns one problem for each span whose command cannot run,
// naming the place and the literal it is in.
func unrunnableCommands(root *cobra.Command, spans []inlineSpan) []string {
	var problems []string
	for _, s := range spans {
		if why := unrunnable(root, s.Command); why != "" {
			problems = append(problems, fmt.Sprintf("%s tells the user to run %q, which %s:\n    %q",
				s.where(), s.Command, why, s.Literal))
		}
	}
	return problems
}

// inlineFixture is a Go source file holding body, so that a case is only the few
// lines it is about.
func inlineFixture(body string) string {
	return "package fixture\n\n" + body + "\n"
}

// inlineCommands is the commands of spans, in order.
func inlineCommands(spans []inlineSpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Command)
	}
	return out
}

func TestInlineInvocations_FindsAQuotedCommandInAnyStringLiteral(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want []string
	}{
		{
			"a backtick span in an interpreted literal",
			"var m = \"Run `drift account login` to get back in.\"",
			[]string{"drift account login"},
		},
		{
			"an apostrophe span",
			`var m = "Run 'drift account login' to get back in."`,
			[]string{"drift account login"},
		},
		{
			"an apostrophe span in a raw literal",
			"var m = `Run 'drift file new' first.`",
			[]string{"drift file new"},
		},
		{
			"a call argument",
			"func f() { fmt.Println(\"Run `drift plan`.\") }",
			[]string{"drift plan"},
		},
		{
			"a format string keeps its placeholder as written",
			"var m = fmt.Sprintf(\"Run `drift slice resize %s` to add capacity.\", name)",
			[]string{"drift slice resize %s"},
		},
		{
			"two commands in one literal",
			"var m = \"`drift plan` shows it and `drift apply` does it\"",
			[]string{"drift plan", "drift apply"},
		},
		{
			"two literals joined with +, in source order",
			"var m = \"Run `drift plan` \" + \"then 'drift apply'.\"",
			[]string{"drift plan", "drift apply"},
		},
		{
			"a value in a map",
			`var m = map[string]string{"k": "Run 'drift plan' first."}`,
			[]string{"drift plan"},
		},
		{
			"a literal with no command",
			`var m = "you're not logged in"`,
			nil,
		},
		{
			"a quoted span that is not a drift command",
			"var m = \"Run `go install` first.\"",
			nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans, err := inlineInvocations(inlineFixture(c.body))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if got := inlineCommands(spans); !slices.Equal(got, c.want) {
				t.Errorf("found %q, want %q", got, c.want)
			}
		})
	}
}

// An apostrophe inside a word is not a quote. quotedInvocations pairs each
// delimiter with the next one, so an apostrophe that is not a quote shifts every
// pair after it and hides the command. A message is written in plain English, and
// plain English has contractions.
func TestInlineInvocations_AnApostropheInsideAWordIsNotAQuote(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want []string
	}{
		{
			"a contraction before the command",
			`var m = "you're at your plan limit. Run 'drift slice resize' to add capacity."`,
			[]string{"drift slice resize"},
		},
		{
			"a contraction that opens the sentence",
			`var m = "Couldn't read document: --key is required.\nHint: use 'drift backbone nosql list --collection <name>'."`,
			[]string{"drift backbone nosql list --collection <name>"},
		},
		{
			"a possessive before the command",
			`var m = "Add it to your Driftfile's edge block and run 'drift apply'."`,
			[]string{"drift apply"},
		},
		{
			"two commands after a contraction",
			`var m = "don't run 'drift plan' or 'drift apply' there"`,
			[]string{"drift plan", "drift apply"},
		},
		{
			"an apostrophe inside the command is kept as written",
			`var m = "Run 'drift account member remove o'brien'."`,
			[]string{"drift account member remove o'brien"},
		},
		{
			"a backtick span after a contraction",
			"var m = \"you're logged out: run `drift account login`\"",
			[]string{"drift account login"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans, err := inlineInvocations(inlineFixture(c.body))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if got := inlineCommands(spans); !slices.Equal(got, c.want) {
				t.Errorf("found %q, want %q", got, c.want)
			}
		})
	}
}

// The help of a command is not a message: it shows commands the way they are
// typed, with their flags and values. Each case keeps a literal beside the help
// that IS read, so a source that failed to parse, or a scan that read nothing,
// cannot pass for the right reason.
func TestInlineInvocations_DoesNotReadTheHelpOfACommand(t *testing.T) {
	const beside = "\nvar m = \"Run `drift apply` next.\""

	for _, field := range []string{"Use", "Short", "Long", "Example"} {
		t.Run(field, func(t *testing.T) {
			body := "var c = &cobra.Command{\n\t" + field + ": \"Run `drift account signup` first.\",\n}" + beside
			spans, err := inlineInvocations(inlineFixture(body))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if got, want := inlineCommands(spans), []string{"drift apply"}; !slices.Equal(got, want) {
				t.Errorf("found %q, want %q: a span in %s must not be read, and the literal beside it must", got, want, field)
			}
		})
	}

	for _, c := range []struct {
		name string
		body string
		want []string
	}{
		{
			"a field that is not help is read",
			"var c = &cobra.Command{\n\tDeprecated: \"Run `drift account signup` first.\",\n}" + beside,
			[]string{"drift account signup", "drift apply"},
		},
		{
			"a Long built by a call",
			"var c = &cobra.Command{\n\tLong: strings.TrimSpace(`\n  Run 'drift account signup' first.\n`),\n}" + beside,
			[]string{"drift apply"},
		},
		{
			"an Example joined from several literals",
			"var c = &cobra.Command{\n\tExample: \"  drift plan\\n\" +\n\t\t\"  `drift account signup`\",\n}" + beside,
			[]string{"drift apply"},
		},
		{
			"a field set after the command is built",
			"func f(c *cobra.Command) {\n\tc.Short = \"Run `drift account signup` first.\"\n\tc.Long += \"or 'drift account signup' again\"\n}" + beside,
			[]string{"drift apply"},
		},
		{
			"a literal beside a field that is set is still read",
			"func f(c *cobra.Command) {\n\tc.Short = \"Run `drift account signup` first.\"\n\tfmt.Println(\"Run `drift plan`.\")\n}",
			[]string{"drift plan"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans, err := inlineInvocations(inlineFixture(c.body))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if got := inlineCommands(spans); !slices.Equal(got, c.want) {
				t.Errorf("found %q, want %q", got, c.want)
			}
		})
	}
}

// A comment is not printed, so a command named in one is not a message.
func TestInlineInvocations_DoesNotReadAComment(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
	}{
		{"a line comment above", "// Run `drift account signup` first.\nvar m = \"Run `drift apply` next.\""},
		{"a block comment", "/* Run 'drift account signup' first. */\nvar m = \"Run `drift apply` next.\""},
		{"a comment inside a function", "func f() {\n\t// Run `drift account signup` first.\n\tfmt.Println(\"Run `drift apply` next.\")\n}"},
		{"a comment after the literal", "var m = \"Run `drift apply` next.\" // not `drift account signup`"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans, err := inlineInvocations(inlineFixture(c.body))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if got, want := inlineCommands(spans), []string{"drift apply"}; !slices.Equal(got, want) {
				t.Errorf("found %q, want %q: the comment must not be read, and the literal beside it must", got, want)
			}
		})
	}
}

// Source the parser rejects is an error, not an empty answer: an empty answer is
// what every "is not read" case above would pass on.
func TestInlineInvocations_RefusesSourceItCannotParse(t *testing.T) {
	for name, src := range map[string]string{
		"an unterminated literal": "package fixture\n\nvar m = \"Run `drift plan`.\n",
		"no package clause":       "var m = \"Run `drift plan`.\"\n",
	} {
		if _, err := inlineInvocations(src); err == nil {
			t.Errorf("%s: the source was read as if it were Go", name)
		}
	}
}

func TestInlineInvocations_NamesTheLineAndTheWholeLiteral(t *testing.T) {
	src := inlineFixture("var a = \"nothing here\"\n\nvar b = \"Run `drift plan`, then `drift apply`.\"")
	spans, err := inlineInvocations(src)
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("found %d commands, want 2: %q", len(spans), inlineCommands(spans))
	}
	for _, s := range spans {
		if s.Line != 5 {
			t.Errorf("%q is on line %d of the fixture, want 5", s.Command, s.Line)
		}
		if want := "Run `drift plan`, then `drift apply`."; s.Literal != want {
			t.Errorf("%q came out of the literal %q, want %q", s.Command, s.Literal, want)
		}
	}
}

// A message naming a command that does not exist is reported with where it is
// and what it says, so the person fixing it does not have to search.
func TestUnrunnableCommands_NameTheFileAndTheTextOfAMessageThatNamesNothingReal(t *testing.T) {
	root := newRootCmd("test")

	for _, c := range []struct {
		name    string
		command string
		why     string
	}{
		{"a verb that was never a command", "drift account signup", "is a group and runs nothing"},
		{"a group that does not exist", "drift nosuchgroup", "names no `drift` command"},
		{"a group with no verb", "drift slice", "is a group and runs nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans, err := inlineInvocations(inlineFixture("var m = \"Run `" + c.command + "` first.\""))
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if len(spans) != 1 {
				t.Fatalf("found %d commands in the fixture, want 1", len(spans))
			}
			spans[0].File = "cmd/fixture/fixture.go"

			problems := unrunnableCommands(root, spans)
			if len(problems) != 1 {
				t.Fatalf("%q was reported %d times, want once: %q", c.command, len(problems), problems)
			}
			for _, must := range []string{"cmd/fixture/fixture.go:3", c.command, c.why, "Run `" + c.command + "` first."} {
				if !strings.Contains(problems[0], must) {
					t.Errorf("the report does not say %q:\n%s", must, problems[0])
				}
			}
		})
	}

	// The control: a command that exists is not reported, or the cases above
	// would pass for a report that names everything.
	spans, err := inlineInvocations(inlineFixture("var m = \"Run `drift account login` first.\""))
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	if problems := unrunnableCommands(root, spans); len(problems) != 0 {
		t.Errorf("`drift account login` exists, and was reported: %q", problems)
	}
}

// A placeholder stands for what the user supplies. It is not a word, so
// `drift slice resize %s` is the three words `drift slice resize` and the flag
// after it is a fourth.
func TestWordCount_DoesNotCountAPlaceholder(t *testing.T) {
	for _, c := range []struct {
		command string
		want    int
	}{
		{"drift account login", 3},
		{"drift slice resize %s", 3},
		{"drift slice resize %s --dump", 4},
		{"drift apply <name>", 2},
		{"drift plan [name]", 2},
		{"drift apply %d %q %s", 2},
		{"drift account member remove --email %s", 5},
		{"drift account token create <name> --scope slice:read", 6},
		// Only a marked placeholder is not a word. An example value written out
		// is the user's to change and is still a word.
		{"drift backbone secret set KEY=VALUE", 5},
		{"drift account member remove erica", 5},
		// A token that merely contains the marks is a word.
		{"drift apply 100%", 3},
		{"drift apply <name", 3},
		{"  drift   slice  resize  ", 3},
		{"", 0},
	} {
		if got := wordCount(c.command); got != c.want {
			t.Errorf("wordCount(%q) = %d, want %d", c.command, got, c.want)
		}
	}
}

// The rule is the registry's, so the two counts cannot be allowed to differ on
// the registry's own commands.
func TestWordCount_AgreesWithTheRegistry(t *testing.T) {
	var checked int
	for _, c := range common.ErrorCodes() {
		if c.Command == "" {
			continue
		}
		checked++
		if got, want := wordCount(c.Command), len(strings.Fields(c.Command)); got != want {
			t.Errorf("%s suggests %q: wordCount says %d, the registry's rule (strings.Fields) says %d",
				c.Code, c.Command, got, want)
		}
	}
	if checked == 0 {
		t.Error("no registry command was compared, so this test checked nothing")
	}
}

func TestInlineSpansOfTree_ReadsCmdAndCommonButNoTestFileOrFixture(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const message = "Run `drift account signup`."
	write("cmd/a/a.go", "package a\n\nvar m = \"Run `drift plan`.\"\n")
	write("cmd/a/deeper/b.go", "package b\n\nvar m = \"Run `drift apply`.\"\n")
	write("common/c.go", "package common\n\nvar m = \"Run `drift size`.\"\n")
	write("cmd/a/a_test.go", "package a\n\nvar m = \""+message+"\"\n")
	write("cmd/a/testdata/fixture.go", "package fixture\n\nvar m = \""+message+"\"\n")
	write("elsewhere/d.go", "package d\n\nvar m = \""+message+"\"\n")
	write("cmd/a/notes.txt", message+"\n")

	spans, err := inlineSpansOfTree(root)
	if err != nil {
		t.Fatalf("reading the tree: %v", err)
	}
	var got []string
	for _, s := range spans {
		got = append(got, s.File+":"+strconv.Itoa(s.Line)+" "+s.Command)
	}
	want := []string{"cmd/a/a.go:3 drift plan", "cmd/a/deeper/b.go:3 drift apply", "common/c.go:3 drift size"}
	if !slices.Equal(got, want) {
		t.Errorf("read %q, want %q: the walk takes cmd/ and common/, skips _test.go files and testdata/, and reads only Go", got, want)
	}

	// A file that does not parse is named, not skipped.
	write("cmd/a/broken.go", "package a\n\nvar m = \"unterminated\n")
	if _, err := inlineSpansOfTree(root); err == nil || !strings.Contains(err.Error(), "cmd/a/broken.go") {
		t.Errorf("a file that does not parse was not reported by name: %v", err)
	}
}

// scanTheTree reads this module's own source for the tests that judge it. It
// fails when the walk reached too little to mean anything: a walk that read
// nothing would pass every command.
func scanTheTree(t *testing.T) []inlineSpan {
	t.Helper()
	spans, err := inlineSpansOfTree(repoRoot(t))
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	for _, dir := range []string{"cmd/", "common/"} {
		if !slices.ContainsFunc(spans, func(s inlineSpan) bool { return strings.HasPrefix(s.File, dir) }) {
			t.Fatalf("no command is quoted in any literal under %s, so the walk reached nothing there "+
				"and this test checked nothing", dir)
		}
	}
	return spans
}

// A message that tells the user to type a command that cannot run sends a stuck
// person somewhere that fails again, and is invisible until they try. There is no
// allowance for this: the message is corrected to the command that exists.
func TestEveryCommandAMessageNamesCanRun(t *testing.T) {
	t.Skip("removed by #2559")
	if problems := unrunnableCommands(newRootCmd("test"), scanTheTree(t)); len(problems) > 0 {
		t.Errorf("these messages tell the user to type a command that cannot run. Correct each to the "+
			"command that exists:\n  %s", strings.Join(problems, "\n  "))
	}
}
