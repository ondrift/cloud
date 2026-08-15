// Package common — humane error rendering for API responses and network
// failures.
//
// The guiding philosophy: the CLI owes its users clear, honest, explicit
// messages when something goes wrong. Dumping a raw JSON blob at the user's
// terminal is a cop-out. Every error should read like a human wrote it —
// informal where it helps, informative always, honest when it's our fault.
//
// Typical usage from a command:
//
//	resp, err := common.DoRequest(http.MethodPost, url, body)
//	if err != nil {
//	    fmt.Println(common.TransportError("create slice", err))
//	    return
//	}
//	defer resp.Body.Close()
//
//	respBody, err := common.CheckResponse(resp, "create slice")
//	if err != nil {
//	    fmt.Println(err)
//	    return
//	}
//	// ... use respBody
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// APIError is the humane rendering of a non-2xx response from the Drift API.
// It implements the error interface; callers should just `fmt.Println(err)`.
type APIError struct {
	// Op is a short, lowercase, imperative description of what the CLI
	// was trying to do ("create slice", "deploy atomic function"). Used
	// as the lead-in: "Couldn't {op}: ...".
	Op string

	// Status is the HTTP status code the server returned.
	Status int

	// Detail is the server-supplied reason, extracted from the JSON body
	// ("error" or "message" fields). May be empty when the body is not
	// JSON or when the server didn't include one.
	Detail string

	// Raw is the trimmed response body. Used only as a last-resort
	// fallback when Detail is empty AND the status code has no specific
	// mapping — we'd rather show the raw body than nothing.
	Raw string
}

// ErrPlatformUnavailable and ErrSessionRejected split the two ways a token
// refresh can fail. They are opposite in meaning and were indistinguishable to
// callers, which is how an outage came to advise logging out.
var (
	// ErrPlatformUnavailable — the platform could not answer. The session may
	// be perfectly valid; nobody knows yet. Wait.
	ErrPlatformUnavailable = errors.New("drift is temporarily unavailable")
	// ErrSessionRejected — the platform answered, and said no. Log in again.
	ErrSessionRejected = errors.New("session rejected")
)

// MaintenanceMessage is what a user sees when Drift is temporarily
// unavailable. One string, used by every path that can conclude it, so the
// platform cannot say two different things about the same condition.
//
// It says three things on purpose. That Drift is unavailable, not broken —
// so the next move is to wait. That the user's login is FINE, because the
// path that produced this used to advise re-authenticating, which was worse
// than useless: auth needs the same platform, so a user who followed it
// logged out and could not get back in, turning a wait into a lockout. And
// nothing whatsoever about which component is unavailable — a database, a
// rollout and a chart apply are all the same event from outside, and naming
// the internals leaks the architecture without helping anyone
// (philosophy.md: "No Kubernetes leakage").
const MaintenanceMessage = "Drift is temporarily unavailable — most likely brief maintenance. " +
	"Your login is still valid; nothing to do but try again in a few minutes."

// Error renders the failure and, where the failure has a name, appends it.
//
// The code is chosen from the STATUS rather than the message, because the status
// is what the platform actually said — a message is rewritten as wording
// improves, and a code that moved with the wording would not be stable enough to
// paste. A status with no registered code renders exactly as before.
func (e *APIError) Error() string {
	out := withCode(e.message(), e.code())
	// Only a 5xx. A 4xx is the platform telling the user something true about
	// their request, and checking component health there would offer an excuse
	// for a refusal that is correct.
	if e.Status >= 500 {
		if note := platformFaultNote(DegradedComponents()); note != "" {
			out += "\n" + note
		}
	}
	return out
}

// code names this failure, or "" for a status that has none yet.
func (e *APIError) code() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		// A refused LOGIN is not an expired session — the message already says
		// the password was wrong, and pointing at `account login` would send the
		// user to the command they just ran.
		if e.Op == "log in" {
			return ""
		}
		return "DRIFT-1001"
	case e.Status == http.StatusForbidden:
		return "DRIFT-1002"
	case e.Status == http.StatusNotFound:
		return "DRIFT-1003"
	case e.Status == http.StatusPaymentRequired || e.Status == http.StatusTooManyRequests:
		return "DRIFT-1004"
	case e.Status >= 500:
		return "DRIFT-1005"
	}
	return ""
}

