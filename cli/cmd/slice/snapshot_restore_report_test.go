package slice

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// What `drift slice snapshot restore` prints from the platform's answer.
//
// The answer is {"restored": {<counts>, "notes": [...]}, "errors": [...]}. An
// error is a component that did not come back, and it fails the command. A note
// is work the restore could not do and the caller has to finish, such as
// installing the dependencies of an interpreted backend, which the archive does
// not hold. Everything the archive holds landed, so a restore with notes is a
// success, and its notes are printed under Next: after the counts.

// The counts as the platform sends them: every one, always, each with its own
// value so that a count printed on the wrong line is seen. The last two are sent
// and not printed.
const reportCounts = `"secrets":1,"nosql_collections":2,"blobs":3,"queues":4,"sql_databases":5,` +
	`"functions":6,"canvas":7,"vault_entries":8,"link_identities":9,"pocket_items":10,` +
	`"site_auth_users":11,"labels":12`

// What the command prints for those counts.
const reportCountLines = "" +
	"  Secrets:     1\n" +
	"  NoSQL:       2 collections\n" +
	"  Blobs:       3\n" +
	"  Queues:      4\n" +
	"  SQL:         5 databases\n" +
	"  Vault:       8 entries\n" +
	"  Link:        9 identities\n" +
	"  Pocket:      10 items\n" +
	"  Functions:   6\n" +
	"  Canvas:      7 sites\n"

// The headline of a restore whose answer lists no errors, and of one that lists
// the two failures below; and what follows the list of errors.
const (
	reportHeadlineWhole  = "✓ Snapshot snap-1 restored.\n\n"
	reportHeadlineErrors = "✗ Snapshot snap-1 restored WITH ERRORS — 2 component(s) did not come back.\n\n"
	reportErrorsClosing  = "\n" +
		"  A function listed above is NOT running. Check `restorable` in the\n" +
		"  snapshot's atomic-manifest.json — a function deployed before its entry\n" +
		"  point was recorded cannot be rebuilt from the archive, and redeploying\n" +
		"  it from source is the fix.\n"
)

const (
	reportFailureOne = `atomic "checkout": no slot available: the slice has no free function slot`
	reportFailureTwo = `blob photos/cat.png: upstream timed out`
)

// Notes as the platform words them, one per interpreted backend. The command
// prints whatever it is sent and the wording is the platform's, so the tests
// compare what is printed with what was sent and say nothing about the words.
const (
	reportNoteDefault = "atomic: the default backend is node and its dependencies (package.json) are not in the " +
		"archive by design. If this slice has not already deployed it, run `drift apply` from your project's " +
		"source (the archive holds no Driftfile) to install them, or its functions will fail at first invocation"
	reportNoteNamed = "atomic: backend \"api\" is python and its dependencies (requirements.txt) are not in the " +
		"archive by design. If this slice has not already deployed it, run `drift apply` from your project's " +
		"source (the archive holds no Driftfile) to install them, or its functions will fail at first invocation"
)

// reportJSON is v as JSON, for the parts of an answer that hold text.
func reportJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// reportNotes is the `"notes"` member of an answer that carries these notes.
func reportNotes(t *testing.T, notes ...string) string {
	t.Helper()
	return `,"notes":` + reportJSON(t, notes)
}

// reportAnswer is a restore answer: notes is "" for an answer with no notes
// member, as the platform sends one with nothing to follow up, or a reportNotes;
// errors is the JSON of the "errors" member.
func reportAnswer(notes, errors string) string {
	return `{"restored":{` + reportCounts + notes + `},"errors":` + errors + `}`
}

// reportOf prints an answer and returns what was written and what the report
// ends the command with. The glyphs are plain whatever terminal the tests run in.
func reportOf(t *testing.T, answer string) (string, error) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	err := printRestoreReport(&out, "snap-1", []byte(answer))
	return out.String(), err
}

// reportSame fails when got is not want, naming the first line that differs.
func reportSame(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			t.Errorf("line %d differs:\n printed %q\n    want %q\nwhole output:\n%s", i+1, gl, wl, got)
			return
		}
	}
}

