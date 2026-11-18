package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAPIError_StatusMessages(t *testing.T) {
	// The 5xx rows consult component health, so without a stub this table would
	// reach the real status page — slow, and dependent on the platform being up
	// to assert wording that has nothing to do with it.
	withStatusFeed(t, 200, feedOK)

	tests := []struct {
		name   string
		err    APIError
		substr string
	}{
		{"401 session expired", APIError{Op: "list slices", Status: 401}, "session expired"},
		{"401 login with detail", APIError{Op: "log in", Status: 401, Detail: "bad password"}, "bad password"},
		{"401 login no detail", APIError{Op: "log in", Status: 401}, "invalid username or password"},
		{"403 with detail", APIError{Op: "delete slice", Status: 403, Detail: "not owner"}, "not owner"},
		{"403 no detail", APIError{Op: "delete", Status: 403}, "don't have permission"},
		{"404 with detail", APIError{Op: "get slice", Status: 404, Detail: "slice not found"}, "slice not found"},
		{"404 no detail", APIError{Op: "get", Status: 404}, "wasn't found"},
		{"409 with detail", APIError{Op: "create", Status: 409, Detail: "already exists"}, "already exists"},
		{"409 no detail", APIError{Op: "create", Status: 409}, "conflicts"},
		{"429 with detail", APIError{Op: "deploy", Status: 429, Detail: "function limit"}, "function limit"},
		{"429 no detail", APIError{Op: "deploy", Status: 429}, "plan limit"},
		{"402 same as 429", APIError{Op: "deploy", Status: 402}, "plan limit"},
		{"400 with detail", APIError{Op: "deploy", Status: 400, Detail: "bad input"}, "bad input"},
		{"400 with raw", APIError{Op: "deploy", Status: 400, Raw: "raw body"}, "raw body"},
		{"400 no detail no raw", APIError{Op: "deploy", Status: 400}, "rejected"},
		{"500 with detail", APIError{Op: "deploy", Status: 500, Detail: "db down"}, "db down"},
		{"500 no detail", APIError{Op: "deploy", Status: 500}, "having trouble"},
		// 503 deliberately DISCARDS the server's detail rather than echoing it.
		// This case used to assert the opposite. A 5xx detail is written for an
		// operator ("overloaded", "db down") and reaches a user who can do
		// nothing with it except learn our architecture; unavailability is the
		// one status where the honest message is the same every time.
		{"503 ignores the server detail", APIError{Op: "deploy", Status: 503, Detail: "db down"}, "temporarily unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if !strings.Contains(msg, tt.substr) {
				t.Errorf("error message %q should contain %q", msg, tt.substr)
			}
		})
	}
}

func TestAPIError_LeadIn(t *testing.T) {
	e := &APIError{Op: "create slice", Status: 500}
	if !strings.HasPrefix(e.Error(), "Couldn't create slice") {
		t.Fatalf("should start with op: %q", e.Error())
	}

	e2 := &APIError{Status: 500}
	if !strings.HasPrefix(e2.Error(), "Something went wrong") {
		t.Fatalf("empty op: %q", e2.Error())
	}
}

func TestAPIError_Fallback(t *testing.T) {
	// Status 0 (shouldn't happen but defensive).
	e := &APIError{Op: "test", Detail: "info"}
	if !strings.Contains(e.Error(), "info") {
		t.Fatalf("fallback: %q", e.Error())
	}

	e2 := &APIError{Op: "test", Raw: "raw"}
	if !strings.Contains(e2.Error(), "raw") {
		t.Fatalf("fallback raw: %q", e2.Error())
	}

	e3 := &APIError{Op: "test"}
	if !strings.HasSuffix(e3.Error(), ".") {
		t.Fatalf("fallback empty: %q", e3.Error())
	}
}

func TestCheckResponse_Success(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
	body, err := CheckResponse(resp, "test op")
	if err != nil {
		t.Fatalf("expected nil error: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body: %q", body)
	}
}

func TestCheckResponse_201(t *testing.T) {
	resp := &http.Response{
		StatusCode: 201,
		Body:       io.NopCloser(strings.NewReader(`created`)),
	}
	body, err := CheckResponse(resp, "create")
	if err != nil {
		t.Fatalf("201 should succeed: %v", err)
	}
	if string(body) != "created" {
		t.Fatalf("body: %q", body)
	}
}

func TestCheckResponse_Error(t *testing.T) {
	resp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(`{"error":"bad input"}`)),
	}
	_, err := CheckResponse(resp, "deploy")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != 400 {
		t.Fatalf("status: %d", apiErr.Status)
	}
	if apiErr.Detail != "bad input" {
		t.Fatalf("detail: %q", apiErr.Detail)
	}
}

func TestCheckResponse_NonJSONBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader("Internal Server Error")),
	}
	_, err := CheckResponse(resp, "deploy")
	var apiErr *APIError
	errors.As(err, &apiErr)
	if apiErr.Detail != "" {
		t.Fatalf("non-JSON body should have empty detail: %q", apiErr.Detail)
	}
	if apiErr.Raw != "Internal Server Error" {
		t.Fatalf("raw: %q", apiErr.Raw)
	}
}

