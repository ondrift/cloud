package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// `drift doctor` is what someone runs when the network, the platform or their
// credentials are the thing that failed, so everything under it answers with no
// session and no network. These tests run the real command tree with an empty
// HOME, with every HTTP request failing the test, and read what the command
// printed through the writer the tree hands it.

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// noNetwork leaves the test an empty HOME, plain output, and no way to make an
// HTTP request. The API and the status feed point at a server that reports every
// request it receives; a request to any other host is refused and reported the
// same way, because every client in the CLI goes through the default transport.
// It returns how many requests were made, each also passed to errorf.
func noNetwork(t *testing.T, errorf func(format string, args ...any)) *atomic.Int32 {
	t.Helper()
	isolate(t)
	t.Setenv("NO_COLOR", "1")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		errorf("the platform was called: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	apiBase, statusURL := common.APIBaseURL, common.StatusURL
	common.APIBaseURL, common.StatusURL = srv.URL, srv.URL
	t.Cleanup(func() { common.APIBaseURL, common.StatusURL = apiBase, statusURL })

	transport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == srv.Listener.Addr().String() {
			return transport.RoundTrip(r)
		}
		requests.Add(1)
		errorf("a request left for %s", r.URL.Host)
		return nil, errors.New("this test has no network")
	})
	t.Cleanup(func() { http.DefaultTransport = transport })

	return &requests
}

// runTree runs the real command tree and returns what the command wrote to
// standard output, what the process would write to standard error, and the
// status it would exit with.
func runTree(args ...string) (stdout, stderr string, status int) {
	var out, errOut bytes.Buffer
	root := newRootCmd("test")
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	status = run(root, &errOut)
	return out.String(), errOut.String(), status
}

// squash puts every run of whitespace on one line as a single space, so a text
// that was wrapped to the terminal's width compares equal to the text it came
// from.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// The control for noNetwork. "No request arrived" proves nothing about a trap
// that cannot see one, so this makes a request to each place the doctor must not
// reach and expects the trap to count every one.
func TestTheNoNetworkTrapSeesARequestToAnyHost(t *testing.T) {
	var mu sync.Mutex
	var reported []string
	requests := noNetwork(t, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, fmt.Sprintf(format, args...))
	})

	for _, url := range []string{
		common.APIBaseURL + "/ops/slice/status",
		common.StatusFeedURL(),
		"https://api.github.com/repos/ondrift/cloud/releases/latest",
	} {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if got := requests.Load(); got != 3 {
		t.Errorf("the trap counted %d requests, want 3 (reported: %q)", got, reported)
	}
	if len(reported) != 3 {
		t.Errorf("the trap reported %d requests, want 3: %q", len(reported), reported)
	}
}

// Every registered code is explained with no session and no network: the meaning,
// the remedy in full, and the command that fixes it or the statement that there
// is none, on standard output, with nothing on standard error and no request.
func TestDoctorExplainAnswersOfflineForEveryRegisteredCode(t *testing.T) {
	codes := common.ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so the loop below would prove nothing")
	}
	requests := noNetwork(t, t.Errorf)

	for _, c := range codes {
		t.Run(c.Code, func(t *testing.T) {
			stdout, stderr, status := runTree("doctor", "explain", c.Code)
			if status != 0 || stderr != "" {
				t.Fatalf("explain %s exited %d with stderr %q", c.Code, status, stderr)
			}

			got := squash(stdout)
			for _, field := range []struct{ what, text string }{
				{"the code", c.Code},
				{"the meaning", c.Meaning},
				{"the remedy", c.Remedy},
			} {
				if !strings.Contains(got, squash(field.text)) {
					t.Errorf("explain %s does not print %s %q; it printed:\n%s", c.Code, field.what, field.text, stdout)
				}
			}

			if c.Command != "" {
				if want := "fix: " + c.Command; !strings.Contains(got, want) {
					t.Errorf("explain %s does not print %q; it printed:\n%s", c.Code, want, stdout)
				}
			} else if want := "There is no command you can run for this one."; !strings.Contains(got, want) {
				t.Errorf("explain %s has no command and does not say so (%q); it printed:\n%s", c.Code, want, stdout)
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("%d request(s) arrived while explaining %d codes, want none", n, len(codes))
	}
}

// `drift doctor codes` lists every registered code with its meaning, offline.
func TestDoctorCodesListsEveryRegisteredCodeOffline(t *testing.T) {
	codes := common.ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so the loop below would prove nothing")
	}
	requests := noNetwork(t, t.Errorf)

	stdout, stderr, status := runTree("doctor", "codes")
	if status != 0 || stderr != "" {
		t.Fatalf("codes exited %d with stderr %q", status, stderr)
	}

	if want := fmt.Sprintf("%d failure codes", len(codes)); !strings.Contains(stdout, want) {
		t.Errorf("codes does not say %q; it printed:\n%s", want, stdout)
	}
	lines := strings.Split(stdout, "\n")
	for _, c := range codes {
		listed := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), c.Code) && strings.Contains(line, c.Meaning) {
				listed = true
			}
		}
		if !listed {
			t.Errorf("codes lists no line for %s with its meaning %q; it printed:\n%s", c.Code, c.Meaning, stdout)
		}
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("%d request(s) arrived while listing the codes, want none", n)
	}
}

