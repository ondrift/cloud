package common

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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
		"A second slice is allowed and is priced on its shape — run `drift slice create <name>` " +
		"without --free to choose that shape and see the cost before anything is charged"
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