func TestExtractDetail(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"error field", `{"error":"not found"}`, "not found"},
		{"message field", `{"message":"created"}`, "created"},
		{"detail field", `{"detail":"info"}`, "info"},
		{"reason field", `{"reason":"quota"}`, "quota"},
		{"error takes precedence", `{"error":"err","message":"msg"}`, "err"},
		{"empty body", "", ""},
		{"invalid json", "not json", ""},
		{"empty error field", `{"error":""}`, ""},
		{"numeric value", `{"error":123}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDetail([]byte(tt.body))
			if got != tt.want {
				t.Errorf("extractDetail(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestTransportError_ConnectionRefused(t *testing.T) {
	err := TransportError("deploy", errors.New("dial tcp 127.0.0.1:8000: connection refused"))
	if !strings.Contains(err.Error(), "couldn't reach the Drift API") {
		t.Fatalf("msg: %q", err.Error())
	}
}

func TestTransportError_Generic(t *testing.T) {
	err := TransportError("deploy", errors.New("some unknown error"))
	if !strings.Contains(err.Error(), "some unknown error") {
		t.Fatalf("should pass through: %q", err.Error())
	}
}

func TestTransportError_SessionExpired(t *testing.T) {
	err := TransportError("list slices", errors.New("session expired — run drift account login"))
	if !strings.Contains(err.Error(), "run drift account login") {
		t.Fatalf("msg: %q", err.Error())
	}
}

// TestAPIError_UnavailableIsNotABrokenPlatform — 502/503/504 mean Drift cannot
// answer right now, which is a different user action (wait) from a 500 (report
// it). The message must also not name the component: a database, a rollout and
// a chart apply are the same event from outside.
func TestAPIError_UnavailableIsNotABrokenPlatform(t *testing.T) {
	for _, code := range []int{502, 503, 504} {
		got := (&APIError{Status: code, Op: "list slices"}).Error()
		if !strings.Contains(got, "temporarily unavailable") {
			t.Errorf("HTTP %d: %q should say the platform is temporarily unavailable", code, got)
		}
		if !strings.Contains(got, "login is still valid") {
			t.Errorf("HTTP %d: %q must reassure the user their session is fine", code, got)
		}
		for _, leak := range []string{"database", "mongo", "Mongo", "kubernetes", "pod"} {
			if strings.Contains(got, leak) {
				t.Errorf("HTTP %d: %q leaks infrastructure detail (%q)", code, got, leak)
			}
		}
	}
}

// TestAPIError_RealUnauthorizedStillSaysReauthenticate is the control. The fix
// must not soften a genuine credential rejection into "try again later", or a
// user with a truly dead session waits forever for nothing to change.
func TestAPIError_RealUnauthorizedStillSaysReauthenticate(t *testing.T) {
	got := (&APIError{Status: 401, Op: "list slices"}).Error()
	if !strings.Contains(got, "drift account login") {
		t.Errorf("a real 401 must still tell the user to log in, got %q", got)
	}
}

// TestMaintenanceMessage_NeverAdvisesLoggingOut is the whole point. The message
// this replaced told users to re-authenticate during an outage — and auth needs
// the same platform, so following it turned a wait into a lockout.
func TestMaintenanceMessage_NeverAdvisesLoggingOut(t *testing.T) {
	if strings.Contains(MaintenanceMessage, "account login") ||
		strings.Contains(MaintenanceMessage, "re-authenticate") {
		t.Fatalf("the maintenance message must never advise logging out: %q", MaintenanceMessage)
	}
}

// THE regression. The operator refuses with http.Error, so a refusal goes out as
// text/plain and the api forwards it verbatim: a second free slice answers 409
// with the body below. Parsing only JSON discarded that, and the user saw "that
// conflicts with existing state" — which names no rule, suggests nothing to do,
// and reads as a platform fault rather than as the free-tier limit working
// exactly as designed.
//
// The fixture is the message the platform SENDS TODAY (db.ErrOneFreeSlice). It
// used to be "only one free hacker slice is allowed per account", which a person
// reads as "you may not have another slice" and stops — the platform now says
// that a second slice is allowed and priced, and a fixture quoting the old
// wording would leave this file describing a refusal nobody receives.
func TestDetailForStatus_PlainTextBodyIsTheReason(t *testing.T) {
	const reason = "the free tier is one slice per account, and you already have one. " +
		"A second slice is allowed and is priced on its shape — write billing = \"monthly\" in its " +
		"Driftfile and run `drift plan` to see the cost before anything is charged"
	if got := detailForStatus(http.StatusConflict, []byte(reason+"\n")); got != reason {
		t.Errorf("detailForStatus(409, plain text) = %q, want %q", got, reason)
	}

	// End to end, through the message the user actually reads. The reason must
	// lead — it is the sentence that names the rule — and the code follows it.
	err := &APIError{
		Op:     "create slice",
		Status: http.StatusConflict,
		Detail: detailForStatus(http.StatusConflict, []byte(reason)),
	}
	if got := err.Error(); !strings.HasPrefix(got, "Couldn't create slice: "+reason+".") {
		t.Errorf("Error() = %q, want it to lead with the server's own reason", got)
	}
}

// A 409 CARRIES A CODE. It used to be alone among its neighbours in offering
// none: 401, 403, 404, 402/429 and 5xx all resolved to a DRIFT-xxxx and a
// `drift doctor explain` pointer, and the one status whose entire content is
// "something already exists" resolved to the empty string.
func TestAPIError_AConflictCarriesACodeLikeEveryOtherStatus(t *testing.T) {
	err := &APIError{
		Op:     "create slice",
		Status: http.StatusConflict,
		Detail: "the free tier is one slice per account, and you already have one",
	}
	if got := err.code(); got != "DRIFT-1013" {
		t.Errorf("code() = %q, want DRIFT-1013", got)
	}
	if !strings.Contains(err.Error(), "drift doctor explain DRIFT-1013") {
		t.Errorf("a conflict must offer the explain pointer every other status offers.\ngot: %s", err.Error())
	}
}

// AN UNAVAILABLE PLATFORM IS NOT DRIFT-1005, and this is the one that was
// actively misleading rather than merely missing.
//
// DRIFT-1005 is documented as "Nothing you changed caused it", pointing at
// status.ondrift.eu. For a 502 both sentences are routinely false: a resize
// restarts the slice's pod, so a deploy issued seconds later hits a runner that
// is gone — the user caused it, from the same terminal, and the status page
// shows every component green. message() already told these apart; the code did
// not, so the explain text contradicted the message beside it.
func TestAPIError_UnavailableHasItsOwnCodeNotTheItsNotYouOne(t *testing.T) {
	for _, status := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		err := &APIError{Op: "deploy the function", Status: status}
		if got := err.code(); got != "DRIFT-1012" {
			t.Errorf("status %d: code() = %q, want DRIFT-1012", status, got)
		}
	}

	// The control. A genuine 500 keeps DRIFT-1005, or the split has simply moved
	// every 5xx to the new code and explained nothing.
	plain := &APIError{Op: "deploy the function", Status: http.StatusInternalServerError}
	if got := plain.code(); got != "DRIFT-1005" {
		t.Errorf("a plain 500 must stay DRIFT-1005, got %q", got)
	}
}

// The two explain texts must not say the same thing, or the split is a second
// number for one answer. DRIFT-1005 denies the user caused it; DRIFT-1012 must
// not, because for a 502 they often did.
func TestErrorCodes_TheUnavailableRemedyDoesNotDenyTheUserCausedIt(t *testing.T) {
	unavailable, ok := LookupErrorCode("DRIFT-1012")
	if !ok {
		t.Fatal("DRIFT-1012 is not in the registry")
	}
	if strings.Contains(strings.ToLower(unavailable.Remedy), "nothing you changed") {
		t.Error("DRIFT-1012 repeats DRIFT-1005's denial — the case it exists for is one the user DID cause")
	}
	fault, _ := LookupErrorCode("DRIFT-1005")
	if unavailable.Remedy == fault.Remedy {
		t.Error("the two codes carry the same remedy, so the split added a number and no information")
	}
}

// A THROTTLE IS NOT A QUOTA. Both are a 429; the platform marks a throttle with
// Retry-After. A login refused for too many attempts was told "Run 'drift slice
// resize' to add capacity" under the plan-quota code, sending a person who had
// mistyped their password towards an operation that changes what they pay.
func TestAPIError_AThrottleSaysWaitAndNeverResize(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": {"60"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"too many login attempts, try again later"}`)),
	}
	_, err := CheckResponse(resp, "log in")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.RetryAfter != time.Minute {
		t.Fatalf("RetryAfter = %v, want 1m from the header", apiErr.RetryAfter)
	}

	got := apiErr.Error()
	if strings.Contains(got, "resize") || strings.Contains(got, "plan limit") {
		t.Errorf("a throttle must not send anyone to resize a slice:\n%s", got)
	}
	if !strings.Contains(got, "try again in 60 seconds") {
		t.Errorf("a throttle must say when to try again:\n%s", got)
	}
	if !strings.Contains(got, "too many login attempts") {
		t.Errorf("the server's own reason must lead:\n%s", got)
	}
	if apiErr.code() != "DRIFT-1014" {
		t.Errorf("code() = %q, want DRIFT-1014", apiErr.code())
	}
	if _, ok := LookupErrorCode("DRIFT-1014"); !ok {
		t.Error("DRIFT-1014 is rendered but not registered, so `doctor explain` cannot answer it")
	}

	// The control: a 429 with no Retry-After is a quota, and keeps its advice.
	quota := &APIError{Op: "deploy", Status: http.StatusTooManyRequests, Detail: "function limit reached (5/5)"}
	if quota.code() != "DRIFT-1004" || !strings.Contains(quota.Error(), "drift slice resize") {
		t.Errorf("a quota lost its code or its remedy:\n%s", quota.Error())
	}
}

