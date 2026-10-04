// edgeratelimit.go — `edge.rate_limit`, the slice's requests per minute.
//
// The file writes a rate as `N/s`, `N/min` or `N/h`; the slice holds a whole
// number of requests per minute, so the rate is converted here and refused
// before anything deploys when it does not convert to one. Present, it is sent
// to `POST /ops/slice/edge/rate-limit`; the tier's ceiling is the server's, and
// its refusal is surfaced with the ceiling it publishes. Absent, the slice keeps
// the rate limit it has.
//
// The retired `atomic.rate_limit` lands here as an alias, so an old file sets
// the rate limit it always declared.
//
// This file also holds the two calls the edge keys share: one request to an
// `/ops/slice/edge/` route, and the plan's live-against-declared list.
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/ondrift/cloud/cli/common"
)

// rateLimitShape is the written form: a whole count over one window.
var rateLimitShape = regexp.MustCompile(`^([0-9]+)/(s|min|h)$`)

// rateLimitRPM converts a declared rate into the requests per minute the slice
// holds. It refuses a rate below one per minute, and a per-hour rate that is
// not a whole number per minute, rather than holding a different rate from the
// one the file states.
func rateLimitRPM(declared any) (int, error) {
	raw, ok := declared.(string)
	if !ok {
		return 0, fmt.Errorf("edge.rate_limit: %v is not a rate; write it as N/s, N/min or N/h, such as \"600/min\"", declared)
	}
	match := rateLimitShape.FindStringSubmatch(raw)
	if match == nil {
		return 0, fmt.Errorf("edge.rate_limit: %q is not a rate; write it as N/s, N/min or N/h, such as \"600/min\"", raw)
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n > math.MaxInt32 {
		return 0, fmt.Errorf("edge.rate_limit: %q is too large", raw)
	}

	var rpm int
	switch match[2] {
	case "s":
		if n > math.MaxInt32/60 {
			return 0, fmt.Errorf("edge.rate_limit: %q is too large", raw)
		}
		rpm = n * 60
	case "min":
		rpm = n
	case "h":
		if n >= 60 && n%60 != 0 {
			return 0, fmt.Errorf("edge.rate_limit: %q is %s requests per minute, and a slice holds a whole number "+
				"per minute; write a multiple of 60/h, such as \"%d/h\" or \"%d/h\"",
				raw, strconv.FormatFloat(float64(n)/60, 'f', -1, 64), n/60*60, (n/60+1)*60)
		}
		rpm = n / 60
	}
	if rpm < 1 {
		return 0, fmt.Errorf("edge.rate_limit: %q is below one request per minute, the smallest rate a slice holds; "+
			"the smallest you can write is \"1/min\" (or \"60/h\")", raw)
	}
	return rpm, nil
}

// preflightEdgeRateLimit runs before the first deploy step, so a rate the slice
// cannot hold refuses the file before any of it is applied.
func preflightEdgeRateLimit(m *Manifest) error {
	if !m.Slice().Has("edge", "rate_limit") {
		return nil
	}
	_, err := rateLimitRPM(m.Slice().Get("edge", "rate_limit"))
	return err
}

// liveRateLimit is the reply of GET /ops/slice/edge/rate-limit. Ceiling is null
// for a slice whose tier has none.
type liveRateLimit struct {
	RPM     int  `json:"rpm"`
	Ceiling *int `json:"ceiling"`
}

// applyEdgeRateLimit sets the slice's rate limit to the declared one. Absent, it
// does nothing.
func applyEdgeRateLimit(m *Manifest) error {
	if !m.Slice().Has("edge", "rate_limit") {
		return nil
	}
	rpm, err := rateLimitRPM(m.Slice().Get("edge", "rate_limit"))
	if err != nil {
		return err
	}
	if _, err := callSliceEdge(m, http.MethodPost, "rate-limit", "set the rate limit", map[string]int{"rpm": rpm}); err != nil {
		return fmt.Errorf("edge.rate_limit: %w%s", err, ceilingNote(err))
	}
	fmt.Printf("  %s rate limit: %d requests per minute\n", common.Check(), rpm)
	return nil
}

// ceilingNote is the line naming the tier's ceiling, read from the refusal's
// `ceiling` field, or "" when the refusal carries none.
func ceilingNote(err error) string {
	var apiErr *common.APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	var body struct {
		Ceiling *int `json:"ceiling"`
	}
	if json.Unmarshal([]byte(apiErr.Raw), &body) != nil || body.Ceiling == nil {
		return ""
	}
	return fmt.Sprintf("\n  this slice's tier allows at most %d requests per minute", *body.Ceiling)
}

// planEdgeRateLimit is the `--plan` section for `edge.rate_limit`: the live rate
// against the declared one.
func planEdgeRateLimit(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "rate_limit") {
		return
	}
	fmt.Fprintf(out, "\n  rate limit (edge.rate_limit)\n")

	declared := m.Slice().Get("edge", "rate_limit")
	rpm, derr := rateLimitRPM(declared)

	var live liveRateLimit
	body, lerr := callSliceEdge(m, http.MethodGet, "rate-limit", "read the rate limit", nil)
	if lerr == nil {
		lerr = json.Unmarshal(body, &live)
	}
	switch {
	case lerr != nil:
		fmt.Fprintf(out, "    %s live      unknown (%v)\n", common.Hint("·"), lerr)
	case live.Ceiling != nil:
		fmt.Fprintf(out, "    %s live      %d requests per minute (tier ceiling %d)\n", common.Hint("·"), live.RPM, *live.Ceiling)
	default:
		fmt.Fprintf(out, "    %s live      %d requests per minute\n", common.Hint("·"), live.RPM)
	}

	if derr != nil {
		fmt.Fprintf(out, "    %s %v\n", common.Hint("!"), derr)
		return
	}
	fmt.Fprintf(out, "    %s declared  %v = %d requests per minute\n", common.Hint("·"), declared, rpm)
	switch {
	case lerr != nil:
	case live.RPM == rpm:
		fmt.Fprintf(out, "    %s unchanged\n", common.Hint("="))
	default:
		fmt.Fprintf(out, "    %s changes %d → %d requests per minute\n", common.Hint("~"), live.RPM, rpm)
	}
	if lerr == nil && live.Ceiling != nil && rpm > *live.Ceiling {
		fmt.Fprintf(out, "    %s above the tier's ceiling of %d requests per minute; the apply will be refused\n",
			common.Hint("!"), *live.Ceiling)
	}
}

