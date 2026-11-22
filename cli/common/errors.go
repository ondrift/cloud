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
//	    return common.TransportError("create slice", err)
//	}
//	defer resp.Body.Close()
//
//	respBody, err := common.CheckResponse(resp, "create slice")
//	if err != nil {
//	    return err
//	}
//	// ... use respBody
//
// # A HANDLER RETURNS ITS ERROR AND DOES NOT PRINT IT
//
// The example above used to print and then return, and so did a hundred-odd
// call sites — while `main()` prints whatever a command returns. So every
// failing command said the same thing twice, once on stdout from the handler
// and once on stderr from main, which reads as a bug in the tool:
//
//	$ drift backbone nosql read --key definitely-not-there
//	Couldn't read document: not found.
//	  DRIFT-1003 · drift doctor explain DRIFT-1003
//	Couldn't read document: not found.
//	  DRIFT-1003 · drift doctor explain DRIFT-1003
//
// One convention, and it settles both halves: the handler RETURNS, main prints
// once to stderr and exits 1. An error belongs on stderr, and returning is also
// what makes the process exit non-zero — several commands were cobra `Run`
// handlers with no error channel at all, so a refused delete or a failed deploy
// exited 0 and every `&&` after it carried on.
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIError is the humane rendering of a non-2xx response from the Drift API.
// It implements the error interface; a handler should just `return err` and let
// main print it — see the convention on the package comment above.
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

	// Action, What, Limit, Current, Unit and Fixed are a refusal's ceiling, taken from
	// the response FIELDS rather than parsed out of Detail.
	//
	// The server is the only thing that knows a quota, so it is the only thing
	// allowed to state one: a ceiling the CLI re-derived is a ceiling the CLI can
	// be wrong about, and being confidently wrong about someone's limit is worse
	// than saying nothing. HasLimit distinguishes "the server published no
	// ceiling" from "the ceiling is zero".
	//
	// What is the thing limited in a person's words ("function slots") and Action the
	// token for what was asked ("atomic.deploy_function"). Unit is "bytes" when Limit
	// and Current are sizes, and empty when they are counts. HasCurrent says the server
	// published what the slice holds against the ceiling: a cap on one item holds
	// nothing, so it publishes no current, and an absent current is not a zero. Fixed
	// says no change to the slice's shape raises the ceiling.
	Action     string
	What       string
	Limit      int
	Current    int
	Unit       string
	HasLimit   bool
	HasCurrent bool
	Fixed      bool

	// RetryAfter is how long the server asked the caller to wait, from the
	// response's Retry-After header, or zero when it sent none.
	//
	// It is what tells a THROTTLE from a QUOTA. Both are a 429 and their remedies
	// are opposite: a throttle (too many logins, too many control-plane calls in a
	// minute, a restart inside its cooldown) clears by itself, while a quota stays
	// until something is freed or the slice is resized. The platform sends
	// Retry-After on every throttle and on no quota, so its presence is the whole
	// test and no sentence has to be read.
	RetryAfter time.Duration

	// CredentialRefused says a 401 on this request is the platform turning down a
	// credential the person just presented, a password typed or a proof of
	// identity handed over, and not a session that ran out. Its message is the
	// platform's own reason and it has no code: the code for a 401 sends the person
	// to log in, which is no remedy for a credential that was refused. A refused
	// login is one by its op and sets nothing.
	CredentialRefused bool

	// PlatformAPIVersion is the route-contract major the platform stated on this
	// reply, or 0 when it stated none or stated something that is not a plain
	// integer.
	//
	// It is what tells a PATH THAT MOVED from a THING THAT IS MISSING. Both are a
	// 404 and their remedies are opposite: a missing thing is the caller's to find
	// or create, while a path the platform no longer serves is cured by upgrading
	// this binary. Only a platform a major ahead of this binary can have dropped a
	// path it knows, so only that platform's bare 404 is read as the second.
	PlatformAPIVersion int

	// RefusedBy names the gate that refused a deploy, read from the response
	// field of the same name and never from the status alone: a 422 is used for
	// other refusals too, and nothing here guesses from wording. "start_check" is
	// the one gate this binary knows: a function's new version did not start, so
	// the slice kept the one that was running and never replaced it.
	RefusedBy string

	// FailureTail is the last lines of the refused process's own stderr, from
	// the response's "detail" field. It is what the function itself said, not a
	// sentence the platform composed, so it is shown as the process wrote it.
	FailureTail string

	// StillRunningKnown says the response named a version that kept serving
	// (StillRunningDigest, StillRunningAge) rather than naming none: a first
	// deploy that is refused has nothing still running, and the two cases read
	// differently to the person waiting for an answer.
	StillRunningKnown bool
	// StillRunningDigest is the digest of the version that is still running.
	StillRunningDigest string
	// StillRunningAge is how long ago that version was deployed.
	StillRunningAge time.Duration
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
	// ErrSessionRevoked — the platform answered, and said the session was ended
	// on purpose: a refresh arrived from a different machine than the one that
	// logged in, or with a token that had already been used. That is its response
	// to a stolen session file, and it cuts every session on the account.
	ErrSessionRevoked = errors.New("session revoked")
)