func (e *APIError) message() string {
	lead := "Something went wrong"
	if e.Op != "" {
		lead = "Couldn't " + e.Op
	}

	switch {
	case e.Status == http.StatusUnauthorized:
		if e.Op == "log in" {
			if e.Detail != "" {
				return lead + ": " + e.Detail + "."
			}
			return lead + ": invalid username or password."
		}
		return lead + ": your session expired. Run 'drift account login' to re-authenticate."

	case e.Status == http.StatusForbidden:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": you don't have permission to do that."

	case e.Status == http.StatusNotFound:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that resource wasn't found."

	case e.Status == http.StatusConflict:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that conflicts with existing state."

	case e.Status == http.StatusPaymentRequired || e.Status == http.StatusTooManyRequests:
		if e.Detail != "" {
			return lead + ": " + e.Detail + ". Run 'drift slice resize' to add capacity."
		}
		return lead + ": you're at your plan limit. Run 'drift slice resize' to add capacity."

	case e.Status >= 400 && e.Status < 500:
		// Generic 4xx — trust the server's detail if it sent one.
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		if e.Raw != "" {
			return fmt.Sprintf("%s: %s (HTTP %d).", lead, e.Raw, e.Status)
		}
		return fmt.Sprintf("%s: request was rejected (HTTP %d).", lead, e.Status)

	// 502/503/504 are the platform being UNAVAILABLE rather than broken —
	// a maintenance window, a rolling restart, a dependency being upgraded.
	// Told apart from a generic 500 because the user's next move differs: wait,
	// rather than report it. Deliberately says nothing about WHICH component is
	// unavailable; "the database is down" is our problem to know and theirs to
	// be spared (philosophy.md: no infrastructure leakage).
	case e.Status == http.StatusBadGateway ||
		e.Status == http.StatusServiceUnavailable ||
		e.Status == http.StatusGatewayTimeout:
		return lead + ": " + MaintenanceMessage

	case e.Status >= 500:
		if e.Detail != "" {
			return lead + ": the platform is having trouble — " + e.Detail + ". That's on us; give it a moment and try again."
		}
		return lead + ": the platform is having trouble. That's on us; give it a moment and try again."
	}

	// Fallback — shouldn't normally reach here for HTTP responses.
	if e.Detail != "" {
		return lead + ": " + e.Detail + "."
	}
	if e.Raw != "" {
		return lead + ": " + e.Raw
	}
	return lead + "."
}

// CheckResponse validates an HTTP response. On 2xx it returns the fully-read
// body and a nil error. On any other status it parses the server's error
// message (if any) and returns a humane *APIError.
//
// Callers remain responsible for `defer resp.Body.Close()`.
func CheckResponse(resp *http.Response, op string) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Couldn't %s: failed to read response body (%w)", op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &APIError{
		Op:     op,
		Status: resp.StatusCode,
		Detail: detailForStatus(resp.StatusCode, body),
		Raw:    strings.TrimSpace(string(body)),
	}
}

