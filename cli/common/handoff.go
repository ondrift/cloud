package common

// handoff.go — the browser handoff every verb that changes a slice's shape ends in.
//
// The CLI mints a single-use session against the configurator, opens the user's
// browser at it, and polls a redeem endpoint until the session stops being
// pending. The CLI never sees the SliceConfig the user typed — only the final
// Slice document the configurator forwarded to the api.
//
// It lives here rather than beside `drift slice` because it is not that command's
// property: the configurator owns a slice's shape, so every path that would once
// have written one ends up here. `cmd/slice` imports `cmd/project`, so a caller in
// `cmd/project` — `drift file benchmark`, handing over a measured booking — cannot
// reach a helper that sits in `cmd/slice`. This package is the one both already
// depend on, and it already owns the two things the flow is built from: the
// browser (browser.go) and the configurator's address (session.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/term"
)

// HandoffMode mirrors configurator.routes.SessionMode. The configurator package
// is not imported: the CLI module is not allowed to depend on a server-side
// service module.
type HandoffMode string

// The two flows the configurator renders. Create may arrive with no slice name —
// the form collects it — while resize always names the slice it is changing.
const (
	ModeCreate HandoffMode = "create"
	ModeResize HandoffMode = "resize"
)

// handoffResponse is the configurator's reply to /ops/session/handoff.
type handoffResponse struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

// redeemResponse is the configurator's reply to /ops/session/redeem. Only the
// fields the CLI reads are named — Result stays raw JSON because it is the api's
// Slice document and the CLI does not introspect every field.
type redeemResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// RunBrowserHandoff drives the full browser flow:
//
//  1. POST /ops/session/handoff to mint a session
//  2. open the user's browser at the returned URL
//  3. poll /ops/session/redeem until the session is non-pending
//  4. return the final Slice document or an error
//
// Redeem does NOT delete the session — the browser keeps polling slice status
// after submit, and a session deleted on the CLI's read would 404 every one of
// those. The session goes when its own expiry does, so reading a terminal status
// twice is possible and harmless: the slice already exists and the second read
// says the same thing.
//
// `existing` pre-fills the form and is forwarded as opaque JSON, so a caller can
// hand over a config it has changed without this package learning the shape.
//
// The op string is the lead-in on humane error messages ("create slice",
// "resize slice", "apply the measured bookings").
func RunBrowserHandoff(op, sliceName string, mode HandoffMode, existing any) (json.RawMessage, error) {
	// Refuse a shell that has no browser, BEFORE minting anything.
	//
	// Without this the flow is worst-case in every direction: it mints a
	// single-use session, prints a URL nobody can open, and then polls until the
	// configurator expires it — so CI spends minutes on a form no human was ever
	// going to fill in, and ends with a timeout that describes the symptom.
	//
	// Before the mint rather than after, because a session nobody can redeem is
	// still a session: it occupies the slice's handoff slot until it expires, so
	// the next run — from a real terminal — can meet a conflict caused by a run
	// that never had a browser.
	//
	// stdin, not stdout: the browser flow needs a person at the keyboard, and
	// `drift slice create | tee log` is still interactive. The root command tests
	// the same descriptor to decide between the dashboard and help.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf(
			"Couldn't %s: this needs a browser, and no terminal is attached.\n"+
				"  Configure %q at %s, from a machine you can open a browser on.",
			op, sliceName, ConfiguratorBaseURL)
	}

	body, _ := json.Marshal(map[string]any{
		"slice_name": sliceName,
		"mode":       mode,
		"existing":   existing,
	})

	resp, err := DoJSONRequest(
		http.MethodPost,
		ConfiguratorBaseURL+"/ops/session/handoff",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, TransportError(op, err)
	}
	defer resp.Body.Close()

	respBody, err := CheckResponse(resp, op)
	if err != nil {
		return nil, err
	}

	var hr handoffResponse
	if err := json.Unmarshal(respBody, &hr); err != nil {
		return nil, fmt.Errorf("Couldn't %s: handoff response wasn't valid JSON (%w)", op, err)
	}

	fmt.Println("Opening configurator in your browser...")
	fmt.Println(hr.URL)
	if err := OpenBrowser(hr.URL); err != nil {
		// Browser launch failures are routine in containers, SSH sessions, and
		// headless dev environments. The URL is already printed above; just
		// remind the user to open it themselves.
		fmt.Println("(couldn't launch a browser automatically — open the URL above to continue)")
	}

	return pollRedeem(op, hr.Token)
}

// pollInterval is the gap between redeem calls. A package-level seam so a test
// measures what the loop decides rather than how long it sleeps.
//
// Two seconds because the user has to read a form, fill it in and click submit —
// sub-second polling adds nothing they can perceive and only burns cycles.
var pollInterval = 2 * time.Second

// maxUnansweredPolls bounds the case the server cannot bound: a configurator
// that is unreachable. Sixty consecutive failures is two minutes of no contact
// at the normal cadence, which is far past a blip and well short of leaving a
// terminal hanging.
const maxUnansweredPolls = 60

// pollRedeem hits /ops/session/redeem until the session stops being pending.
//
// THE SERVER DECIDES WHEN IT IS OVER. The loop carried its own 15-minute
// deadline, guessed from a TTL the configurator fixed at mint — so a session
// whose expiry moves outlives the poller, and the user loses a form they were
// still filling in. The configurator answers 410 when a session has expired and
// 404 when it is gone; either ends the loop through CheckResponse, and that is
// the bound.
//
// The only thing left to bound here is a configurator that never answers at all,
// which no server-side expiry can end.
func pollRedeem(op, token string) (json.RawMessage, error) {
	unanswered := 0
	for {
		time.Sleep(pollInterval)

		resp, err := DoRequest(
			http.MethodGet,
			ConfiguratorBaseURL+"/ops/session/redeem?s="+token,
			nil,
		)
		if err != nil {
			// A transient blip is not the server ending the session, so it is
			// retried — but a run of them means nothing is answering, and the
			// session's own expiry can never arrive to stop us.
			unanswered++
			if unanswered >= maxUnansweredPolls {
				return nil, fmt.Errorf(
					"Couldn't %s: the configurator stopped answering (%d attempts). "+
						"Your browser session may still complete — check with `drift slice list`.",
					op, unanswered)
			}
			continue
		}
		unanswered = 0

		body, checkErr := CheckResponse(resp, op)
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		if checkErr != nil {
			return nil, checkErr
		}

		var rr redeemResponse
		if err := json.Unmarshal(body, &rr); err != nil {
			return nil, fmt.Errorf("Couldn't %s: redeem response wasn't valid JSON (%w)", op, err)
		}

		switch rr.Status {
		case "pending":
			continue
		case "completed":
			return rr.Result, nil
		case "failed":
			return nil, fmt.Errorf("Couldn't %s: %s", op, rr.Error)
		case "cancelled":
			return nil, fmt.Errorf("Couldn't %s: the configurator session was cancelled", op)
		default:
			return nil, fmt.Errorf("Couldn't %s: unexpected session status %q", op, rr.Status)
		}
	}
}