// SessionRevokedMessage is what a person sees when the platform ended their
// sessions as a theft response. It differs from an ordinary expiry in the one
// way that matters: the next step is not only to log in, because someone else
// may be holding the session file.
const SessionRevokedMessage = "your session was ended on purpose: a refresh arrived from a different machine " +
	"than the one you logged in on, or with a token that had already been used, which Drift treats as a " +
	"stolen session file, so every session on this account was cut. " +
	"Run 'drift account login' to get back in. " +
	"If that was not you on another machine, also run 'drift account reset-password'."

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
// The code is chosen from the STATUS and from the ceiling the platform published,
// never from the message, because the status and the fields are what the platform
// actually said: a message is rewritten as wording improves, and a code that moved
// with the wording would not be stable enough to paste. A status with no registered
// code renders exactly as before.
func (e *APIError) Error() string {
	out := withCode(e.message(), e.code())
	// The numbers, on their own line, from the server's fields. A refusal that
	// does not say what the ceiling is leaves the user to guess how much to free
	// up, which is a second command away at best.
	if e.HasLimit {
		out += "\n  " + e.numbersLine()
	}
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

// FailureCode is the code this failure carries, which CodeOf reads.
func (e *APIError) FailureCode() string { return e.code() }

// code names this failure. Every status from 400 to 599 has one, except a 401 that
// refuses a credential other than a login's, whose message is the platform's own
// reason and whose person is the one to act on it. A throttle is named before a
// quota, so a 429 with a Retry-After is a throttle whatever else its body carries.
func (e *APIError) code() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		// A refused credential is not an expired session — the message already says
		// what was refused, and pointing at `account login` would send the user to
		// the command they just ran, or to one that cannot help. A refused login has
		// a code of its own for that reason.
		if e.Op == "log in" {
			return "DRIFT-1017"
		}
		if e.refusesCredential() {
			return ""
		}
		return "DRIFT-1001"
	case e.Status == http.StatusForbidden:
		return "DRIFT-1002"
	case e.pathMayHaveMoved():
		return "DRIFT-1015"
	case e.Status == http.StatusNotFound:
		return "DRIFT-1003"
	case e.throttled():
		return "DRIFT-1014"

	// A function refused by the start check is named by the gate in
	// RefusedBy, never by the bare 422: that status answers other refusals
	// too, and nothing here reads it from the message's wording.
	case e.refusedByStartCheck():
		return "DRIFT-1023"

	// A refusal at a ceiling is named by the ceiling and not by the status the
	// platform chose for it: a bucket that is full answers 413 and the member cap
	// 409, and neither is an invalid request or a conflict. A ceiling the platform
	// fixes has a code of its own, because the quota's remedy is to raise the limit
	// and nothing raises this one.
	case e.fixedCeiling():
		return "DRIFT-1021"
	case e.atCeiling() || e.Status == http.StatusPaymentRequired || e.Status == http.StatusTooManyRequests:
		return "DRIFT-1004"

	// A CONFLICT carries a code of its own. It used to fall through to "" — so a
	// 409 was alone among its neighbours in offering no `drift doctor explain`
	// pointer, on a status whose whole content is "something already exists and
	// the message says which".
	case e.Status == http.StatusConflict:
		return "DRIFT-1013"

	// 502/503/504 ARE NOT DRIFT-1005, and the split is the point rather than a
	// tidier taxonomy.
	//
	// DRIFT-1005's documented remedy says "Nothing you changed caused it" and
	// sends the reader to status.ondrift.eu. For an unavailable platform both
	// sentences are routinely FALSE: a resize restarts the slice's pod, so a
	// deploy issued seconds later hits a runner that is gone — the user caused
	// it, from the same terminal, and every component on the status page is
	// green. Observed on alpha in exactly that order.
	//
	// message() already told these three apart; only the code did not, which
	// meant the sentence the user was pointed at contradicted the sentence they
	// had just been shown.
	case e.Status == http.StatusBadGateway ||
		e.Status == http.StatusServiceUnavailable ||
		e.Status == http.StatusGatewayTimeout:
		return "DRIFT-1012"

	case e.Status >= 500:
		return "DRIFT-1005"

	// Any other refusal is the platform saying the request itself is not
	// acceptable. 400 is the commonest answer to a bad request body, and 405, 410,
	// 413, 415 and 422 say the same thing about the method, the path's age, the size,
	// the format and the content.
	case e.Status >= 400 && e.Status < 500:
		return "DRIFT-1016"
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
		if e.refusesCredential() {
			if e.Detail != "" {
				return lead + ": " + e.Detail + "."
			}
			if e.Op == "log in" {
				return lead + ": invalid username or password."
			}
			return lead + ": it was not accepted."
		}
		return lead + ": your session expired. Run 'drift account login' to re-authenticate."

	case e.Status == http.StatusForbidden:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": you don't have permission to do that."

	// "May", because a bare 404 relayed from a slice carries the same words as the
	// api's own route miss: see isBareNotFound.
	case e.pathMayHaveMoved():
		return lead + ": the platform may no longer answer that request. " + NewerPlatformNotice(e.PlatformAPIVersion)

	case e.Status == http.StatusNotFound:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that resource wasn't found."

	// A throttle says when, and nothing about capacity: resizing changes what a
	// slice costs and does not lift a rate limit.
	case e.throttled():
		reason := "too many requests"
		if e.Detail != "" {
			reason = e.Detail
		}
		return lead + ": " + reason + ". This clears by itself; try again in " + humanWait(e.RetryAfter) + "."

	// A function the deploy shipped failed its own start check: what was asked
	// (lead, from Op), that the new version never replaced the one that was
	// running, the last lines of the process's own failure, and what still
	// serves.
	case e.refusedByStartCheck():
		return e.startCheckMessage(lead)

	// A refusal at a ceiling says what stopped it in the platform's own sentence,
	// whatever status the platform chose for it, and then where to raise the limit,
	// unless nothing raises it. A 402 and a 429 are this refusal even when the body
	// publishes no ceiling.
	case e.atCeiling() || e.Status == http.StatusPaymentRequired || e.Status == http.StatusTooManyRequests:
		return lead + ": " + e.ceilingReason() + "." + e.raiseAdvice()

	case e.Status == http.StatusConflict:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that conflicts with existing state."

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
		return nil, Failf("DRIFT-1020", "Couldn't %s: failed to read response body (%w)", op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, NewAPIError(op, resp.StatusCode, resp.Header, body)
}

// NewAPIError builds the failure for a response that is not 2xx, from what a
// caller holds once it has read the body itself: the op it was attempting, the
// status, the header and the body. CheckResponse is this construction after it
// reads the body, so a failure built here is the one it would have built.
//
// It is for a caller that has to read the body before it knows what the answer
// is, such as a 401 that may be a refusal to be acted on. A caller that has not
// read it calls CheckResponse. The header may be nil.
func NewAPIError(op string, status int, header http.Header, body []byte) *APIError {
	e := &APIError{
		Op:     op,
		Status: status,
		Detail: detailForStatus(status, body),
		Raw:    strings.TrimSpace(string(body)),
	}
	q := quotaFromBody(body)
	e.Action, e.What, e.Limit, e.Current, e.Unit = q.Action, q.What, q.Limit, q.Current, q.Unit
	e.HasLimit, e.HasCurrent, e.Fixed = q.HasLimit, q.HasCurrent, q.Fixed
	e.RetryAfter = retryAfter(header.Get("Retry-After"), time.Now())
	e.PlatformAPIVersion = platformAPIVersion(header)
	e.RefusedBy, e.FailureTail, e.StillRunningKnown, e.StillRunningDigest, e.StillRunningAge =
		startCheckFromBody(body, time.Now())
	return e
}

// refusesCredential reports whether a 401 is the platform turning down a
// credential and not a session that ran out: a refused login by its op, and any
// other request by CredentialRefused.
func (e *APIError) refusesCredential() bool {
	return e.CredentialRefused || e.Op == "log in"
}

// pathMayHaveMoved reports whether this refusal is a request for a path the
// platform no longer serves, rather than for a thing that does not exist: a bare
// 404 from a platform whose route contract is ahead of this binary's.
func (e *APIError) pathMayHaveMoved() bool {
	return e.Status == http.StatusNotFound &&
		platformAhead(e.PlatformAPIVersion) &&
		isBareNotFound(e.Detail)
}

// throttled reports whether this refusal is a rate limit that clears by itself:
// a 429 that says when to retry.
func (e *APIError) throttled() bool {
	return e.Status == http.StatusTooManyRequests && e.RetryAfter > 0
}

// refusedByStartCheck reports whether this refusal is a deploy whose new
// version failed to start: RefusedBy names the gate, read from the response
// field and never from the bare status, which a 422 also carries for other
// refusals.
func (e *APIError) refusedByStartCheck() bool {
	return e.RefusedBy == "start_check"
}

// startCheckMessage renders a start-check refusal: that the new version never
// replaced the one that was running, the last lines of the process's own
// failure, and what still serves.
func (e *APIError) startCheckMessage(lead string) string {
	out := lead + ": the new version failed its start check, and did not replace the version that was running."
	if tail := strings.TrimSpace(e.FailureTail); tail != "" {
		out += "\n" + indentLines(tail)
	}
	out += "\n" + e.stillRunningLine()
	return out
}

// stillRunningLine says what kept serving a refused deploy: a short digest and
// how long ago it was deployed, or that nothing was running, for a first
// deploy that was refused.
func (e *APIError) stillRunningLine() string {
	if !e.StillRunningKnown {
		return "Nothing was running before this deploy."
	}
	return fmt.Sprintf("Still running: %s, deployed %s ago.", shortDigest(e.StillRunningDigest), humanWait(e.StillRunningAge))
}

// indentLines indents every line of s by two spaces, so a process's own
// stderr reads as something quoted rather than part of the CLI's own sentence.
func indentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// shortDigest shortens a content digest ("sha256:deadbeef...") to the twelve
// characters a person can compare at a glance, its algorithm prefix dropped.
func shortDigest(digest string) string {
	if i := strings.IndexByte(digest, ':'); i >= 0 {
		digest = digest[i+1:]
	}
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return digest
}

// atCeiling reports whether this refusal is the platform stopping a request at a
// ceiling it published, whatever status it chose for it: a count answers 429 except
// the member cap, which answers 409, and a size answers 413. A 5xx is the platform
// failing and not a refusal, so it is none.
//
// It is asked after a 401, a 403, a 404 and a throttle have been named, in code() and
// in message() alike: each of those keeps its own code and sentence whatever numbers
// its body carries, and a throttle is a rate and not a capacity.
func (e *APIError) atCeiling() bool {
	return e.HasLimit && e.Status >= 400 && e.Status < 500
}

// fixedCeiling reports whether this refusal is at a ceiling that no change to the
// slice's shape raises.
func (e *APIError) fixedCeiling() bool {
	return e.atCeiling() && e.Fixed
}

// ceilingReason is the platform's own sentence for a refusal at a ceiling, and
// the CLI's where the platform sent none.
func (e *APIError) ceilingReason() string {
	switch {
	case e.Detail != "":
		return e.Detail
	case e.fixedCeiling():
		return "that is over a limit Drift sets for every slice"
	}
	return "you're at your plan limit"
}

// The two sentences that end a quota refusal, each saying where the limit is raised.
// A slice a Driftfile declares is raised in the file, because `drift apply` makes the
// slice the shape the file says and undoes a limit raised on the resize form. Any other
// slice is raised on that form.
const (
	QuotaDriftfileMessage = "Raise it in the Driftfile, then run 'drift apply'."
	QuotaResizeMessage    = "Run 'drift slice resize' to add capacity."
)

// raiseAdvice ends a refusal at a ceiling with where to raise the limit, led by the
// space that joins it to the sentence before. It is empty for a ceiling the platform
// fixes, because nothing the person can run raises it.
//
// The Driftfile is named when the slice this command acts on is the one a Driftfile
// declares, found as every command finds its slice. The check is the working
// directory's and not the request's, so a command that names another slice by
// argument is advised as if it acted on the project's. DRIFT_SLICE names a slice the
// file need not declare, and gets the resize form.
func (e *APIError) raiseAdvice() string {
	switch {
	case e.fixedCeiling():
		return ""
	case actsOnDriftfileSlice():
		return " " + QuotaDriftfileMessage
	}
	return " " + QuotaResizeMessage
}

// unitBytes is the unit the platform names for a ceiling that is a size. A ceiling
// that is a count has no unit.
const unitBytes = "bytes"

// numbersLine says where the person stands against the ceiling, in the words the
// platform's fields give. What names the limit, and Action stands in for a platform
// that sends no What. A ceiling that is a stock the slice holds reads "function
// slots: 6 of 6 used."; a cap on one item holds nothing, and reads "secret size:
// limit is 1 KiB.", because a current that is absent is not a zero.
func (e *APIError) numbersLine() string {
	what := e.What
	if what == "" {
		what = e.Action
	}
	if what == "" {
		what = "this resource"
	}
	if !e.HasCurrent {
		return fmt.Sprintf("%s: limit is %s.", what, e.figure(e.Limit))
	}
	return fmt.Sprintf("%s: %s of %s used.", what, e.figure(e.Current), e.figure(e.Limit))
}

// figure writes one of the ceiling's numbers: a size as KiB, MiB or GiB, and any
// other as the plain count.
func (e *APIError) figure(n int) string {
	if e.Unit == unitBytes {
		return FormatBytes(int64(n))
	}
	return strconv.Itoa(n)
}

// retryAfter reads a Retry-After header in either form RFC 9110 allows, a
// number of seconds or an HTTP date, and returns zero for a missing,
// malformed or past value: a wait the CLI cannot state is no wait.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// humanWait states a wait the way a person says it, rounded up so the user
// never retries early: "30 seconds", "1 minute", "5 minutes", "24 hours".
func humanWait(d time.Duration) string {
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(n, 10) + " " + one + "s"
	}
	secs := int64(math.Ceil(d.Seconds()))
	switch {
	case secs < 90:
		return unit(secs, "second")
	case secs < 90*60:
		return unit((secs+59)/60, "minute")
	default:
		return unit((secs+3599)/3600, "hour")
	}
}

