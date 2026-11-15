package slice

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A COMMAND THAT REPORTS AN HTTP FAILURE REPORTS IT THROUGH THE ONE RENDERER.
//
// What a person reads when a command fails is the error it returns: main prints
// it once, to stderr, and exits 1. Each test here runs a real command against a
// stub of the platform and reads that error, so it holds what is printed and the
// exit status, and says nothing about how the code gets there.
//
// The renderer is common.APIError. It is the only thing that gives a failure its
// code line, the note that a 5xx is the platform's and not the caller's, a
// quota's numbers and a throttle's wait. A command that reads the status of an
// answer and writes its own sentence has none of them.

const (
	// apiErrDegradedFeed is the platform's status feed with one component down.
	apiErrDegradedFeed = `[{"name":"API","status":"operational"},{"name":"Routing","status":"degraded"}]`

	// apiErrFaultNote is the line the renderer adds to a 5xx when the feed names
	// a component that is down.
	apiErrFaultNote = "Routing is not operational right now"
)

// apiErrStatusFeed points the health check at a feed with Routing down and
// returns how many times the feed has been read.
func apiErrStatusFeed(t *testing.T) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(apiErrDegradedFeed))
	}))
	t.Cleanup(srv.Close)
	previous := common.StatusURL
	common.StatusURL = srv.URL
	t.Cleanup(func() { common.StatusURL = previous })
	return &reads
}

// apiErrCapture runs fn with stdout redirected and returns what it printed.
func apiErrCapture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w

	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	fn()
	os.Stdout = previous
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// apiErrAnswer is what the stub platform says to one request.
type apiErrAnswer struct {
	status  int
	body    string
	headers map[string]string
}

// apiErrReply is a route that always answers the same way.
func apiErrReply(status int, body string) func(*http.Request) apiErrAnswer {
	return func(*http.Request) apiErrAnswer { return apiErrAnswer{status: status, body: body} }
}

// apiErrCalls records the requests the stub platform received, each as
// "METHOD /path?query".
type apiErrCalls struct {
	mu    sync.Mutex
	lines []string
}

func (c *apiErrCalls) record(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

// count is how many requests began with prefix.
func (c *apiErrCalls) count(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, line := range c.lines {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// apiErrPlatform stands up a stub platform with a saved session and an active
// slice, as stubLinkAPI does. A route is "METHOD /path"; a request for any other
// route is answered 404 with no body.
func apiErrPlatform(t *testing.T, routes map[string]func(*http.Request) apiErrAnswer) *apiErrCalls {
	t.Helper()
	calls := &apiErrCalls{}
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.record(r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery)
		answer := apiErrAnswer{status: http.StatusNotFound}
		if route, ok := routes[r.Method+" "+r.URL.Path]; ok {
			answer = route(r)
		}
		for k, v := range answer.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(answer.status)
		_, _ = w.Write([]byte(answer.body))
	})
	return calls
}

// apiErrTypePassword answers the password prompt, which would otherwise wait on
// a terminal.
func apiErrTypePassword(t *testing.T, password string) {
	t.Helper()
	previous := readPassword
	readPassword = func(string) string { return password }
	t.Cleanup(func() { readPassword = previous })
}

// apiErrRun runs `drift slice <args>` and returns what it printed to stdout and
// the error it returned, which is what main prints to stderr.
func apiErrRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	cmd := GetCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var err error
	out := apiErrCapture(t, func() { err = cmd.Execute() })
	return out, err
}

// apiErrCodeLine is the line the renderer puts under a failure that has a code.
func apiErrCodeLine(code string) string {
	return code + " · drift doctor explain " + code
}

// apiErrContains reports each of wants that got does not hold.
func apiErrContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not say %q. It reads:\n%s", want, got)
		}
	}
}

// apiErrAbsent reports each of unwanted that got holds.
func apiErrAbsent(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(got, bad) {
			t.Errorf("the failure says %q, which it must not. It reads:\n%s", bad, got)
		}
	}
}

// apiErrFailure holds what every failure here has in common: a failure the
// renderer wrote, with its lead-in, its reason and its code line, and the note
// about the platform on a 5xx and on nothing else.
func apiErrFailure(t *testing.T, got error, reads *atomic.Int32, status int, lead, reason, code string) {
	t.Helper()
	if got == nil {
		t.Fatalf("the platform answered %d and the command returned no error, so the process exits 0", status)
	}
	msg := got.Error()
	apiErrContains(t, msg, lead, reason, apiErrCodeLine(code))
	if status >= 500 {
		apiErrContains(t, msg, apiErrFaultNote)
		return
	}
	apiErrAbsent(t, msg, "not your code")
	if n := reads.Load(); n != 0 {
		t.Errorf("a %d is the platform telling the caller something true, and the failure read the platform's health %d time(s)", status, n)
	}
}

