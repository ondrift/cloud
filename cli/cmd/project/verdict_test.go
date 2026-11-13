package project

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
		case "/ops/slice/get":
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
