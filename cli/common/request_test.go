package common

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// Telling an expired session apart from a refusal the platform MEANT.
//
// Both are 401. The first is fixed by refreshing the token and re-sending; the
// second is the platform's answer to the request, and re-sending it answers a
// question nobody asked — while spending again whatever single-use credential
// the request carried.

// resp401 builds a 401 whose body is what the platform sent.
func resp401(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// THE TWO REFUSALS ARE RECOGNISED, by their machine-readable key rather than by
// the sentence beside it.
//
// This is the case that cost a rehearsal: a snapshot download spends its
// step-up grant on the attempt that carries it, the operator then refuses for a
// wrong passphrase, and a blind retry re-sent the SPENT grant. The user was
// told their proof of identity had already been used, which is true and is not
// the reason, and the real refusal never reached them.
func TestIsApplicationRefusal_RecognisesWhatThePlatformMeant(t *testing.T) {
	cases := map[string]string{
		"a step-up request": `{"error":"this action needs a fresh proof of identity","step_up_required":"snapshot.download"}`,
		"a wrong passphrase": `{"error":"that passphrase does not open this snapshot",` +
			`"passphrase_required":"abc","reason":"wrong_passphrase"}`,
		"a missing passphrase": `{"error":"this snapshot was created with a passphrase",` +
			`"passphrase_required":"abc","reason":"passphrase_required"}`,
	}
	for name, body := range cases {
		if !isApplicationRefusal(resp401(body)) {
			t.Errorf("%s: read as an expired session, so it would be retried and the reason lost", name)
		}
	}
}

// AN ORDINARY 401 IS STILL AN ORDINARY 401. An access token that aged out is
// the common case by a wide margin, and it is fixed invisibly by a refresh — a
// change that stopped refreshing would send every user to `drift account login`
// fifteen minutes into a session.
func TestIsApplicationRefusal_LeavesAnExpiredSessionAlone(t *testing.T) {
	for name, body := range map[string]string{
		"an expired token":   `{"error":"token expired"}`,
		"not JSON at all":    `Unauthorized`,
		"empty":              ``,
		"a JSON array":       `[{"step_up_required":"x"}]`,
		"the key but empty":  `{"step_up_required":""}`,
		"a neighbouring key": `{"step_up":"snapshot.download"}`,
	} {
		if isApplicationRefusal(resp401(body)) {
			t.Errorf("%s: read as a platform refusal, so an expired session would never refresh", name)
		}
	}
}

// THE BODY SURVIVES THE PEEK, on both paths.
//
// The caller reads it on every route out — to find the purpose a step-up names,
// to find which passphrase failure this is, or to print the platform's reason.
// A check that consumed the body would leave every one of those reading an
// empty string, and each would then report the wrong thing confidently.
func TestIsApplicationRefusal_RestoresTheBodyEitherWay(t *testing.T) {
	for name, body := range map[string]string{
		"a refusal":         `{"error":"needs a passphrase","passphrase_required":"abc"}`,
		"an expired 401":    `{"error":"token expired"}`,
		"not JSON":          `Unauthorized`,
		"a very long value": `{"error":"` + strings.Repeat("x", 4096) + `","step_up_required":"p"}`,
	} {
		resp := resp401(body)
		isApplicationRefusal(resp)

		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("%s: reading the restored body failed: %v", name, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s: the body did not survive the peek\n got: %q\nwant: %q", name, got, body)
		}
	}
}

// A body past the peek's limit is still served whole. The limit bounds how much
// is INSPECTED, not how much the caller can read — and a response big enough to
// hit it is one the caller most needs intact.
func TestIsApplicationRefusal_RestoresABodyLargerThanThePeek(t *testing.T) {
	body := strings.Repeat("y", 32<<10)
	resp := resp401(body)
	isApplicationRefusal(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the restored body failed: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("the body was truncated to %d bytes, want %d", len(got), len(body))
	}
	if string(got) != body {
		t.Error("the body past the peek's limit did not come back verbatim")
	}
}

// The api states which route contract it speaks, in X-Drift-API-Version on every
// control-plane reply, and the client decides what to do with it. Only the MAJOR
// decides: a platform ahead of this binary means paths this binary knows may have
// moved, so it says so once; the same major, an older one, or no statement at all
// says nothing. Nothing is blocked or retried on the strength of the comparison.
//
// "Once per command" is a property of a process, so each scenario runs DoRequest
// in a fresh child of this test binary and reads what that process wrote to its
// own stderr.

// routeContractHelperEnv turns TestRouteContractHelperProcess into the child; its
// value is the stub api's address.
const routeContractHelperEnv = "DRIFT_TEST_ROUTE_CONTRACT_API"

// TestRouteContractHelperProcess is the child of the tests below. Outside a child
// run it skips.
func TestRouteContractHelperProcess(t *testing.T) {
	api := os.Getenv(routeContractHelperEnv)
	if api == "" {
		t.Skip("runs only as the child of the route-contract tests")
	}
	APIBaseURL = api
	if err := SaveSession("access", "refresh"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	for i := 1; i <= 3; i++ {
		resp, err := DoRequest(http.MethodGet, api+"/ops/slice", nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("RESULT %d %s\n", resp.StatusCode, body)
	}
}

// askThreeTimes runs three requests through DoRequest, in a child process, against
// a stub api that answers each with status and body and, when stated is not empty,
// with X-Drift-API-Version: stated. It returns the results the child saw, what the
// child wrote to stderr, and how many requests the stub received.
func askThreeTimes(t *testing.T, stated string, status int, body string) (results []string, stderr string, served int) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/slice" {
			// The schema refresh the first request of every process makes.
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		seen++
		mu.Unlock()
		if stated != "" {
			w.Header().Set("X-Drift-API-Version", stated)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())
	t.Setenv(TokenEnv, "")

	child := exec.Command(os.Args[0], "-test.run=^TestRouteContractHelperProcess$", "-test.count=1")
	child.Env = append(os.Environ(), routeContractHelperEnv+"="+srv.URL)
	var out, errOut strings.Builder
	child.Stdout, child.Stderr = &out, &errOut
	if err := child.Run(); err != nil {
		t.Fatalf("the child process failed: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
	}

	for _, line := range strings.Split(out.String(), "\n") {
		if r, ok := strings.CutPrefix(line, "RESULT "); ok {
			results = append(results, r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return results, errOut.String(), seen
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// requireUnchanged fails unless every request reached the stub exactly once and
// the caller saw exactly what the stub answered: the comparison neither blocks
// nor retries nor rewrites a response.
func requireUnchanged(t *testing.T, results []string, served, status int, body string) {
	t.Helper()
	if served != 3 {
		t.Errorf("the stub received %d requests for 3 calls: the comparison blocked or retried one", served)
	}
	want := fmt.Sprintf("%d %s", status, body)
	if len(results) != 3 {
		t.Fatalf("the child reported %d results, want 3: %q", len(results), results)
	}
	for i, got := range results {
		if got != want {
			t.Errorf("call %d returned %q, want %q: the response must reach the caller untouched", i+1, got, want)
		}
	}
}

// A platform a major ahead is said once per process, on stderr, whatever the
// reply: a client holding the wrong route set sees 404s and refusals as often as
// successes, and the header rides all of them.
func TestAPlatformAheadOfTheCLIIsSaidOnceAndNothingIsChanged(t *testing.T) {
	for name, tc := range map[string]struct {
		stated string
		status int
		body   string
	}{
		"a success":            {"2", http.StatusOK, `{"slices":[]}`},
		"a missing route":      {"2", http.StatusNotFound, `{"error":"not found"}`},
		"a platform fault":     {"2", http.StatusInternalServerError, `{"error":"db down"}`},
		"several majors ahead": {"7", http.StatusOK, `{"slices":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			results, stderr, served := askThreeTimes(t, tc.stated, tc.status, tc.body)

			lines := nonEmptyLines(stderr)
			if len(lines) != 1 {
				t.Fatalf("three requests to a platform at API version %s wrote %d lines to stderr, want exactly 1:\n%s",
					tc.stated, len(lines), stderr)
			}
			for _, want := range []string{
				"older than the platform",
				"API version 1",
				"the platform speaks " + tc.stated,
				"`drift upgrade`",
			} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("the line does not say %q:\n%s", want, lines[0])
				}
			}
			requireUnchanged(t, results, served, tc.status, tc.body)
		})
	}
}

// THE CONTROLS: the same major, an older one, no statement and a statement that is
// not a number all say nothing. A newer CLI against an older platform is fine, and
// a platform that does not state a version is unknown, which is never a reason to
// speak.
func TestAPlatformNotAheadOfTheCLISaysNothing(t *testing.T) {
	for name, stated := range map[string]string{
		"the same major": "1",
		"an older major": "0",
		"no statement":   "",
		"not a number":   "latest",
	} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound} {
			t.Run(fmt.Sprintf("%s, %d", name, status), func(t *testing.T) {
				const body = `{"error":"not found"}`
				results, stderr, served := askThreeTimes(t, stated, status, body)

				if strings.TrimSpace(stderr) != "" {
					t.Errorf("a platform stating %q wrote to stderr:\n%s", stated, stderr)
				}
				requireUnchanged(t, results, served, status, body)
			})
		}
	}
}