// A restore that needs follow-up work says so after the counts, and is still a
// success: the notes are not errors.
func TestRestoreReport_PrintsTheNotesThePlatformReturns(t *testing.T) {
	// The platform sends "errors" as null when nothing failed, and an empty list
	// is the same answer.
	for _, noErrors := range []string{`null`, `[]`} {
		got, err := reportOf(t, reportAnswer(reportNotes(t, reportNoteDefault), noErrors))
		if err != nil {
			t.Fatalf(`errors %s: a restore with notes and no errors is a success, got: %v`, noErrors, err)
		}
		reportSame(t, got, reportHeadlineWhole+reportCountLines+
			"\n"+
			"  Next:\n"+
			"    - "+reportNoteDefault+"\n")
	}
}

// One note per interpreted backend, so a slice with two has two. They share one
// heading, in the order the platform sent them.
func TestRestoreReport_SeveralNotesShareOneHeading(t *testing.T) {
	got, err := reportOf(t, reportAnswer(reportNotes(t, reportNoteDefault, reportNoteNamed), `null`))
	if err != nil {
		t.Fatalf("a restore with notes and no errors is a success, got: %v", err)
	}
	reportSame(t, got, reportHeadlineWhole+reportCountLines+
		"\n"+
		"  Next:\n"+
		"    - "+reportNoteDefault+"\n"+
		"    - "+reportNoteNamed+"\n")
}

// A restore that failed in part and also has follow-up work prints both, the
// notes first because they follow the counts, and still fails the command.
func TestRestoreReport_ErrorsAndNotesPrintBothAndTheRestoreIsStillIncomplete(t *testing.T) {
	failures := reportJSON(t, []string{reportFailureOne, reportFailureTwo})
	got, err := reportOf(t, reportAnswer(reportNotes(t, reportNoteDefault), failures))
	if err == nil || err.Error() != "restore incomplete: 2 component(s) failed" {
		t.Errorf("the error = %v, want the restore incomplete error for the 2 failures", err)
	}
	reportSame(t, got, reportHeadlineErrors+reportCountLines+
		"\n"+
		"  Next:\n"+
		"    - "+reportNoteDefault+"\n"+
		"\n"+
		"  Errors (2):\n"+
		"    - "+reportFailureOne+"\n"+
		"    - "+reportFailureTwo+"\n"+
		reportErrorsClosing)
}

// An answer with no notes has no Next: block. It prints the headline and the
// counts, and the errors block when there are errors, and nothing else, so the
// command's output for such an answer is the text written out here.
//
// This is the control for the three above: it holds on every answer that has
// nothing to follow up, however the platform spells the absence.
func TestRestoreReport_WithNoNotesPrintsWhatItAlwaysPrinted(t *testing.T) {
	whole := reportHeadlineWhole + reportCountLines
	failures := reportJSON(t, []string{reportFailureOne, reportFailureTwo})
	failed := reportHeadlineErrors + reportCountLines +
		"\n" +
		"  Errors (2):\n" +
		"    - " + reportFailureOne + "\n" +
		"    - " + reportFailureTwo + "\n" +
		reportErrorsClosing

	for _, c := range []struct {
		name   string
		answer string
		want   string
		failed bool
	}{
		{"no notes member, errors null", reportAnswer("", `null`), whole, false},
		{"no notes member, errors empty", reportAnswer("", `[]`), whole, false},
		{"an empty list of notes", reportAnswer(`,"notes":[]`, `[]`), whole, false},
		{"a null list of notes", reportAnswer(`,"notes":null`, `null`), whole, false},
		{"notes that are blank", reportAnswer(reportNotes(t, "", "  \n"), `[]`), whole, false},
		{"errors and no notes member", reportAnswer("", failures), failed, true},
		{"errors and blank notes", reportAnswer(reportNotes(t, ""), failures), failed, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := reportOf(t, c.answer)
			if c.failed {
				if err == nil || err.Error() != "restore incomplete: 2 component(s) failed" {
					t.Errorf("the error = %v, want the restore incomplete error for the 2 failures", err)
				}
			} else if err != nil {
				t.Errorf("a restore with no errors is a success, got: %v", err)
			}
			reportSame(t, got, c.want)
		})
	}
}