// The registry says the same thing the message does: wait, and do not resize.
func TestErrorCodes_TheThrottleRemedyDoesNotAdviseResizing(t *testing.T) {
	c, ok := LookupErrorCode("DRIFT-1014")
	if !ok {
		t.Fatal("DRIFT-1014 is not in the registry")
	}
	if c.Command != "" {
		t.Errorf("a throttle's only remedy is to wait, so it names no command, got %q", c.Command)
	}
	quota, _ := LookupErrorCode("DRIFT-1004")
	if c.Remedy == quota.Remedy {
		t.Error("the throttle and the quota carry the same remedy")
	}
}

// Retry-After comes in two forms, seconds or an HTTP date. Anything the CLI
// cannot turn into a future wait is no wait, so the refusal reads as a quota
// rather than as "try again in 0 seconds".
func TestRetryAfter_ReadsBothFormsAndRefusesNonsense(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"60", time.Minute},
		{" 86400 ", 24 * time.Hour},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"0", 0},
		{"-5", 0},
		{"soon", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := retryAfter(c.in, now); got != c.want {
			t.Errorf("retryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A wait is stated the way a person says it, and rounded UP: rounding down tells
// someone to retry while the refusal still stands.
func TestHumanWait_RoundsUpIntoTheUnitAPersonUses(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Second:                  "1 second",
		30 * time.Second:             "30 seconds",
		time.Minute:                  "60 seconds",
		90 * time.Second:             "2 minutes",
		270 * time.Second:            "5 minutes",
		24 * time.Hour:               "24 hours",
		90*time.Minute + time.Second: "2 hours",
		1500 * time.Millisecond:      "2 seconds",
	} {
		if got := humanWait(d); got != want {
			t.Errorf("humanWait(%v) = %q, want %q", d, got, want)
		}
	}
}

// 4xx only. A 5xx is the platform being broken, and those paths already say
// something careful that names no component; splicing a bare "Internal Server
// Error" into one adds noise and no information.
func TestDetailForStatus_PlainTextIsClientErrorsOnly(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		if got := detailForStatus(status, []byte("Internal Server Error")); got != "" {
			t.Errorf("detailForStatus(%d, plain text) = %q, want empty", status, got)
		}
	}
	// A server that DOES send structured JSON on a 5xx is still read.
	if got := detailForStatus(500, []byte(`{"error":"db down"}`)); got != "db down" {
		t.Errorf("detailForStatus(500, JSON) = %q, want db down", got)
	}
}

// JSON still wins, and a JSON body with no readable field yields nothing rather
// than the raw JSON — printing `{"code":42}` at a person is not an improvement.
func TestDetailForStatus_JSONStillPreferred(t *testing.T) {
	if got := detailForStatus(409, []byte(`{"error":"nope"}`)); got != "nope" {
		t.Errorf("JSON error field: got %q, want nope", got)
	}
	if got := detailForStatus(409, []byte(`{"code":42}`)); got != "" {
		t.Errorf("JSON without a message field: got %q, want empty", got)
	}
}

// Machinery must not reach the user. A gateway's HTML page, a multi-line trace
// and an overlong body are all worse in a one-line CLI message than the generic
// text they would replace.
func TestDetailForStatus_RejectsNonMessages(t *testing.T) {
	cases := map[string]string{
		"html":      "<html><body>502 Bad Gateway</body></html>",
		"multiline": "panic: nope\n\tgoroutine 1 [running]:",
		"toolong":   strings.Repeat("x", maxPlainDetail+1),
		"control":   "bad\x00byte",
	}
	for name, body := range cases {
		if got := detailForStatus(http.StatusConflict, []byte(body)); got != "" {
			t.Errorf("%s: detailForStatus = %q, want empty", name, got)
		}
	}
}

// A refused resize carries the resource that would drop below usage, and the
// user needs it: "resize would shrink below current usage" alone tells them to
// raise a number without saying which one.
func TestDetailForStatus_RendersResizeViolations(t *testing.T) {
	body := []byte(`{"error":"resize would shrink below current usage",` +
		`"violations":[{"resource":"atomic.functions","used":5,"new_limit":1}]}`)
	got := detailForStatus(http.StatusConflict, body)
	want := "resize would shrink below current usage (atomic.functions: 5 in use, limit would be 1)"
	if got != want {
		t.Errorf("detailForStatus =\n  %q\nwant\n  %q", got, want)
	}
}

// An error body with no violations is unchanged — most refusals have none, and
// appending an empty parenthetical to all of them would be noise.
func TestDetailForStatus_NoViolationsIsUnchanged(t *testing.T) {
	if got := detailForStatus(409, []byte(`{"error":"nope"}`)); got != "nope" {
		t.Errorf("got %q, want nope", got)
	}
	if got := detailForStatus(409, []byte(`{"error":"nope","violations":[]}`)); got != "nope" {
		t.Errorf("empty violations: got %q, want nope", got)
	}
}

// A 404 FROM A PLATFORM A MAJOR AHEAD IS A VERSION DIFFERENCE, NOT A MISSING
// RESOURCE.
//
// The api answers a path it no longer serves with the bare {"error":"not found"}
// and states its route-contract major beside it. Printing "not found" sends a
// person looking for a typo when the thing to do is upgrade. Only a BARE answer
// is read that way: a 404 that names its resource ("slice not found: demo") is
// the platform having found the route and missed the thing, which is a missing
// resource whatever major the platform speaks.

// refusalFrom returns the error CheckResponse builds for a reply with the given
// status and body from a platform that states `stated` as its API version ("" is
// no statement at all).
func refusalFrom(t *testing.T, status int, stated, body string) *APIError {
	t.Helper()
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	if stated != "" {
		resp.Header.Set(APIVersionHeader, stated)
	}
	_, err := CheckResponse(resp, "delete function")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	return apiErr
}

func TestAPIError_ABareNotFoundFromAPlatformAheadIsAVersionDifference(t *testing.T) {
	for name, body := range map[string]string{
		"the api's own route miss": `{"error":"not found"}`,
		"an empty body":            ``,
		"a router's default text":  "404 page not found\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := refusalFrom(t, http.StatusNotFound, "2", body)
			got := e.Error()

			for _, want := range []string{
				"older than the platform",
				"API version 1",
				"the platform speaks 2",
				"`drift upgrade`",
				"DRIFT-1015",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("the error does not say %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "wasn't found") || strings.Contains(got, ": not found.") {
				t.Errorf("a version difference was reported as a missing resource:\n%s", got)
			}
			if _, ok := LookupErrorCode(e.code()); !ok {
				t.Errorf("%s is rendered but not registered, so `doctor explain` cannot answer it", e.code())
			}
		})
	}
}

