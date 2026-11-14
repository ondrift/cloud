package project

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A plan concludes one value: the slice would be created, differs, could not be
// read in full, or is the file, with what differs and what could not be read.

// Each state is a value of its own, with the lines that justify it.
func TestVerdict_EachStateCarriesWhatJustifiesIt(t *testing.T) {
	for _, c := range verdictCases() {
		t.Run(c.name, func(t *testing.T) {
			p := c.state(t)
			v := p.verdict(p.diff())

			if v.State != c.verdict || string(v.State) != c.name {
				t.Errorf("the verdict is %q, want %q", v.State, c.verdict)
			}
			switch c.verdict {
			case VerdictCreate:
				if !slices.ContainsFunc(v.Changes, func(s string) bool { return strings.HasPrefix(s, "+ function POST /rpc  ") }) {
					t.Errorf("the changes of a slice to create do not add POST /rpc: %q", v.Changes)
				}
			case VerdictDiffers:
				if !slices.ContainsFunc(v.Changes, func(s string) bool { return strings.HasPrefix(s, "~ slots  ") }) {
					t.Errorf("the changes of a slice that differs do not include its slots: %q", v.Changes)
				}
			case VerdictUnread:
				if len(v.Changes) != 0 {
					t.Errorf("a plan with no marked line has the changes %q", v.Changes)
				}
				if len(v.Unread) != 1 || !strings.HasPrefix(v.Unread[0], "site /: sites could not be read (") ||
					!strings.Contains(v.Unread[0], "list canvas sites") {
					t.Errorf("the unread facts are %q, want the sites, once, with why", v.Unread)
				}
			case VerdictMatch:
				if len(v.Changes) != 0 {
					t.Errorf("a match has the changes %q", v.Changes)
				}
			}
			if c.verdict != VerdictUnread && len(v.Unread) != 0 {
				t.Errorf("a plan that read everything has the unread facts %q", v.Unread)
			}
			if len(v.Attention) != 0 {
				t.Errorf("nothing needs a person, and the verdict says %q does", v.Attention)
			}
		})
	}
}

// `drift apply` prints the plan through the same body as `drift plan`, so a slice
// whose deployed functions could not be listed has the header the plan gives it,
// and not the "unchanged" of a slice that was read.
func TestApply_PrintsTheHeaderThePlanPrintsForAnUnreadSlice(t *testing.T) {
	live := ""
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/atomic/list":
			w.WriteHeader(http.StatusInternalServerError)
		case "/ops/canvas":
			_, _ = w.Write([]byte(`{"sites":[]}`))
		default:
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}
	})
	withRealSchema(t)
	applyIn(t, orderDriftfile, "atomic")
	for path, body := range map[string]string{
		filepath.Join("atomic", "hello.js"): helloHandler,
		"schema.sql":                        "CREATE TABLE IF NOT EXISTS txs (id TEXT);",
		"seed.sql":                          "INSERT INTO txs VALUES ('genesis');",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	live = liveAtFileShape(t)

	// What the apply goes on to do is not the subject; it may fail once the
	// functions cannot be listed.
	out := captureStdout(t, func() { _ = applyAgreeingAtZero(t) })
	if got := headerOf(t, out); got != "unchanged (not fully read)" {
		t.Errorf("drift apply gives a slice it could not read in full the header %q, want %q:\n%s", got, "unchanged (not fully read)", out)
	}
}

// A difference found is a difference, whatever else could not be read: the plan
// can say what applying changes, and lists what it could not read beside it.
func TestVerdict_ADifferenceWinsOverAnUnreadFact(t *testing.T) {
	planStub(t, fxevmLive, `[]`, "/ops/canvas")
	p := planStateIn(t, nil)
	v := p.verdict(p.diff())

	if v.State != VerdictDiffers {
		t.Errorf("a slice that differs and could not be read in part is %q, want %q", v.State, VerdictDiffers)
	}
	if len(v.Changes) == 0 || len(v.Unread) != 1 {
		t.Errorf("the verdict has the changes %q and the unread facts %q, want both", v.Changes, v.Unread)
	}
}

// One line per fact the plan could not read, with the slots or the sites it was
// read for, and why on the same line: the same cause for several slots is one
// fact, and a fact that sits at no line of its own (the secrets) names itself.
func TestVerdict_UnreadLinesNameEachFactOnceAndItsReasonOnOneLine(t *testing.T) {
	d := &planDiff{}
	listing := errors.New("Couldn't list atomic functions: the platform is having trouble.\n  DRIFT-1005 · drift doctor explain DRIFT-1005")
	d.noteUnread("function GET /bench", "code", listing)
	d.noteUnread("function POST /rpc", "code", listing)
	d.noteUnread("function POST /rpc", "code", listing)
	d.noteUnread("secrets", "secrets", errors.New("no answer"))
	d.noteUnread("site /", "sites", errors.New("no answer"))
	d.noteUnread("function GET /bench", "code", errors.New("the build cannot read this project"))

	want := []string{
		"function GET /bench, function POST /rpc: code could not be read (Couldn't list atomic functions: the platform is having trouble.)",
		"secrets could not be read (no answer)",
		"site /: sites could not be read (no answer)",
		"function GET /bench: code could not be read (the build cannot read this project)",
	}
	if got := d.unreadLines(); !slices.Equal(got, want) {
		t.Errorf("the unread lines are\n%q\nwant\n%q", got, want)
	}
}

