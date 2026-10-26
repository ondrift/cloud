package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
)

// scripted answers questions in order, and records every label it was asked, so a
// test can say what was and was not put to the person.
type scripted struct {
	answers []string
	asked   []string
}

func (s *scripted) ask(label string) string {
	s.asked = append(s.asked, label)
	if len(s.answers) == 0 {
		return ""
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

// The guide asks for the slice's name, the first pulse's method,
// route and language, and a site folder, in that order, and the file it writes is
// the answers: the pulse's handler is the one `drift atomic new` produces for
// them, so the two halves still compose.
func TestNew_AGuidedRunWritesTheFileFromTheAnswers(t *testing.T) {
	cmd := getNewCmd()
	in := &scripted{answers: []string{"orders-app", "post", "orders", "python", "site"}}
	o := starterOptions{Name: "from-the-directory"}
	if err := o.guide(cmd, "the directory name", in.ask); err != nil {
		t.Fatal(err)
	}
	if len(in.asked) != 5 {
		t.Fatalf("asked %d questions, want 5: %q", len(in.asked), in.asked)
	}
	o.fillDefaults()
	if err := o.validate(); err != nil {
		t.Fatal(err)
	}
	df := o.render()
	for _, want := range []string{
		`slice "orders-app" {}`,
		`pulse "POST /orders"`,
		`handler = "` + atomic_cmd_new.HandlerName("post", "orders", "python") + `"`,
		`site "/"`,
		`dir = "site"`,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("the file does not contain %q:\n%s", want, df)
		}
	}
	if got := o.fnCommand(); got != "drift atomic new orders -l python -m post" {
		t.Errorf("the printed command is %q, which would write a different pulse than the file declares", got)
	}
}

// Enter on every question is the starter file, so a person who just presses it
// gets what a script that passes --yes gets.
func TestNew_EnterEverywhereIsTheDefaultFile(t *testing.T) {
	cmd := getNewCmd()
	in := &scripted{}
	o := starterOptions{Name: "demo"}
	if err := o.guide(cmd, "the directory name", in.ask); err != nil {
		t.Fatal(err)
	}
	o.fillDefaults()
	if got, want := o.render(), starterDriftfile("demo", ""); got != want {
		t.Errorf("pressing Enter wrote a different file than the defaults:\n%s\nwant\n%s", got, want)
	}
}

// A flag answers its question in advance: it is not asked, and its value stands.
func TestNew_AFlagIsNotAsked(t *testing.T) {
	cmd := getNewCmd()
	for flag, v := range map[string]string{"name": "fixed", "method": "put", "lang": "node"} {
		if err := cmd.Flags().Set(flag, v); err != nil {
			t.Fatal(err)
		}
	}
	o := starterOptions{Name: "fixed", Method: "put", Lang: "node"}
	in := &scripted{answers: []string{"jobs", ""}}
	if err := o.guide(cmd, "--name", in.ask); err != nil {
		t.Fatal(err)
	}
	if len(in.asked) != 2 {
		t.Fatalf("asked %q, want only the route and the site folder", in.asked)
	}
	if o.Name != "fixed" || o.Method != "put" || o.Lang != "node" || o.Route != "jobs" {
		t.Errorf("answers = %+v", o)
	}
}

// An answer the file could not be applied with is refused at the question that
// took it, and asked again, rather than written and found by `drift lint`.
func TestNew_ABadAnswerIsAskedAgain(t *testing.T) {
	cmd := getNewCmd()
	in := &scripted{answers: []string{"Bad Name", "good-name", "", "/leading", "orders"}}
	o := starterOptions{Name: "demo"}
	if err := o.guide(cmd, "the directory name", in.ask); err != nil {
		t.Fatal(err)
	}
	if o.Name != "good-name" || o.Route != "orders" {
		t.Errorf("answers = %+v, want the second name and the second route", o)
	}
}

// Without a terminal nothing is asked, and the flags and defaults make the file.
func TestNew_WithYesItAsksNothingAndWritesTheFlagsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cmd := getNewCmd()
	cmd.SetArgs([]string{filepath.Join(dir, "Driftfile"), "--name", "demo", "--route", "orders", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`slice "demo" {}`, `pulse "GET /orders"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the file does not contain %q:\n%s", want, raw)
		}
	}
}

func TestNew_RefusesAnAnswerTheFileCannotBeAppliedWith(t *testing.T) {
	for name, o := range map[string]starterOptions{
		"method":   {Name: "demo", Method: "trace"},
		"language": {Name: "demo", Lang: "cobol"},
		"slash":    {Name: "demo", Route: "/orders"},
		"route":    {Name: "demo", Route: "or ders"},
		"name":     {Name: "Demo"},
	} {
		o.fillDefaults()
		if err := o.validate(); err == nil {
			t.Errorf("%s: %+v was accepted", name, o)
		}
	}
}