// THE CONTROLS. Each of these is a 404 that stays what it was: a named resource
// from a platform ahead, and the bare answer from a platform that is not ahead or
// states nothing readable. Without them the test above passes for a change that
// turns every 404 into advice to upgrade.
func TestAPIError_ANotFoundThatIsAMissingResourceStaysOne(t *testing.T) {
	for name, tc := range map[string]struct {
		stated, body, want string
	}{
		"a named resource, platform ahead":    {"2", `{"error":"slice not found: demo"}`, "slice not found: demo"},
		"a named function, platform ahead":    {"2", `{"error":"function not found"}`, "function not found"},
		"the bare answer, the same major":     {"1", `{"error":"not found"}`, "not found"},
		"the bare answer, no statement":       {"", `{"error":"not found"}`, "not found"},
		"the bare answer, an unreadable one":  {"latest", `{"error":"not found"}`, "not found"},
		"an empty body, the same major":       {"1", ``, "wasn't found"},
		"an empty body, no statement":         {"", ``, "wasn't found"},
		"a router's default, no statement":    {"", "404 page not found\n", "404 page not found"},
		"a router's default, the same major":  {"1", "404 page not found\n", "404 page not found"},
		"a named resource, no statement":      {"", `{"error":"secret not found"}`, "secret not found"},
		"a named resource, the same major":    {"1", `{"error":"secret not found"}`, "secret not found"},
		"a named resource, several ahead":     {"9", `{"error":"queue orders not found"}`, "queue orders not found"},
		"the bare answer, an older statement": {"0", `{"error":"not found"}`, "not found"},
	} {
		t.Run(name, func(t *testing.T) {
			e := refusalFrom(t, http.StatusNotFound, tc.stated, tc.body)
			got := e.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("the missing resource lost its words, want %q in:\n%s", tc.want, got)
			}
			if strings.Contains(got, "drift upgrade") || strings.Contains(got, "older than the platform") {
				t.Errorf("a missing resource was reported as a version difference:\n%s", got)
			}
			if e.code() != "DRIFT-1003" {
				t.Errorf("code() = %q, want DRIFT-1003", e.code())
			}
		})
	}
}