// reportAPI is the platform as far as a restore reaches it: the question whether
// the snapshot has a passphrase is answered no, and the restore is answered with
// answer. A request for anything else is answered 404 and not judged, because a
// request that is not this test's can arrive from a test that ran before it.
func reportAPI(answer string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ops/slice/snapshot/status":
			_, _ = io.WriteString(w, `{"passphrase_protected":false}`)
		case r.Method == http.MethodPost && r.URL.Path == "/ops/slice/snapshot/restore":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, answer)
		default:
			http.NotFound(w, r)
		}
	}
}

// reportRun runs `drift slice snapshot restore snap-1` against a platform that
// answers with answer, and returns what it printed and what it returned.
func reportRun(t *testing.T, answer string) (string, error) {
	t.Helper()
	stubLinkAPI(t, reportAPI(answer))
	cmd := getSnapshotRestoreCmd()
	var err error
	out := captured(t, func() { err = cmd.RunE(cmd, []string{"snap-1"}) })
	return out, err
}

// The command prints the report the platform's answer makes and ends with the
// error that report ends with. The report is tested above on its own; this is
// the command's use of it: a restore that came back with notes exits 0, and one
// that came back with errors exits non-zero whatever else it printed.
func TestSnapshotRestoreCmd_PrintsTheNotesAndReturnsTheReportsVerdict(t *testing.T) {
	t.Run("notes and no errors", func(t *testing.T) {
		out, err := reportRun(t, reportAnswer(reportNotes(t, reportNoteDefault), `null`))
		if err != nil {
			t.Fatalf("a restore with notes and no errors must exit 0, got: %v", err)
		}
		reportSame(t, out, reportHeadlineWhole+reportCountLines+
			"\n"+
			"  Next:\n"+
			"    - "+reportNoteDefault+"\n")
	})

	t.Run("notes and errors", func(t *testing.T) {
		failures := reportJSON(t, []string{reportFailureOne, reportFailureTwo})
		out, err := reportRun(t, reportAnswer(reportNotes(t, reportNoteDefault), failures))
		if err == nil || err.Error() != "restore incomplete: 2 component(s) failed" {
			t.Errorf("a restore with errors must fail with the restore incomplete error, got: %v", err)
		}
		reportSame(t, out, reportHeadlineErrors+reportCountLines+
			"\n"+
			"  Next:\n"+
			"    - "+reportNoteDefault+"\n"+
			"\n"+
			"  Errors (2):\n"+
			"    - "+reportFailureOne+"\n"+
			"    - "+reportFailureTwo+"\n"+
			reportErrorsClosing)
	})
}

// The command's output for an answer with no notes. It runs the command itself,
// so it holds the output whoever prints it.
func TestSnapshotRestoreCmd_WithNoNotesPrintsWhatItAlwaysPrinted(t *testing.T) {
	t.Run("whole", func(t *testing.T) {
		out, err := reportRun(t, reportAnswer("", `null`))
		if err != nil {
			t.Fatalf("a restore with no errors must exit 0, got: %v", err)
		}
		reportSame(t, out, reportHeadlineWhole+reportCountLines)
	})

	t.Run("with errors", func(t *testing.T) {
		failures := reportJSON(t, []string{reportFailureOne, reportFailureTwo})
		out, err := reportRun(t, reportAnswer("", failures))
		if err == nil || err.Error() != "restore incomplete: 2 component(s) failed" {
			t.Errorf("a restore with errors must fail with the restore incomplete error, got: %v", err)
		}
		reportSame(t, out, reportHeadlineErrors+reportCountLines+
			"\n"+
			"  Errors (2):\n"+
			"    - "+reportFailureOne+"\n"+
			"    - "+reportFailureTwo+"\n"+
			reportErrorsClosing)
	})
}