// extractDetail pulls a human-readable message out of a JSON error body.
// The Drift API uses {"error": "..."} as its primary shape; LogAndRespond
// produces {"status", "message", ...}. We accept both, plus "detail" and
// "reason" as defensive fallbacks.
func extractDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, k := range []string{"error", "message", "detail", "reason"} {
		if v, ok := parsed[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// detailForStatus is extractDetail plus the case it was missing: on a CLIENT
// error, a non-JSON body is the reason itself.
//
// The operator renders a refusal with http.Error, so it goes out as text/plain
// and the api forwards it verbatim — a second free slice really does answer 409
// with the body `only one free hacker slice is allowed per account`. Reading only
// JSON discarded that and left the user with "that conflicts with existing
// state", which names no rule, suggests nothing to do, and reads like a platform
// fault rather than a per-account limit working exactly as designed.
//
// Scoped to 4xx deliberately. A 4xx is the server telling the USER what they did
// wrong, which is precisely the text worth surfacing. A 5xx is the platform being
// broken: those paths already have carefully-worded messages that say nothing
// about which component failed, and a bare "Internal Server Error" spliced into
// one adds noise rather than information.
//
// Handled here rather than at the one handler that refuses: every endpoint using
// http.Error has this shape, and a CLI that understands only its server's current
// content type breaks again the next time one of them differs.
func detailForStatus(status int, body []byte) string {
	if d := extractDetail(body); d != "" {
		if v := resizeViolations(body); v != "" {
			return d + " (" + v + ")"
		}
		return d
	}
	if status < 400 || status >= 500 {
		return ""
	}
	if json.Valid(body) {
		// Structured, but with no field a person can read. Showing the raw JSON
		// would be worse than the generic message it replaces.
		return ""
	}
	return plainTextDetail(strings.TrimSpace(string(body)))
}

// resizeViolations renders the per-resource detail a refused resize already
// carries, or "" for any body that has none.
//
// A refused resize answers with `{"error": "...", "violations": [{resource, used,
// new_limit}]}` — the operator computes exactly which limit would drop below
// current usage, and the configurator uses it to highlight the offending form
// field. The CLI printed only the summary, so a person was told the resize would
// shrink below current usage without being told below WHAT, which leaves them
// guessing at which number to raise.
//
// Best-effort by construction: anything unparseable yields "" and the caller
// still shows the summary. A partial answer is worse than the summary alone.
func resizeViolations(body []byte) string {
	var parsed struct {
		Violations []struct {
			Resource string `json:"resource"`
			Used     int    `json:"used"`
			NewLimit int    `json:"new_limit"`
		} `json:"violations"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Violations) == 0 {
		return ""
	}
	parts := make([]string, 0, len(parsed.Violations))
	for _, v := range parsed.Violations {
		if v.Resource == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d in use, limit would be %d", v.Resource, v.Used, v.NewLimit))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// maxPlainDetail bounds what a non-JSON body may contribute. A refusal from
// Drift is one short sentence; anything longer is a proxy's HTML error page or a
// stack trace, and pasting that into a one-line CLI message is worse than the
// generic text it would replace.
const maxPlainDetail = 300

// plainTextDetail returns a non-JSON body if it reads like a message meant for a
// person, and "" if it looks like machinery.
func plainTextDetail(s string) string {
	if len(s) > maxPlainDetail {
		return ""
	}
	// An HTML or XML page is a gateway talking, not Drift.
	if strings.HasPrefix(s, "<") {
		return ""
	}
	// Multi-line bodies are traces and dumps. http.Error writes exactly one
	// line, so the message this exists for is unaffected.
	if strings.ContainsAny(s, "\n\r") {
		return ""
	}
	// Control characters mean it is not text meant to be read.
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// TransportError wraps a network-level failure (DNS, dial, TLS, timeout) in
// a humane message. Pass the same `op` string you'd give to CheckResponse.
//
// This is the right function to call when DoRequest returns an error —
// the return value is already a formatted, printable error.
func TransportError(op string, err error) error {
	lead := "Couldn't " + op + ": "

	// Session-expired error bubbles up from DoRequest's refresh path.
	// Its message is already polite; just prepend the lead.
	if strings.Contains(err.Error(), "session expired") {
		return fmt.Errorf("%s%s", lead, strings.TrimPrefix(err.Error(), "session expired — "))
	}

	// Timeouts — check both url.Error and the generic net.Error interface.
	// Distinct from unreachable: the platform ANSWERED the connection and then
	// did not finish, so a deploy may have landed even though this failed.
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return errors.New(withCode(lead+"couldn't reach the Drift API (timed out). Is the platform up?", "DRIFT-1007"))
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New(withCode(lead+"couldn't reach the Drift API (timed out). Is the platform up?", "DRIFT-1007"))
	}

	// Connection refused / DNS failure / dial errors.
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp") {
		out := withCode(
			fmt.Sprintf("%scouldn't reach the Drift API. Is the platform up? (%v)", lead, err), "DRIFT-1006")
		// The status page is a different host, so it can often answer when the
		// API cannot — which is exactly the case where the user most needs to
		// hear that the fault is not theirs.
		if note := platformFaultNote(DegradedComponents()); note != "" {
			out += "\n" + note
		}
		return errors.New(out)
	}

	// Unknown — wrap it unchanged so the user still sees something.
	return fmt.Errorf("%s%v", lead, err)
}