// Only a 404 is read this way. A refusal or a fault from a platform ahead keeps
// its own words and its own code: the platform said something specific, and it is
// not a path that moved.
func TestAPIError_OnlyANotFoundIsReadAsAVersionDifference(t *testing.T) {
	withStatusFeed(t, 200, feedOK)

	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
		code   string
	}{
		"a refusal":     {http.StatusForbidden, `{"error":"not the owner"}`, "not the owner", "DRIFT-1002"},
		"a conflict":    {http.StatusConflict, `{"error":"name taken"}`, "name taken", "DRIFT-1013"},
		"a quota":       {http.StatusTooManyRequests, `{"error":"function limit reached"}`, "function limit reached", "DRIFT-1004"},
		"a bad request": {http.StatusBadRequest, `{"error":"not found"}`, "not found", "DRIFT-1016"},
		"a fault":       {http.StatusInternalServerError, `{"error":"not found"}`, "having trouble", "DRIFT-1005"},
	} {
		t.Run(name, func(t *testing.T) {
			e := refusalFrom(t, tc.status, "2", tc.body)
			got := e.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, got)
			}
			if strings.Contains(got, "drift upgrade") {
				t.Errorf("a status other than 404 was reported as a version difference:\n%s", got)
			}
			if e.code() != tc.code {
				t.Errorf("code() = %q, want %q", e.code(), tc.code)
			}
		})
	}
}

// The remedy the registry names for DRIFT-1015 is the upgrade command, in at most
// three words, and it leaves the way back open: a 404 that survives an upgrade is
// an ordinary missing resource.
func TestErrorCodes_ThePlatformAheadRemedyIsTheUpgradeCommand(t *testing.T) {
	c, ok := LookupErrorCode("DRIFT-1015")
	if !ok {
		t.Fatal("DRIFT-1015 is not in the registry")
	}
	if c.Command != "drift upgrade" {
		t.Errorf("Command = %q, want %q", c.Command, "drift upgrade")
	}
	if !strings.Contains(c.Remedy, "DRIFT-1003") {
		t.Errorf("the remedy does not say what a 404 that survives the upgrade means:\n%s", c.Remedy)
	}
}

// ─── a refusal at a ceiling ─────────────────────────────────────────────────
//
// The platform says a ceiling as fields beside `error`: `what` names the thing limited in a
// person's words, `limit` is the ceiling, `current` is what the slice holds against it when
// the ceiling is a stock, `unit` is "bytes" when the figures are sizes, and `fixed` is true
// when no change to the slice raises the ceiling. The status is the platform's to choose (a
// count is a 429 except the member cap, which is a 409, and a size is a 413), so the code and
// the advice follow from the fields and not from the status.

// One body for each shape of ceiling. The sentences are the platform's own and carry no final
// full stop, because the CLI adds its own.
const (
	// A count the slice holds: `current`, and no `unit`.
	bodySlots = `{"error":"this slice allows 6 function slots and holds 6","action":"atomic.deploy_function",` +
		`"what":"function slots","limit":6,"current":6}`
	// Bytes the slice holds: `current` and `unit` both.
	bodyBucket = `{"error":"bucket \"uploads\" holds 5 MiB of the 5 MiB it allows","action":"backbone.put_blob",` +
		`"what":"bucket \"uploads\"","limit":5242880,"current":5242880,"unit":"bytes"}`
	// A cap on one item: nothing is held against it, so there is no `current` key at all.
	bodySecretSize = `{"error":"a secret can be at most 1 KiB; this one is 2 KiB","action":"backbone.set_secret",` +
		`"what":"secret size","limit":1024,"unit":"bytes"}`
	// A cap on one item that the slice's shape does set.
	bodySiteSize = `{"error":"this site is 120 MiB, over the 100 MiB this slice allows for a site","action":"canvas.deploy_site",` +
		`"what":"site size","limit":104857600,"unit":"bytes"}`
	// A cap on one item that no change to the slice raises.
	bodyFunctionSize = `{"error":"this function is over the 64 MiB limit on a function's size","action":"atomic.deploy_function",` +
		`"what":"function size","limit":67108864,"unit":"bytes","fixed":true}`
	// A count held, fixed: the account's member cap.
	bodyMembers = `{"error":"this account is at its limit of 25 members and pending invites; remove one first",` +
		`"action":"account.invite_member","what":"members and pending invites","limit":25,"current":25,"fixed":true}`
	// Bytes held, fixed: the cache's size.
	bodyCacheSize = `{"error":"the cache holds 64 MiB of the 64 MiB it allows","action":"backbone.set_cache_value",` +
		`"what":"cache size","limit":67108864,"current":67108864,"unit":"bytes","fixed":true}`
	// What a platform wrote before `what`, `unit` and `fixed`: the action token, a limit and a current.
	bodyBeforeTheFields = `{"error":"limit reached for atomic.deploy_function (current: 6, max: 6)",` +
		`"action":"atomic.deploy_function","limit":6,"current":6}`
)

// refusedAtCeiling builds the failure CheckResponse builds for an answer with this status and
// body, from a platform that sends no Retry-After, which is how it says a quota and not a
// throttle.
func refusedAtCeiling(t *testing.T, op string, status int, body string) *APIError {
	t.Helper()
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	_, err := CheckResponse(resp, op)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	return apiErr
}

