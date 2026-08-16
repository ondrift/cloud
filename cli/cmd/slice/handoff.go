// Package lifecycle — browser handoff helpers for `drift slice create` and
// `drift slice resize`.
//
// Both default paths go through here: the CLI mints a session against the
// configurator service, opens the user's browser at a single-use URL, and polls
// a redeem endpoint until the session is finalized. The CLI never sees the
// SliceConfig the user typed in — only the final Slice document the
// configurator forwarded to api/ops/slice/{create,resize}.
//
// Both also take a non-interactive route: --free on create, and --from
// <Driftfile> on resize (see resize.go), which posts directly to the api
// gateway and is the only way to drive a resize from a shell with no browser
// (CI, tests, scripts piped through ssh). It is intentionally less ergonomic
// than the browser flow — the user hand-authors the target shape.
package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// handoffMode mirrors configurator.routes.SessionMode. We do not import
// the configurator package because the CLI module is not allowed to depend
// on a server-side service module.
type handoffMode string

// The two flows the configurator renders. Create may arrive with no slice name
// — the form collects it — while resize always names the slice it is changing.
const (
	modeCreate handoffMode = "create"
	modeResize handoffMode = "resize"
)

// handoffResponse is the configurator's reply to /ops/session/handoff.
type handoffResponse struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

// redeemResponse is the configurator's reply to /ops/session/redeem. We
// only need the fields the CLI actually reads — Status and the embedded
// Result/Error. Result is left as raw JSON because it is the api service's
// Slice document and the CLI does not need to introspect every field.
type redeemResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// runBrowserHandoff drives the full browser flow:
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
// The op string is used for the lead-in on humane error messages
// ("create slice", "resize slice").
func runBrowserHandoff(op, sliceName string, mode handoffMode, existing any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"slice_name": sliceName,
		"mode":       mode,
		"existing":   existing,
	})

	resp, err := common.DoJSONRequest(
		http.MethodPost,
		common.ConfiguratorBaseURL+"/ops/session/handoff",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, common.TransportError(op, err)
	}
	defer resp.Body.Close()

	respBody, err := common.CheckResponse(resp, op)
	if err != nil {
		return nil, err
	}

	var hr handoffResponse
	if err := json.Unmarshal(respBody, &hr); err != nil {
		return nil, fmt.Errorf("Couldn't %s: handoff response wasn't valid JSON (%w)", op, err)
	}

	fmt.Println("Opening configurator in your browser...")
	fmt.Println(hr.URL)
	if err := common.OpenBrowser(hr.URL); err != nil {
		// Browser launch failures are routine in containers, SSH
		// sessions, and headless dev environments. The URL is already
		// printed above; just remind the user to open it themselves.
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

		resp, err := common.DoRequest(
			http.MethodGet,
			common.ConfiguratorBaseURL+"/ops/session/redeem?s="+token,
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

		body, checkErr := common.CheckResponse(resp, op)
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

// sliceNameFrom reads the name out of the api's Slice document. The create
// form may collect the name itself, so this is the only place the CLI learns
// what the slice ended up being called.
func sliceNameFrom(raw json.RawMessage) string {
	var s struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s.Name
}

// printSliceSummary prints a one-line confirmation for the user.
func printSliceSummary(verb string, raw json.RawMessage) {
	var s struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.Name == "" {
		fmt.Printf("Slice %s.\n", verb)
		return
	}
	label := "configured"
	if s.Tier == "hacker" {
		label = "free"
	}
	fmt.Printf("Slice '%s' %s (%s).\n", s.Name, verb, label)
}