// quota is what a refusal says about its ceiling, as the response's fields carry it.
// The fields of APIError that hold a ceiling are these, one for one.
type quota struct {
	Action     string
	What       string
	Limit      int
	Current    int
	Unit       string
	HasLimit   bool
	HasCurrent bool
	Fixed      bool
}

// quotaFromBody reads a refusal's ceiling out of the response FIELDS.
//
// Never out of Detail: the sentence carries the same figures, and parsing them
// back out would make the CLI's rendering depend on the server's wording — the
// exact coupling that publishing fields removes.
//
// A body with no limit yields HasLimit=false, and callers then say nothing about
// limits. That is the honest reading of an older platform, or of an action with
// no quota at all. A ceiling that was not published has no current, unit or fixed
// either, so those are dropped with it: a `fixed` beside no limit is not a fixed
// ceiling. An absent current is not a zero, and HasCurrent keeps the two apart.
func quotaFromBody(body []byte) quota {
	var parsed struct {
		Action  string `json:"action"`
		What    string `json:"what"`
		Limit   *int   `json:"limit"`
		Current *int   `json:"current"`
		Unit    string `json:"unit"`
		Fixed   bool   `json:"fixed"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return quota{}
	}
	q := quota{Action: parsed.Action, What: parsed.What}
	if parsed.Limit == nil {
		return q
	}
	q.Limit, q.HasLimit, q.Unit, q.Fixed = *parsed.Limit, true, parsed.Unit, parsed.Fixed
	if parsed.Current != nil {
		q.Current, q.HasCurrent = *parsed.Current, true
	}
	return q
}

// startCheckFromBody reads a start-check refusal's fields: the gate that
// refused, the last lines of the process's own stderr, and which version kept
// serving, read as the response's fields published it rather than as a count
// re-derived from them.
//
// still_running is absent from a body that is not a start-check refusal, so
// a response with no refused_by also has known=false, which is the honest
// reading of "this is not that kind of refusal" rather than "nothing was
// running".
func startCheckFromBody(body []byte, now time.Time) (refusedBy, tail string, known bool, digest string, age time.Duration) {
	var parsed struct {
		RefusedBy    string `json:"refused_by"`
		Detail       string `json:"detail"`
		StillRunning *struct {
			Digest     string `json:"digest"`
			DeployedAt string `json:"deployed_at"`
		} `json:"still_running"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", false, "", 0
	}
	refusedBy, tail = parsed.RefusedBy, parsed.Detail
	if parsed.StillRunning == nil {
		return refusedBy, tail, false, "", 0
	}
	deployedAt, err := time.Parse(time.RFC3339Nano, parsed.StillRunning.DeployedAt)
	if err != nil {
		return refusedBy, tail, false, "", 0
	}
	return refusedBy, tail, true, parsed.StillRunning.Digest, now.Sub(deployedAt)
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
// with the body `the free tier is one slice per account, and you already have
// one. …`. Reading only JSON discarded that and left the user with "that
// conflicts with existing state", which names no rule, suggests nothing to do,
// and reads like a platform fault rather than a per-account limit working
// exactly as designed.
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
// current usage. Rendering it is what tells a person WHAT the resize would
// shrink below; the summary alone leaves them guessing at which number to raise.
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
	// Its message is already polite; just prepend the lead. The code it carries
	// goes with it: the text keeps the code's line, and Coded names the failure
	// for CodeOf.
	if strings.Contains(err.Error(), "session expired") {
		return Coded(CodeOf(err), errors.New(lead+strings.TrimPrefix(err.Error(), "session expired — ")))
	}

	// Timeouts — check both url.Error and the generic net.Error interface.
	// Distinct from unreachable: the platform ANSWERED the connection and then
	// did not finish, so a deploy may have landed even though this failed.
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return Failf("DRIFT-1007", "%s", lead+"couldn't reach the Drift API (timed out). Is the platform up?")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return Failf("DRIFT-1007", "%s", lead+"couldn't reach the Drift API (timed out). Is the platform up?")
	}

	// Connection refused / DNS failure / dial errors.
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp") {
		out := withCode(
			fmt.Sprintf("%scouldn't reach the Drift API. Is the platform up? (%v)", lead, err), "DRIFT-1006")
		// The feed is the API's own, so when the API does not answer it usually
		// does not either, and the note is then silent. When the API answers the
		// feed but refused this request, it is the case where the user most needs
		// to hear that the fault is not theirs.
		if note := platformFaultNote(DegradedComponents()); note != "" {
			out += "\n" + note
		}
		// The note follows the code's line, and Coded leaves a line that is there
		// where it is.
		return Coded("DRIFT-1006", errors.New(out))
	}

	// A failure that already carries its code is wrapped with the lead and nothing
	// else, so the code it was given stays the one CodeOf reads and the line prints
	// once.
	if CodeOf(err) != "" {
		return fmt.Errorf("%s%w", lead, err)
	}

	// Unknown — the original text follows the lead unchanged, so the user still
	// sees what the connection reported, under the code for a failure this does not
	// recognise.
	return Failf("DRIFT-1020", "%s%v", lead, err)
}