// ─── drift slice domain remove ──────────────────────────────────────────────

func TestDomainRemove_AFailureIsReturnedThroughTheRenderer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
		code   string
	}{
		{"the domain is not on the slice", http.StatusNotFound,
			`{"error":"domain is not registered on this slice"}`,
			"domain is not registered on this slice", "DRIFT-1003"},
		{"the domain cannot be removed yet", http.StatusConflict,
			`{"error":"the certificate for this domain is still being issued"}`,
			"the certificate for this domain is still being issued", "DRIFT-1013"},
		{"the platform fails", http.StatusInternalServerError,
			`{"error":"could not remove the route"}`,
			"could not remove the route", "DRIFT-1005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := apiErrStatusFeed(t)
			platform := apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
				"DELETE /ops/slice/domain": apiErrReply(tc.status, tc.body),
			})

			out, err := apiErrRun(t, "domain", "remove", "forms.example")

			apiErrFailure(t, err, reads, tc.status, "Couldn't remove domain", tc.reason, tc.code)
			if got := platform.count("DELETE /ops/slice/domain?host=forms.example"); got != 1 {
				t.Errorf("the platform saw %d removals of forms.example, want 1", got)
			}
			// Said once, by main, on stderr: not also here, and no success line
			// above a failure.
			apiErrAbsent(t, out, "removed", "Couldn't")
		})
	}
}

// The control: a removal the platform accepts still says so.
func TestDomainRemove_ASuccessStillSaysRemoved(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK} {
		apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
			"DELETE /ops/slice/domain": apiErrReply(status, ``),
		})
		out, err := apiErrRun(t, "domain", "remove", "forms.example")
		if err != nil {
			t.Fatalf("the platform answered %d and the removal failed: %v", status, err)
		}
		apiErrContains(t, out, "Domain forms.example removed.")
	}
}

// ─── drift slice snapshot delete ────────────────────────────────────────────

func TestSnapshotDelete_AFailureIsReturnedThroughTheRenderer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
		code   string
	}{
		{"there is no such snapshot", http.StatusNotFound,
			`{"error":"snapshot not found"}`, "snapshot not found", "DRIFT-1003"},
		{"the platform fails", http.StatusInternalServerError,
			`{"error":"the archive store did not answer"}`, "the archive store did not answer", "DRIFT-1005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := apiErrStatusFeed(t)
			platform := apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
				"DELETE /ops/slice/snapshot": apiErrReply(tc.status, tc.body),
			})

			out, err := apiErrRun(t, "snapshot", "delete", "snap-1", "--yes")

			apiErrFailure(t, err, reads, tc.status, "Couldn't delete snapshot", tc.reason, tc.code)
			apiErrAbsent(t, err.Error(), "delete failed")
			if got := platform.count("DELETE /ops/slice/snapshot?id=snap-1"); got != 1 {
				t.Errorf("the platform saw %d deletions of snap-1, want 1", got)
			}
			apiErrAbsent(t, out, "deleted")
		})
	}
}

// The control: a deletion the platform accepts still says so.
func TestSnapshotDelete_ASuccessStillSaysDeleted(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK} {
		apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
			"DELETE /ops/slice/snapshot": apiErrReply(status, ``),
		})
		out, err := apiErrRun(t, "snapshot", "delete", "snap-1", "--yes")
		if err != nil {
			t.Fatalf("the platform answered %d and the deletion failed: %v", status, err)
		}
		apiErrContains(t, out, "Snapshot snap-1 deleted.")
	}
}

// ─── drift slice snapshot download ──────────────────────────────────────────

const (
	// apiErrStepUpRefusal is how the platform asks for a fresh proof of identity.
	apiErrStepUpRefusal = `{"error":"this action needs a fresh proof of identity","step_up_required":"snapshot.download"}`

	// apiErrGrant is the single-use grant the stub platform mints.
	apiErrGrant = "grant-1"
)