// callSliceEdge sends one request to `/ops/slice/edge/<route>` and returns the
// reply body, or the server's refusal.
//
// The slice is named on the request rather than taken from the session's
// active slice: `--plan` reads before the apply makes this file's slice the
// active one, and would otherwise compare against whichever slice this machine
// last used.
func callSliceEdge(m *Manifest, method, route, op string, payload any) ([]byte, error) {
	headers := map[string]string{"X-Slice": m.Name()}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
		headers["Content-Type"] = "application/json"
	}
	resp, err := common.DoRequestWithHeaders(method, common.APIBaseURL+"/ops/slice/edge/"+route, body, headers)
	if err != nil {
		return nil, common.TransportError(op, err)
	}
	defer resp.Body.Close()
	return common.CheckResponse(resp, op)
}

// planListDiff writes the live list against the declared one: `+` for what the
// apply adds, `-` for what it removes, `=` for what it keeps. Both lists are in
// the form the caller compares them in.
func planListDiff(out io.Writer, live, declared []string, liveErr error) {
	if liveErr != nil {
		fmt.Fprintf(out, "    %s live      unknown (%v)\n", common.Hint("·"), liveErr)
		for _, d := range declared {
			fmt.Fprintf(out, "    %s %s\n", common.Hint("·"), d)
		}
		return
	}
	inLive := make(map[string]bool, len(live))
	for _, l := range live {
		inLive[l] = true
	}
	inDeclared := make(map[string]bool, len(declared))
	for _, d := range declared {
		inDeclared[d] = true
	}
	var lines []string
	for _, d := range declared {
		if inLive[d] {
			lines = append(lines, "= "+d)
		} else {
			lines = append(lines, "+ "+d)
		}
	}
	for _, l := range live {
		if !inDeclared[l] {
			lines = append(lines, "- "+l)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i][0] < lines[j][0] })
	for _, line := range lines {
		fmt.Fprintf(out, "    %s\n", line)
	}
	if countPrefix(lines, '=') == len(lines) {
		fmt.Fprintf(out, "    %s\n", common.Hint("unchanged"))
	}
}

// countPrefix counts the lines that start with c.
func countPrefix(lines []string, c byte) int {
	n := 0
	for _, l := range lines {
		if l[0] == c {
			n++
		}
	}
	return n
}