// inNoProject makes the working directory a place where no Driftfile resolves and the
// environment names no slice, so a quota's advice is the resize form.
func inNoProject(t *testing.T) {
	t.Helper()
	t.Setenv(SliceEnv, "")
	withDriftfileSlice(t, func(string) (string, string, bool, error) { return "", "", false, nil })
}

// ceilingFailure is what the CLI prints for a refusal: its sentence, the code's line, and the
// numbers on a line of their own. A code or numbers left empty leave their line out.
func ceilingFailure(sentence, code, numbers string) string {
	out := sentence
	if code != "" {
		out += fmt.Sprintf("\n  %[1]s · drift doctor explain %[1]s", code)
	}
	if numbers != "" {
		out += "\n  " + numbers
	}
	return out
}

// fixedCeilingCode is the registered code for a ceiling the platform fixes. It is found by its
// key, because the registry assigns the number and a test that held it would be wrong the day
// an earlier code is minted.
func fixedCeilingCode(t *testing.T) string {
	t.Helper()
	for _, c := range ErrorCodes() {
		if c.Key == "platform-limit" {
			return c.Code
		}
	}
	t.Fatal(`no registry entry has the key "platform-limit", the code for a ceiling the platform fixes`)
	return ""
}

// A ceiling is a quota whatever status the platform chose for it. A 413 that says a bucket is
// full printed the code for an invalid request and a 409 the code for a conflict, because the
// code was read from the status alone.
func TestAPIError_ACeilingPrintsTheQuotaCodeAndItsNumbersWhateverItsStatus(t *testing.T) {
	t.Skip("fails until APIError.code() and Error() read the ceiling's fields (task #2735)")
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)

	want := ceilingFailure(
		"Couldn't deploy the function: this slice allows 6 function slots and holds 6. Run 'drift slice resize' to add capacity.",
		"DRIFT-1004", "function slots: 6 of 6 used.")

	for _, status := range []int{
		http.StatusPaymentRequired,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity,
		http.StatusTooManyRequests,
	} {
		t.Run(fmt.Sprintf("%d %s", status, http.StatusText(status)), func(t *testing.T) {
			e := refusedAtCeiling(t, "deploy the function", status, bodySlots)

			if got := e.code(); got != "DRIFT-1004" {
				t.Errorf("code() = %q, want DRIFT-1004", got)
			}
			if got := e.Error(); got != want {
				t.Errorf("the failure reads\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// The numbers line has three shapes, each read from the fields: a count the slice holds, bytes
// the slice holds, and a cap on one item, which holds nothing and says only the limit. A
// missing current is never a zero.
func TestAPIError_TheNumbersLineSaysTheCeilingInTheWordsTheFieldsGive(t *testing.T) {
	t.Skip("fails until Error() renders the numbers line from the fields (task #2735)")
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)

	for _, tc := range []struct {
		name   string
		status int
		body   string
		line   string
	}{
		{"a count the slice holds", http.StatusTooManyRequests, bodySlots,
			"function slots: 6 of 6 used."},
		{"bytes the slice holds", http.StatusRequestEntityTooLarge, bodyBucket,
			`bucket "uploads": 5 MiB of 5 MiB used.`},
		{"a cap on one item", http.StatusRequestEntityTooLarge, bodySecretSize,
			"secret size: limit is 1 KiB."},
		{"a count with no stock, for a slice allowed none", http.StatusTooManyRequests,
			`{"error":"this slice has no scheduled jobs","what":"scheduled jobs","limit":0}`,
			"scheduled jobs: limit is 0."},
		{"a size that is not whole in its unit", http.StatusRequestEntityTooLarge,
			`{"error":"x","what":"collection \"events\"","limit":1048576,"current":1536,"unit":"bytes"}`,
			`collection "events": 1.5 KiB of 1 MiB used.`},
		{"no what and no current, so the action names the limit and no zero is printed", http.StatusTooManyRequests,
			`{"error":"x","action":"atomic.deploy_function","limit":6}`,
			"atomic.deploy_function: limit is 6."},
		{"the figures are the fields and not the digits in the sentence", http.StatusTooManyRequests,
			`{"error":"limit reached (current: 9, max: 9)","what":"function slots","limit":6,"current":6}`,
			"function slots: 6 of 6 used."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refusedAtCeiling(t, "deploy", tc.status, tc.body).Error()

			lines := strings.Split(got, "\n")
			if last := lines[len(lines)-1]; last != "  "+tc.line {
				t.Errorf("the numbers line reads %q, want %q; the whole failure:\n%s", last, "  "+tc.line, got)
			}
		})
	}
}

// A ceiling the platform fixes has a code of its own, and says nothing about raising it: the
// quota's advice is to resize, which would send a person to a form that has no line for it.
func TestAPIError_ACeilingThePlatformFixesHasItsOwnCodeAndNoAdvice(t *testing.T) {
	t.Skip("fails until code() and message() read the fixed field (task #2735)")
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)
	code := fixedCeilingCode(t)
	if code == "DRIFT-1004" {
		t.Fatal("the fixed ceiling shares the quota's code, so a person cannot tell them apart")
	}

	for _, tc := range []struct {
		name   string
		op     string
		status int
		body   string
		want   string
	}{
		{"a cap on one item", "deploy the function", http.StatusRequestEntityTooLarge, bodyFunctionSize,
			ceilingFailure("Couldn't deploy the function: this function is over the 64 MiB limit on a function's size.",
				code, "function size: limit is 64 MiB.")},
		{"a count held, as a 409", "invite a member", http.StatusConflict, bodyMembers,
			ceilingFailure("Couldn't invite a member: this account is at its limit of 25 members and pending invites; remove one first.",
				code, "members and pending invites: 25 of 25 used.")},
		{"bytes held, as a 429", "set the cache value", http.StatusTooManyRequests, bodyCacheSize,
			ceilingFailure("Couldn't set the cache value: the cache holds 64 MiB of the 64 MiB it allows.",
				code, "cache size: 64 MiB of 64 MiB used.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := refusedAtCeiling(t, tc.op, tc.status, tc.body)

			if got := e.code(); got != code {
				t.Errorf("code() = %q, want %s", got, code)
			}
			got := e.Error()
			if got != tc.want {
				t.Errorf("the failure reads\n%s\nwant\n%s", got, tc.want)
			}
			for _, advice := range []string{"resize", "Driftfile", "drift apply", "capacity"} {
				if strings.Contains(got, advice) {
					t.Errorf("a ceiling nothing raises must not advise %q:\n%s", advice, got)
				}
			}
		})
	}
}

// A refusal at a ceiling that carries no sentence still says what sort of refusal it is, and a
// fixed one does not call itself the plan's.
func TestAPIError_ACeilingWithNoSentenceStillSaysWhatSortOfRefusalItIs(t *testing.T) {
	t.Skip("fails until message() reads the ceiling's fields (task #2735)")
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)
	code := fixedCeilingCode(t)

	got := refusedAtCeiling(t, "deploy", http.StatusTooManyRequests,
		`{"what":"function slots","limit":6,"current":6}`).Error()
	want := ceilingFailure("Couldn't deploy: you're at your plan limit. Run 'drift slice resize' to add capacity.",
		"DRIFT-1004", "function slots: 6 of 6 used.")
	if got != want {
		t.Errorf("a quota with no sentence reads\n%s\nwant\n%s", got, want)
	}

	got = refusedAtCeiling(t, "deploy", http.StatusRequestEntityTooLarge,
		`{"what":"function size","limit":67108864,"unit":"bytes","fixed":true}`).Error()
	want = ceilingFailure("Couldn't deploy: that is over a limit Drift sets for every slice.",
		code, "function size: limit is 64 MiB.")
	if got != want {
		t.Errorf("a fixed ceiling with no sentence reads\n%s\nwant\n%s", got, want)
	}
}

// A status that is not a refusal at a capacity keeps its own code whatever its body carries:
// a session that ran out, a permission, a missing thing and a platform fault each have a
// remedy of their own, and a quota's advice would send the person the wrong way.
func TestAPIError_AStatusThatIsNotARefusalKeepsItsOwnCodeWhateverItsBodyCarries(t *testing.T) {
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)

	for _, tc := range []struct {
		status int
		code   string
	}{
		{http.StatusUnauthorized, "DRIFT-1001"},
		{http.StatusForbidden, "DRIFT-1002"},
		{http.StatusNotFound, "DRIFT-1003"},
		{http.StatusInternalServerError, "DRIFT-1005"},
		{http.StatusServiceUnavailable, "DRIFT-1012"},
	} {
		t.Run(fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)), func(t *testing.T) {
			e := refusedAtCeiling(t, "list slices", tc.status, bodySlots)

			if got := e.code(); got != tc.code {
				t.Errorf("code() = %q, want %s", got, tc.code)
			}
			got := e.Error()
			for _, advice := range []string{"drift slice resize", "Driftfile"} {
				if strings.Contains(got, advice) {
					t.Errorf("a status that is not a quota must not advise %q:\n%s", advice, got)
				}
			}
		})
	}
}