// apiErrDownloadPlatform serves the routes a download calls. The status route
// says the archive is not passphrase protected. With gated, the download asks for
// a proof of identity until it carries a grant, and the grant is minted for the
// password that was typed; answer is what the download says once nothing more is
// asked of the caller, and is given the grant the request carried.
func apiErrDownloadPlatform(t *testing.T, gated bool, answer func(grant string) apiErrAnswer) *apiErrCalls {
	t.Helper()
	return apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
		"GET /ops/slice/snapshot/status": apiErrReply(http.StatusOK, `{"id":"snap-1","passphrase_protected":false}`),
		"POST /ops/account/stepup":       apiErrReply(http.StatusOK, `{"grant":"`+apiErrGrant+`"}`),
		"GET /ops/slice/snapshot/download": func(r *http.Request) apiErrAnswer {
			grant := r.Header.Get(stepUpHeader)
			if gated && grant == "" {
				return apiErrAnswer{status: http.StatusUnauthorized, body: apiErrStepUpRefusal}
			}
			return answer(grant)
		},
	})
}

// The download is asked for a proof of identity first and fails once it has one:
// the failure the person reads is the renderer's.
func TestSnapshotDownload_AFailureAfterTheProofIsReturnedThroughTheRenderer(t *testing.T) {
	reads := apiErrStatusFeed(t)
	apiErrTypePassword(t, "the-account-password")
	platform := apiErrDownloadPlatform(t, true, func(grant string) apiErrAnswer {
		if grant != apiErrGrant {
			return apiErrAnswer{status: http.StatusBadRequest, body: `{"error":"the grant did not arrive"}`}
		}
		return apiErrAnswer{status: http.StatusInternalServerError, body: `{"error":"the archive could not be read"}`}
	})

	out, err := apiErrRun(t, "snapshot", "download", "snap-1", "-o", filepath.Join(t.TempDir(), "out.tar.gz"))

	apiErrFailure(t, err, reads, http.StatusInternalServerError,
		"Couldn't download snapshot", "the archive could not be read", "DRIFT-1005")
	apiErrAbsent(t, err.Error(), "download failed")
	if got := platform.count("POST /ops/account/stepup"); got != 1 {
		t.Errorf("the proof of identity was minted %d times, want 1", got)
	}
	apiErrAbsent(t, out, "Snapshot downloaded")
}

// A failure on the first request, before any proof is asked for: a platform that
// does not gate the download, or a failure ahead of the gate. The platform's own
// reason is in the failure, which is the body of THAT answer.
func TestSnapshotDownload_AFailureOnTheFirstRequestKeepsThePlatformsReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
		code   string
	}{
		{"there is no such snapshot", http.StatusNotFound,
			`{"error":"snapshot not found"}`, "snapshot not found", "DRIFT-1003"},
		{"the platform fails", http.StatusInternalServerError,
			`{"error":"the archive could not be read"}`, "the archive could not be read", "DRIFT-1005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := apiErrStatusFeed(t)
			apiErrDownloadPlatform(t, false, func(string) apiErrAnswer {
				return apiErrAnswer{status: tc.status, body: tc.body}
			})

			_, err := apiErrRun(t, "snapshot", "download", "snap-1", "-o", filepath.Join(t.TempDir(), "out.tar.gz"))

			apiErrFailure(t, err, reads, tc.status, "Couldn't download snapshot", tc.reason, tc.code)
		})
	}
}

// A refusal of the proof of identity the download carried is the platform's
// answer to that proof. It is not a session that ran out.
func TestSnapshotDownload_ARefusedProofIsNotASessionExpiry(t *testing.T) {
	apiErrStatusFeed(t)
	apiErrTypePassword(t, "the-account-password")
	apiErrDownloadPlatform(t, true, func(string) apiErrAnswer {
		return apiErrAnswer{status: http.StatusUnauthorized, body: `{"error":"that proof of identity was not accepted; ` +
			`it may have been used already or expired","step_up_required":"snapshot.download"}`}
	})

	_, err := apiErrRun(t, "snapshot", "download", "snap-1", "-o", filepath.Join(t.TempDir(), "out.tar.gz"))

	if err == nil {
		t.Fatal("the platform refused the proof and the command returned no error")
	}
	apiErrContains(t, err.Error(), "Couldn't download snapshot", "that proof of identity was not accepted")
	apiErrAbsent(t, err.Error(), "session expired", "drift account login", "DRIFT-1001")
}

// The control: a passphrase refusal keeps its own sentence, and carries no code,
// because the person fixes it themselves.
func TestSnapshotDownload_APassphraseRefusalKeepsItsOwnSentence(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{"wrong_passphrase", "that passphrase does not open snapshot snap-1"},
		{"passphrase_required", "snapshot snap-1 was created with a passphrase, and it is needed to download it"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			apiErrStatusFeed(t)
			apiErrTypePassword(t, "the-account-password")
			apiErrDownloadPlatform(t, true, func(string) apiErrAnswer {
				return apiErrAnswer{status: http.StatusUnauthorized,
					body: `{"error":"the passphrase is needed","passphrase_required":"snap-1","reason":"` + tc.reason + `"}`}
			})

			_, err := apiErrRun(t, "snapshot", "download", "snap-1", "-o", filepath.Join(t.TempDir(), "out.tar.gz"))

			if err == nil {
				t.Fatal("the platform refused the passphrase and the command returned no error")
			}
			apiErrContains(t, err.Error(), tc.want)
			apiErrAbsent(t, err.Error(), "DRIFT-", "session expired", "Couldn't download snapshot")
		})
	}
}

