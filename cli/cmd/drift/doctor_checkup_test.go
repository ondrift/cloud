package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ondrift/cloud/cli/cmd/doctor"
	"github.com/ondrift/cloud/cli/common"
)

// Bare `drift doctor` is the checkup. These tests run the real command tree
// against a stub of the platform, with an empty HOME and a saved session, and read
// what the command wrote through its writer, what main would write to stderr, and
// the status the process would exit with.

const (
	feedHealthy  = `[{"name":"API","status":"operational"},{"name":"Routing","status":"operational"}]`
	feedRouting  = `[{"name":"API","status":"operational"},{"name":"Routing","status":"degraded"}]`
	routingNote  = "  Routing is not operational right now — this is the platform, not your code."
	codeLineSep  = " · drift doctor explain "
	bareHelpWord = "drift doctor"
)

// checkupPlatform stands up a platform whose status feed answers with whatever
// feed holds, and whose slice list always fails with a 503. It points the CLI at
// it, with a session and no other network.
func checkupPlatform(t *testing.T, initial string) (setFeed func(string)) {
	t.Helper()
	isolate(t)
	t.Setenv("NO_COLOR", "1")
	var feed atomic.Value
	feed.Store(initial)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/status":
			_, _ = w.Write([]byte(feed.Load().(string)))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"the platform could not answer"}`))
		}
	}))
	t.Cleanup(srv.Close)

	apiBase, statusURL := common.APIBaseURL, common.StatusURL
	common.APIBaseURL, common.StatusURL = srv.URL, ""
	t.Cleanup(func() { common.APIBaseURL, common.StatusURL = apiBase, statusURL })
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	return func(s string) { feed.Store(s) }
}

// statusWords are the words a checkup line may start with.
var statusWords = []string{"ok", "problem", "unknown", "skipped"}

// checkupLines are the lines of stdout, none empty.
func checkupLines(t *testing.T, stdout string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != len(doctor.Checklist()) {
		t.Fatalf("%d lines for %d checks:\n%s", len(lines), len(doctor.Checklist()), stdout)
	}
	for _, line := range lines {
		starts := false
		for _, word := range statusWords {
			if strings.HasPrefix(line, word+" ") {
				starts = true
			}
		}
		if !starts {
			t.Errorf("line %q does not start with ok, problem, unknown or skipped", line)
		}
	}
	return lines
}

func TestDoctorBareRunsTheChecklistThroughItsWriter(t *testing.T) {
	checkupPlatform(t, feedHealthy)

	stdout, stderr, status := runTree("doctor")
	if status != 0 || stderr != "" {
		t.Fatalf("a healthy platform: exit %d, stderr %q", status, stderr)
	}
	lines := checkupLines(t, stdout)
	if !strings.HasPrefix(lines[0], "ok ") || !strings.Contains(lines[0], "platform") {
		t.Errorf("the platform line = %q, want ok", lines[0])
	}
}

// One problem line: exit 1, and the error says how many and carries the code,
// once, on stderr.
func TestDoctorAProblemExitsOneAndSaysItOnce(t *testing.T) {
	checkupPlatform(t, feedRouting)

	stdout, stderr, status := runTree("doctor")
	if status != 1 {
		t.Errorf("exit %d, want 1", status)
	}
	lines := checkupLines(t, stdout)
	if !strings.HasPrefix(lines[0], "problem ") || !strings.Contains(lines[0], "DRIFT-1022") {
		t.Errorf("the platform line = %q, want a problem carrying DRIFT-1022", lines[0])
	}
	if !strings.Contains(stderr, "1 problem") {
		t.Errorf("stderr = %q, want it to count the problem lines", stderr)
	}
	if n := strings.Count(stderr, "DRIFT-1022"+codeLineSep+"DRIFT-1022"); n != 1 {
		t.Errorf("stderr names the code's line %d times, want once:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("a problem found is the answer, not a command held wrongly:\n%s", stderr)
	}
}

// THE agreement: the note beside a failing command and the checkup's platform line
// read the same feed, so they name the same component, and flipping the feed flips
// both together.
func TestDoctorAndAFailingCommandAgreeOnWhichComponentIsDown(t *testing.T) {
	setFeed := checkupPlatform(t, feedRouting)

	for _, tc := range []struct {
		name string
		feed string
		down bool
	}{
		{"routing degraded", feedRouting, true},
		{"healthy", feedHealthy, false},
		{"routing degraded again", feedRouting, true},
	} {
		setFeed(tc.feed)

		_, listErr, listStatus := runTree("slice", "list")
		doctorOut, doctorErr, doctorStatus := runTree("doctor")
		if listStatus == 0 {
			t.Errorf("%s: a 503 listing slices exited 0", tc.name)
		}

		if tc.down {
			if !strings.Contains(listErr, routingNote) {
				t.Errorf("%s: the failing command's stderr lacks the 5xx note %q:\n%s", tc.name, routingNote, listErr)
			}
			if !strings.Contains(doctorOut, "Routing") {
				t.Errorf("%s: the checkup does not name Routing:\n%s", tc.name, doctorOut)
			}
			if !strings.Contains(doctorOut, "DRIFT-1022") || doctorStatus != 1 {
				t.Errorf("%s: the checkup exited %d and printed:\n%s", tc.name, doctorStatus, doctorOut)
			}
			continue
		}
		for what, text := range map[string]string{"the failing command": listErr, "the checkup": doctorOut + doctorErr} {
			if strings.Contains(text, "Routing") || strings.Contains(text, "not your code") {
				t.Errorf("%s: %s names a component with the feed healthy:\n%s", tc.name, what, text)
			}
		}
		if doctorStatus != 0 {
			t.Errorf("%s: the checkup exited %d on a healthy feed", tc.name, doctorStatus)
		}
	}
}

// The API does not answer a connection: a problem carrying DRIFT-1006, every line
// printed, a non-zero exit, and inside the bound.
func TestDoctorWithTheAPIRefusingConnectionsIsAProblemCarrying1006(t *testing.T) {
	checkupPlatform(t, feedHealthy)
	closed := httptest.NewServer(http.NotFoundHandler())
	common.APIBaseURL = closed.URL
	closed.Close()

	start := time.Now()
	stdout, stderr, status := runTree("doctor")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the run took %s", elapsed)
	}
	lines := checkupLines(t, stdout)
	if !strings.HasPrefix(lines[0], "problem ") || !strings.Contains(lines[0], "DRIFT-1006") {
		t.Errorf("the platform line = %q, want a problem carrying DRIFT-1006", lines[0])
	}
	if status != 1 || !strings.Contains(stderr, "DRIFT-1006") {
		t.Errorf("exit %d, stderr %q: want 1 and the code", status, stderr)
	}
}

// The API accepts the connection and does not answer: DRIFT-1007.
func TestDoctorWithTheAPITimingOutIsAProblemCarrying1007(t *testing.T) {
	checkupPlatform(t, feedHealthy)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	common.APIBaseURL = srv.URL
	timeout := common.StatusTimeout
	common.StatusTimeout = 200 * time.Millisecond
	t.Cleanup(func() { common.StatusTimeout = timeout })

	stdout, stderr, status := runTree("doctor")
	lines := checkupLines(t, stdout)
	if !strings.HasPrefix(lines[0], "problem ") || !strings.Contains(lines[0], "DRIFT-1007") {
		t.Errorf("the platform line = %q, want a problem carrying DRIFT-1007", lines[0])
	}
	if status != 1 || !strings.Contains(stderr, "DRIFT-1007") {
		t.Errorf("exit %d, stderr %q: want 1 and the code", status, stderr)
	}
}

// A feed that answers something unreadable is neither healthy nor a fault found:
// unknown, with the reason on the line.
func TestDoctorWithAnUnreadableFeedIsUnknownWithTheReason(t *testing.T) {
	checkupPlatform(t, "<html>maintenance</html>")

	stdout, stderr, status := runTree("doctor")
	lines := checkupLines(t, stdout)
	if !strings.HasPrefix(lines[0], "unknown ") || !strings.Contains(lines[0], "not a list of components") {
		t.Errorf("the platform line = %q, want unknown with the reason", lines[0])
	}
	if status != 0 || stderr != "" {
		t.Errorf("exit %d, stderr %q: an unknown line is not a problem line", status, stderr)
	}
}

// The noun holds `explain` and `codes` and nothing else. A third verb replaces one
// of them, which is the Feature's cap, and this is where it is held.
func TestDoctorHoldsExactlyTwoVerbs(t *testing.T) {
	var doctorCmd = subcommand(newRootCmd("test"), "doctor")
	if doctorCmd == nil {
		t.Fatal("`drift doctor` is not registered")
	}
	var verbs []string
	for _, c := range doctorCmd.Commands() {
		if !c.Hidden {
			verbs = append(verbs, c.Name())
		}
	}
	if got := strings.Join(verbs, ","); got != "codes,explain" {
		t.Errorf("`drift doctor` has the verbs %q, want exactly codes and explain: "+
			"the Feature caps the noun at two, so a third verb replaces one", got)
	}
}

func TestDoctorRefusesAVerbItDoesNotHave(t *testing.T) {
	checkupPlatform(t, feedHealthy)

	stdout, stderr, status := runTree("doctor", "nosuch")
	if status == 0 {
		t.Error("`drift doctor nosuch` exited 0")
	}
	if !strings.Contains(stderr, `"nosuch"`) {
		t.Errorf("stderr = %q, want it to name the verb", stderr)
	}
	if stdout != "" {
		t.Errorf("the checkup ran for a verb that is not one:\n%s", stdout)
	}
}

func TestDoctorHelpListsTheTwoVerbsAndTheBareForm(t *testing.T) {
	stdout, _, status := runTree("doctor", "--help")
	if status != 0 {
		t.Fatalf("--help exited %d", status)
	}
	for _, want := range []string{"explain", "codes", bareHelpWord + "\n", "checkup"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("`drift doctor --help` does not mention %q:\n%s", want, stdout)
		}
	}
}

// Every code any check can carry is registered, and the fix beside it, which is the
// registry's and never the check's, is at most three words and names a command.
func TestEveryCodeACheckCanCarryIsRegisteredWithAFixThatResolves(t *testing.T) {
	root := newRootCmd("test")
	seen := 0
	for _, check := range doctor.Checklist() {
		for _, result := range check.Possible() {
			seen++
			if err := result.Validate(); err != nil {
				t.Errorf("%s: %+v: %v", check.Name, result, err)
			}
			if result.Code == "" {
				continue
			}
			entry, ok := common.LookupErrorCode(result.Code)
			if !ok {
				t.Errorf("%s carries %s, which is not registered", check.Name, result.Code)
				continue
			}
			if entry.Command == "" {
				continue
			}
			if n := len(strings.Fields(entry.Command)); n > 3 {
				t.Errorf("%s: the fix for %s is %q, %d words", check.Name, result.Code, entry.Command, n)
			}
			if problem := unresolved(root, entry.Command); problem != "" {
				t.Errorf("%s: the fix for %s is %q, %s", check.Name, result.Code, entry.Command, problem)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no check lists a possible result, so nothing was walked")
	}
}

// The control for the walk: a result carrying a code nobody registered is caught.
func TestTheWalkCatchesAnUnregisteredCode(t *testing.T) {
	r := doctor.Result{Status: doctor.StatusProblem, Sentence: "x", Code: "DRIFT-9999"}
	if r.Validate() == nil {
		t.Error("an unregistered code passed Validate, so the walk above cannot fail")
	}
}