// A throttle is told from a quota by Retry-After, and a throttle that also carries a ceiling is
// still a throttle: it clears by itself, and resizing does not lift it.
func TestAPIError_AThrottleThatCarriesACeilingIsStillAThrottle(t *testing.T) {
	inNoProject(t)

	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": {"60"}},
		Body:       io.NopCloser(strings.NewReader(bodySlots)),
	}
	_, err := CheckResponse(resp, "deploy")
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("expected *APIError, got %T", err)
	}

	if got := e.code(); got != "DRIFT-1014" {
		t.Errorf("code() = %q, want DRIFT-1014", got)
	}
	got := e.Error()
	if !strings.Contains(got, "try again in 60 seconds") {
		t.Errorf("a throttle must say when to try again:\n%s", got)
	}
	for _, advice := range []string{"resize", "Driftfile", "capacity"} {
		if strings.Contains(got, advice) {
			t.Errorf("a throttle must not advise %q:\n%s", advice, got)
		}
	}
}

// A body from a platform that predates the fields (no what, no unit, no fixed) renders exactly
// as it always did, and a refusal that names no ceiling prints no numbers line and keeps the
// code its status has.
func TestAPIError_ABodyFromAPlatformBeforeTheFieldsRendersAsItAlwaysDid(t *testing.T) {
	withStatusFeed(t, 200, feedOK)
	inNoProject(t)

	const resize = " Run 'drift slice resize' to add capacity."

	for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests} {
		t.Run(fmt.Sprintf("a ceiling at %d", status), func(t *testing.T) {
			want := ceilingFailure(
				"Couldn't deploy: limit reached for atomic.deploy_function (current: 6, max: 6)."+resize,
				"DRIFT-1004", "atomic.deploy_function: 6 of 6 used.")

			if got := refusedAtCeiling(t, "deploy", status, bodyBeforeTheFields).Error(); got != want {
				t.Errorf("the failure reads\n%s\nwant\n%s", got, want)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"a quota that names no ceiling", http.StatusTooManyRequests, `{"error":"limit reached"}`,
			ceilingFailure("Couldn't deploy: limit reached."+resize, "DRIFT-1004", "")},
		{"a quota with nothing to say", http.StatusPaymentRequired, `{}`,
			ceilingFailure("Couldn't deploy: you're at your plan limit."+resize, "DRIFT-1004", "")},
		{"a conflict", http.StatusConflict, `{"error":"already exists"}`,
			ceilingFailure("Couldn't deploy: already exists.", "DRIFT-1013", "")},
		{"a body too large that is no ceiling", http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`,
			ceilingFailure("Couldn't deploy: request body too large.", "DRIFT-1016", "")},
		{"digits in the sentence are no ceiling", http.StatusTooManyRequests,
			`{"error":"limit reached (current: 6, max: 6)"}`,
			ceilingFailure("Couldn't deploy: limit reached (current: 6, max: 6)."+resize, "DRIFT-1004", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusedAtCeiling(t, "deploy", tc.status, tc.body).Error(); got != tc.want {
				t.Errorf("the failure reads\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// A quota is raised where the slice's shape is written. For a slice a Driftfile declares that is
// the file, because `drift apply` makes the slice the file's shape and undoes a limit raised on
// the resize form; for any other slice it is the form. The Driftfile counts only when it is
// what names the slice: DRIFT_SLICE names a slice the file may not declare.
func TestAPIError_AQuotaNamesTheDriftfileWhenOneResolvesAndTheResizeFormWhenNoneDoes(t *testing.T) {
	t.Skip("fails until message() chooses the sentence by the Driftfile (task #2736)")
	withStatusFeed(t, 200, feedOK)

	const (
		inTheFile = "Raise it in the Driftfile, then run 'drift apply'."
		onTheForm = "Run 'drift slice resize' to add capacity."
	)
	project := func(string) (string, string, bool, error) { return "orders", "/p/Driftfile", true, nil }
	none := func(string) (string, string, bool, error) { return "", "", false, nil }
	unnamed := func(string) (string, string, bool, error) {
		return "", "/p/Driftfile", true, errors.New("it declares 2 slice blocks")
	}

	for _, tc := range []struct {
		name   string
		env    string
		find   func(string) (string, string, bool, error)
		status int
		body   string
		says   string
	}{
		{"a Driftfile resolves: a count", "", project, http.StatusTooManyRequests, bodySlots, inTheFile},
		{"a Driftfile resolves: a size the slice allows", "", project, http.StatusRequestEntityTooLarge, bodySiteSize, inTheFile},
		{"a Driftfile resolves: a refusal that names no ceiling", "", project, http.StatusPaymentRequired, `{"error":"limit reached"}`, inTheFile},
		{"a Driftfile resolves: the platform fixes the ceiling", "", project, http.StatusRequestEntityTooLarge, bodyFunctionSize, ""},
		{"no Driftfile resolves: a count", "", none, http.StatusTooManyRequests, bodySlots, onTheForm},
		{"no Driftfile resolves: a size the slice allows", "", none, http.StatusRequestEntityTooLarge, bodySiteSize, onTheForm},
		{"no Driftfile resolves: a refusal that names no ceiling", "", none, http.StatusTooManyRequests, `{"error":"limit reached"}`, onTheForm},
		{"a Driftfile resolves and DRIFT_SLICE names a slice", "other", project, http.StatusTooManyRequests, bodySlots, onTheForm},
		{"a Driftfile that names no one slice", "", unnamed, http.StatusTooManyRequests, bodySlots, onTheForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(SliceEnv, tc.env)
			withDriftfileSlice(t, tc.find)

			got := refusedAtCeiling(t, "deploy", tc.status, tc.body).Error()
			first := strings.SplitN(got, "\n", 2)[0]

			if tc.says != "" && !strings.HasSuffix(first, " "+tc.says) {
				t.Errorf("the refusal should end with %q:\n%s", tc.says, got)
			}
			for _, sentence := range []string{inTheFile, onTheForm} {
				if sentence != tc.says && strings.Contains(got, sentence) {
					t.Errorf("the refusal should not say %q:\n%s", sentence, got)
				}
			}
		})
	}
}

// The code for a ceiling the platform fixes is registered like every other, and it names no
// command: nothing a person can run raises the limit, and the remedy says to send less.
func TestErrorCodes_TheFixedCeilingCodeNamesNoCommandAndDoesNotAdviseResizing(t *testing.T) {
	c, ok := LookupErrorCode(fixedCeilingCode(t))
	if !ok {
		t.Fatal("the code for a ceiling the platform fixes does not resolve")
	}
	if c.Command != "" {
		t.Errorf("nothing a person can run raises a fixed ceiling, so it names no command, got %q", c.Command)
	}
	if strings.TrimSpace(c.Meaning) == "" || strings.TrimSpace(c.Remedy) == "" {
		t.Errorf("a code needs a meaning and a remedy: %+v", c)
	}
	if strings.Contains(strings.ToLower(c.Remedy), "resize") {
		t.Errorf("the remedy for a ceiling no resize raises must not mention resizing:\n%s", c.Remedy)
	}
	quota, _ := LookupErrorCode("DRIFT-1004")
	if c.Key == quota.Key || c.Remedy == quota.Remedy {
		t.Error("the fixed ceiling and the quota carry the same key or remedy, so the split added a number and no information")
	}
}

// The quota's remedy names the route a slice with a Driftfile takes, as its command, and keeps
// the resize form in its prose for the slice that has none.
func TestErrorCodes_TheQuotaRemedyNamesTheDriftfileRouteAndKeepsTheResizeRoute(t *testing.T) {
	t.Skip("fails until the quota's command is drift apply (task #2736)")
	c, ok := LookupErrorCode("DRIFT-1004")
	if !ok {
		t.Fatal("DRIFT-1004 is not in the registry")
	}
	if c.Command != "drift apply" {
		t.Errorf("Command = %q, want %q", c.Command, "drift apply")
	}
	for _, want := range []string{"Driftfile", "drift slice resize"} {
		if !strings.Contains(c.Remedy, want) {
			t.Errorf("the remedy does not mention %q:\n%s", want, c.Remedy)
		}
	}
}