// The control: an archive the platform serves without asking for a proof arrives
// whole, however large it is.
func TestSnapshotDownload_AnArchiveServedWithoutAProofArrivesWhole(t *testing.T) {
	archive := bytes.Repeat([]byte("0123456789abcdef"), 2500)
	apiErrDownloadPlatform(t, false, func(string) apiErrAnswer {
		return apiErrAnswer{status: http.StatusOK, body: string(archive)}
	})
	path := filepath.Join(t.TempDir(), "out.tar.gz")

	out, err := apiErrRun(t, "snapshot", "download", "snap-1", "-o", path)

	if err != nil {
		t.Fatalf("the download failed: %v", err)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("no archive was written: %v", rerr)
	}
	if !bytes.Equal(got, archive) {
		t.Errorf("the archive on disk is %d bytes and differs from the %d the platform sent", len(got), len(archive))
	}
	apiErrContains(t, out, "Snapshot downloaded to "+path)
}

// ─── drift slice resize ─────────────────────────────────────────────────────

// A resize answered with anything but its two questions fails through the
// renderer, on the form and from a file alike: both go through postResize.
var apiErrResizeRefusals = []struct {
	name    string
	status  int
	headers map[string]string
	body    string
	code    string
	says    []string
}{
	{"a quota", http.StatusPaymentRequired, nil,
		`{"error":"this shape needs more functions than the plan allows","action":"atomic.functions","limit":3,"current":3}`,
		"DRIFT-1004", []string{"this shape needs more functions than the plan allows", "atomic.functions: 3 of 3 used."}},
	{"a refusal", http.StatusForbidden, nil,
		`{"error":"only the owner can resize this slice"}`,
		"DRIFT-1002", []string{"only the owner can resize this slice"}},
	{"a throttle", http.StatusTooManyRequests, map[string]string{"Retry-After": "30"},
		`{"error":"too many control-plane calls this minute"}`,
		"DRIFT-1014", []string{"too many control-plane calls this minute", "try again in 30 seconds"}},
	{"a platform fault", http.StatusInternalServerError, nil,
		`{"error":"the shape could not be applied"}`,
		"DRIFT-1005", []string{"the shape could not be applied"}},
}

func TestPostResize_AnswersOtherThanTheQuestionAreFailuresThroughTheRenderer(t *testing.T) {
	for _, tc := range apiErrResizeRefusals {
		t.Run(tc.name, func(t *testing.T) {
			reads := apiErrStatusFeed(t)
			apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
				"POST /ops/slice/resize": func(*http.Request) apiErrAnswer {
					return apiErrAnswer{status: tc.status, headers: tc.headers, body: tc.body}
				},
			})

			ok, refusal, err := postResize(map[string]any{"name": "lab", "config": map[string]any{}})

			if ok || refusal != nil {
				t.Fatalf("a %d was read as a success or as a question: ok=%v refusal=%v", tc.status, ok, refusal)
			}
			apiErrFailure(t, err, reads, tc.status, "Couldn't resize slice", tc.says[0], tc.code)
			apiErrContains(t, err.Error(), tc.says...)
			apiErrAbsent(t, err.Error(), "resize refused")
		})
	}
}

// The control: the 409 that asks a question is still handed back as a question,
// with no error, so the form can draw its confirmation.
func TestPostResize_TheQuestionStillComesBackAsARefusal(t *testing.T) {
	apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
		"POST /ops/slice/resize": apiErrReply(http.StatusConflict, bothRefusal),
	})

	ok, refusal, err := postResize(map[string]any{"name": "lab", "config": map[string]any{}})

	if ok || err != nil || refusal == nil {
		t.Fatalf("postResize = (%v, %v, %v), want the question decoded and no error", ok, refusal, err)
	}
	if !refusal.asksPrice() || !refusal.asksRemoval() {
		t.Errorf("the refusal lost a question: asksPrice=%v asksRemoval=%v", refusal.asksPrice(), refusal.asksRemoval())
	}
}