// A command in another package asks for the verdict by calling PlanVerdict. For
// each of the four states it returns the value `drift plan` concludes from the
// same slice, and that value is the state whose header the plan prints.
func TestPlanVerdict_ReturnsWhatThePlanConcludesForEachState(t *testing.T) {
	for _, c := range verdictCases() {
		t.Run(c.name, func(t *testing.T) {
			c.stub(t)
			p := planStateIn(t, nil)
			want := p.verdict(p.diff())

			got, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
			if err != nil {
				t.Fatalf("PlanVerdict: %v", err)
			}
			if got.State != c.verdict {
				t.Errorf("PlanVerdict says a slice that is %q is %q", c.name, got.State)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("PlanVerdict returns %+v, and the plan concludes %+v from the same slice", got, want)
			}

			out, err := planOutput(t, "--no-env-file")
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if header := headerOf(t, out); header != got.State.header() {
				t.Errorf("PlanVerdict says the slice is %q, whose header is %q, and `drift plan` prints the header %q",
					got.State, got.State.header(), header)
			}
		})
	}
}

// PlanVerdict prints to the writer it is given and to nothing else: the
// variables it sets and the environment it plans are said there, so a caller
// that hands it io.Discard prints nothing.
func TestPlanVerdict_PrintsOnlyToTheWriterItIsGiven(t *testing.T) {
	planStub(t, "", `[]`)
	inFixture(t, withEnvironments(""))
	t.Setenv("EXTRA", "from the terminal")

	var (
		out strings.Builder
		v   Verdict
		err error
	)
	stdout := captureStdout(t, func() {
		v, err = PlanVerdict(PlanOptions{
			Environment: "staging",
			Variables:   []string{"EXTRA=from the flag"},
			NoEnvFile:   true,
		}, &out)
	})
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	if stdout != "" {
		t.Errorf("PlanVerdict printed %q on the process's stdout, and not to its writer", stdout)
	}
	for _, want := range []string{"environment staging → slice", "fxevm-testnode-staging", "already set in the environment", "EXTRA"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the writer PlanVerdict was given does not say %q:\n%s", want, out.String())
		}
	}
	if v.State != VerdictCreate {
		t.Errorf("staging has no slice yet, and PlanVerdict says %q, not %q", v.State, VerdictCreate)
	}
}

// PlanVerdict reads the slice the Driftfile names, whichever slice the caller has
// active, and leaves the caller's active slice as it found it: a command that
// asks for the verdict and goes on to act on its own slice acts on that one.
func TestPlanVerdict_ReadsTheFilesSliceAndLeavesTheActiveSliceAlone(t *testing.T) {
	for _, c := range []struct {
		name, active string
		set          bool
	}{
		{name: "another slice is active", active: "the-callers-slice", set: true},
		{name: "none is active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var asked []string
			stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ops/slice" {
					asked = append(asked, r.Header.Get("X-Slice"))
				}
				w.WriteHeader(http.StatusNotFound)
			})
			inFixture(t, nil)
			if c.set {
				t.Setenv(common.SliceEnv, c.active)
			} else if err := os.Unsetenv(common.SliceEnv); err != nil {
				t.Fatal(err)
			}

			if _, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard); err != nil {
				t.Fatalf("PlanVerdict: %v", err)
			}
			if !slices.Equal(asked, []string{"fxevm-testnode"}) {
				t.Errorf("the plan read the slices %q, want the Driftfile's slice fxevm-testnode once", asked)
			}
			got, set := os.LookupEnv(common.SliceEnv)
			if set != c.set || got != c.active {
				t.Errorf("%s was %q (set: %v) before PlanVerdict and is %q (set: %v) after it",
					common.SliceEnv, c.active, c.set, got, set)
			}
		})
	}
}

// The verdict is a conclusion about a slice that was read. A caller with no
// session, or one that cannot reach the platform, gets the error that says so
// and no verdict: not a match, and not a zero value that reads as one.
func TestPlanVerdict_WithoutASessionOrTheNetworkIsTheErrorAndNoVerdict(t *testing.T) {
	for _, c := range []struct {
		name string
		// lose takes away what the plan needs, and want is what the error says.
		lose func(t *testing.T)
		want string
	}{
		{
			name: "no session",
			lose: func(t *testing.T) {
				if err := common.ClearSession(); err != nil {
					t.Fatal(err)
				}
				t.Setenv(common.TokenEnv, "")
			},
			want: "not logged in",
		},
		{
			name: "no network",
			lose: func(t *testing.T) {
				previous := common.APIBaseURL
				common.APIBaseURL = "http://127.0.0.1:1"
				t.Cleanup(func() { common.APIBaseURL = previous })
			},
			want: "fetch slice",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := planStub(t, fxevmLive, `[]`)
			c.lose(t)
			inFixture(t, nil)

			v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("with %s PlanVerdict does not fail with an error that says %q: %v", c.name, c.want, err)
			}
			if !reflect.DeepEqual(v, Verdict{}) {
				t.Errorf("with %s PlanVerdict returns the verdict %+v beside its error", c.name, v)
			}
			if n := rec.count("GET /ops/slice"); n != 0 {
				t.Errorf("with %s the platform stub still answered %d reads of the slice", c.name, n)
			}
		})
	}
}
