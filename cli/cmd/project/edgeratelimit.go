// edgeratelimit.go — `edge.rate_limit`, the slice's requests per minute, and
// the one request every `edge` key sends.
//
// The file writes a rate as `N/s`, `N/min` or `N/h`; the slice holds a whole
// number of requests per minute, so the compiler converts the rate here and
// refuses one that does not convert. The result is part of the slice's shape
// (the same field as `limits.requests_per_minute`), so it reaches the slice
// with the resize `drift apply` makes, and `drift plan` shows and prices it
// with the rest of the shape.
package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
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

// callSliceEdge sends one request to `/ops/slice/edge/<route>` and returns the
// reply body, or the server's refusal.
//
// The slice is named on the request rather than taken from the session's
// active slice: `drift plan` reads before the apply makes this file's slice the
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