func TestResizeConfig_AFailureIsReturnedThroughTheRenderer(t *testing.T) {
	for _, tc := range apiErrResizeRefusals {
		if tc.status != http.StatusPaymentRequired && tc.status != http.StatusInternalServerError {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			reads := apiErrStatusFeed(t)
			apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
				"POST /ops/slice/resize": func(*http.Request) apiErrAnswer {
					return apiErrAnswer{status: tc.status, headers: tc.headers, body: tc.body}
				},
			})

			_, err := apiErrRun(t, "resize", "lab", "--config", shapeFile(t))

			apiErrFailure(t, err, reads, tc.status, "Couldn't resize slice", tc.says[0], tc.code)
			apiErrContains(t, err.Error(), tc.says...)
			apiErrAbsent(t, err.Error(), "resize refused")
		})
	}
}

// ─── the step-up password ───────────────────────────────────────────────────

// apiErrStepUpPlatform answers the proof-of-identity route with status and body.
// A 401 that names no step-up key is read as an expired session first, so the
// platform's refresh route answers too.
func apiErrStepUpPlatform(t *testing.T, status int, body string) *apiErrCalls {
	t.Helper()
	return apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
		"POST /ops/account/stepup": apiErrReply(status, body),
		"POST /refresh":            apiErrReply(http.StatusOK, `{"access_token":"fresh-access","refresh_token":"fresh-refresh"}`),
	})
}

// apiErrMint asks for a grant, with the password typed as typed.
func apiErrMint(t *testing.T, typed string) (string, error) {
	t.Helper()
	apiErrTypePassword(t, typed)
	var grant string
	var err error
	apiErrCapture(t, func() { grant, err = mintStepUpGrant("snapshot.download", "Confirm your password.") })
	return grant, err
}

// A wrong password is the platform's reason, in the platform's words. It is not
// an expired session, and sending the person to log in sends them to the command
// that could not have helped.
func TestMintStepUpGrant_AWrongPasswordIsThePlatformsReasonAndNotASessionExpiry(t *testing.T) {
	apiErrStatusFeed(t)
	apiErrStepUpPlatform(t, http.StatusUnauthorized, `{"error":"password is incorrect"}`)

	_, err := apiErrMint(t, "not-the-password")

	if err == nil {
		t.Fatal("a wrong password minted a grant")
	}
	apiErrContains(t, err.Error(), "Couldn't verify your password", "password is incorrect")
	apiErrAbsent(t, err.Error(), "session expired", "drift account login", "DRIFT-1001", "could not verify")
}

// A refusal that is not a 401 keeps the platform's reason and the code its status
// has.
func TestMintStepUpGrant_AForbiddenAnswerKeepsThePlatformsReason(t *testing.T) {
	reads := apiErrStatusFeed(t)
	apiErrStepUpPlatform(t, http.StatusForbidden, `{"error":"this token may not confirm a password"}`)

	_, err := apiErrMint(t, "the-account-password")

	apiErrFailure(t, err, reads, http.StatusForbidden,
		"Couldn't verify your password", "this token may not confirm a password", "DRIFT-1002")
}

// A platform fault is the platform's, and the failure says so.
func TestMintStepUpGrant_APlatformFaultIsReportedAsOne(t *testing.T) {
	reads := apiErrStatusFeed(t)
	apiErrStepUpPlatform(t, http.StatusInternalServerError, `{"error":"could not issue a grant"}`)

	_, err := apiErrMint(t, "the-account-password")

	apiErrFailure(t, err, reads, http.StatusInternalServerError,
		"Couldn't verify your password", "could not issue a grant", "DRIFT-1005")
	apiErrAbsent(t, err.Error(), "session expired", "could not verify")
}

// The control: the password that was typed is the one sent, and the grant that
// comes back is the one returned.
func TestMintStepUpGrant_ThePasswordTypedIsTheOneSent(t *testing.T) {
	var sent string
	apiErrPlatform(t, map[string]func(*http.Request) apiErrAnswer{
		"POST /ops/account/stepup": func(r *http.Request) apiErrAnswer {
			raw, _ := io.ReadAll(r.Body)
			sent = string(raw)
			return apiErrAnswer{status: http.StatusOK, body: `{"grant":"` + apiErrGrant + `"}`}
		},
	})

	grant, err := apiErrMint(t, "the-account-password")

	if err != nil || grant != apiErrGrant {
		t.Fatalf("mintStepUpGrant = (%q, %v), want (%q, nil)", grant, err, apiErrGrant)
	}
	apiErrContains(t, sent, `"password":"the-account-password"`, `"purpose":"snapshot.download"`)
}