// highestCodeNumber is the number of the highest registered code.
func highestCodeNumber(t *testing.T) int {
	t.Helper()
	highest := 0
	for _, c := range common.ErrorCodes() {
		var n int
		if _, err := fmt.Sscanf(c.Code, "DRIFT-%d", &n); err != nil {
			t.Fatalf("%q is not shaped like a code: %v", c.Code, err)
		}
		if n > highest {
			highest = n
		}
	}
	if highest == 0 {
		t.Fatal("the registry is empty")
	}
	return highest
}

// One code typed three ways is one question, so it gets one answer, and the
// answer is the registry's for that code rather than an empty one.
func TestDoctorExplainTreatsEverySpellingOfACodeAlike(t *testing.T) {
	noNetwork(t, t.Errorf)
	want, ok := common.LookupErrorCode("DRIFT-1006")
	if !ok {
		t.Fatal("DRIFT-1006 is not registered")
	}

	canonical, stderr, status := runTree("doctor", "explain", "DRIFT-1006")
	if status != 0 || stderr != "" {
		t.Fatalf("explain DRIFT-1006 exited %d with stderr %q", status, stderr)
	}
	if !strings.Contains(canonical, want.Meaning) {
		t.Fatalf("explain DRIFT-1006 does not print its meaning %q; it printed:\n%s", want.Meaning, canonical)
	}

	for _, spelling := range []string{"drift-1006", "Drift-1006", "1006", " 1006 "} {
		got, stderr, status := runTree("doctor", "explain", spelling)
		if status != 0 || stderr != "" {
			t.Errorf("explain %q exited %d with stderr %q", spelling, status, stderr)
			continue
		}
		if got != canonical {
			t.Errorf("explain %q printed:\n%s\nwant what DRIFT-1006 prints:\n%s", spelling, got, canonical)
		}
	}
}

// A code numbered above every registered one is what a newer drift prints and
// this binary has never heard of. The answer still says nothing is registered
// under it, and adds that this binary may be the older one and what fixes that.
func TestDoctorExplainSaysWhenACodeMayComeFromANewerDrift(t *testing.T) {
	noNetwork(t, t.Errorf)
	highest := highestCodeNumber(t)

	for _, tc := range []struct{ typed, code string }{
		{fmt.Sprintf("DRIFT-%d", highest+1), fmt.Sprintf("DRIFT-%d", highest+1)},
		{fmt.Sprintf("drift-%d", highest+1), fmt.Sprintf("DRIFT-%d", highest+1)},
		{fmt.Sprintf("%d", highest+1), fmt.Sprintf("DRIFT-%d", highest+1)},
		{fmt.Sprintf(" %d ", highest+250), fmt.Sprintf("DRIFT-%d", highest+250)},
	} {
		t.Run(tc.typed, func(t *testing.T) {
			stdout, stderr, status := runTree("doctor", "explain", tc.typed)
			if status != 1 || stdout != "" {
				t.Fatalf("explain %q exited %d with stdout %q, want a failure with nothing on stdout", tc.typed, status, stdout)
			}
			for _, want := range []string{
				fmt.Sprintf("no failure is registered under %q.", tc.typed),
				"`drift doctor codes` lists every one this binary knows.",
				fmt.Sprintf("knows codes up to DRIFT-%d", highest),
				fmt.Sprintf("may be older than %s", tc.code),
				"`drift upgrade`",
			} {
				if !strings.Contains(stderr, want) {
					t.Errorf("explain %q does not say %q; it said:\n%s", tc.typed, want, stderr)
				}
			}
		})
	}
}

// notRegisteredMessage is what explain says, on standard error, for text that
// names no registered failure.
const notRegisteredMessage = "no failure is registered under %q.\n" +
	"  Codes look like DRIFT-1006. `drift doctor codes` lists every one this binary knows.\n"

// Text that is not a code, and a number below every code this binary registers,
// keep the message they always had. Nothing newer is claimed for them: a newer
// drift cannot have minted a number below the first.
func TestDoctorExplainKeepsItsMessageForTextThatIsNotACode(t *testing.T) {
	noNetwork(t, t.Errorf)

	for _, text := range []string{
		"banana",
		"",
		"DRIFT-",
		"DRIFT-ABC",
		"1006x",
		"+9999",
		"99999999999999999999",
		"DRIFT-1000",
		"0",
	} {
		stdout, stderr, status := runTree("doctor", "explain", text)
		if status != 1 || stdout != "" {
			t.Errorf("explain %q exited %d with stdout %q, want a failure with nothing on stdout", text, status, stdout)
		}
		if want := fmt.Sprintf(notRegisteredMessage, text); stderr != want {
			t.Errorf("explain %q said:\n%s\nwant:\n%s", text, stderr, want)
		}
	}
}
