package common

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// NewAPIError is what CheckResponse builds a failure with. A caller that has
// already read the body, because it had to know what the answer was before it
// could say what went wrong, builds the same failure from the pieces it holds.

// A throttle: the platform's Retry-After is the wait, the reason leads, and
// nothing in it advises resizing.
func TestNewAPIError_AThrottleCarriesItsWait(t *testing.T) {
	header := http.Header{"Retry-After": {"60"}}
	body := []byte(`{"error":"too many login attempts, try again later"}`)

	e := NewAPIError("log in", http.StatusTooManyRequests, header, body)

	if e.Op != "log in" || e.Status != http.StatusTooManyRequests {
		t.Errorf("Op, Status = %q, %d, want %q, %d", e.Op, e.Status, "log in", http.StatusTooManyRequests)
	}
	if e.Detail != "too many login attempts, try again later" {
		t.Errorf("Detail = %q, want the platform's reason", e.Detail)
	}
	if e.RetryAfter != time.Minute {
		t.Errorf("RetryAfter = %v, want 1m from the header", e.RetryAfter)
	}
	if got := e.code(); got != "DRIFT-1014" {
		t.Errorf("code() = %q, want DRIFT-1014", got)
	}
	got := e.Error()
	for _, want := range []string{"Couldn't log in: too many login attempts, try again later", "try again in 60 seconds"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "resize") {
		t.Errorf("a throttle must not send anyone to resize a slice:\n%s", got)
	}
}

// A refusal that is a quota: the ceiling comes from the response's fields, and
// with no Retry-After the 429 and the 402 are both a plan limit.
func TestNewAPIError_AQuotaCarriesItsCeiling(t *testing.T) {
	body := []byte(`{"error":"limit reached for atomic.deploy_function (current: 6, max: 6)",` +
		`"action":"atomic.deploy_function","limit":6,"current":6}`)

	e := NewAPIError("deploy", http.StatusPaymentRequired, http.Header{}, body)

	if !e.HasLimit || e.Limit != 6 || e.Current != 6 || e.Action != "atomic.deploy_function" {
		t.Errorf("the ceiling did not parse: HasLimit=%v Limit=%d Current=%d Action=%q",
			e.HasLimit, e.Limit, e.Current, e.Action)
	}
	if e.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want none: the platform sent no Retry-After", e.RetryAfter)
	}
	if got := e.code(); got != "DRIFT-1004" {
		t.Errorf("code() = %q, want DRIFT-1004", got)
	}
	got := e.Error()
	for _, want := range []string{"atomic.deploy_function: 6 of 6 used.", "DRIFT-1004"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not say %q:\n%s", want, got)
		}
	}
}

// A ceiling the platform writes with every field carries all of them onto the failure: what
// it limits, the unit of its figures and that nothing raises it.
func TestNewAPIError_ACeilingCarriesWhatItsUnitAndWhetherItIsFixed(t *testing.T) {
	body := []byte(`{"error":"the cache holds 64 MiB of the 64 MiB it allows","action":"backbone.set_cache_value",` +
		`"what":"cache size","limit":67108864,"current":67108864,"unit":"bytes","fixed":true}`)

	e := NewAPIError("set the cache value", http.StatusTooManyRequests, http.Header{}, body)

	if !e.HasLimit || !e.HasCurrent || e.Limit != 67108864 || e.Current != 67108864 {
		t.Errorf("the figures did not carry: HasLimit=%v HasCurrent=%v Limit=%d Current=%d",
			e.HasLimit, e.HasCurrent, e.Limit, e.Current)
	}
	if e.Action != "backbone.set_cache_value" || e.What != "cache size" || e.Unit != "bytes" || !e.Fixed {
		t.Errorf("the words did not carry: Action=%q What=%q Unit=%q Fixed=%v", e.Action, e.What, e.Unit, e.Fixed)
	}
}

// The header is the response's own, and a caller with none passes nil: that reads
// as a platform that stated no wait and no version.
func TestNewAPIError_ANilHeaderStatesNothing(t *testing.T) {
	e := NewAPIError("delete function", http.StatusNotFound, nil, []byte(`{"error":"function not found"}`))

	if e.RetryAfter != 0 || e.PlatformAPIVersion != 0 {
		t.Errorf("RetryAfter, PlatformAPIVersion = %v, %d, want neither", e.RetryAfter, e.PlatformAPIVersion)
	}
	if e.Detail != "function not found" || e.code() != "DRIFT-1003" {
		t.Errorf("Detail, code() = %q, %q, want the reason and DRIFT-1003", e.Detail, e.code())
	}
}

// CheckResponse is NewAPIError after it has read the body: for every kind of
// answer the two build the same failure, field for field. Retry-After is given in
// seconds, the form that does not depend on the clock.
func TestCheckResponse_BuildsWhatNewAPIErrorBuilds(t *testing.T) {
	ahead := http.Header{}
	ahead.Set(APIVersionHeader, "2")
	throttle := http.Header{"Retry-After": {"60"}}

	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		body   string
	}{
		{"a refusal with a reason", http.StatusForbidden, nil, `{"error":"not the owner"}`},
		{"a bare not found from a platform that is ahead", http.StatusNotFound, ahead, `{"error":"not found"}`},
		{"a throttle", http.StatusTooManyRequests, throttle, `{"error":"too many calls"}`},
		{"a quota", http.StatusPaymentRequired, nil, `{"error":"limit reached","action":"slices","limit":1,"current":1}`},
		{"a plain text conflict", http.StatusConflict, nil, "the free tier is one slice per account\n"},
		{"a fault with no JSON", http.StatusInternalServerError, nil, "Internal Server Error"},
		{"a resize that would shrink below usage", http.StatusConflict, nil,
			`{"error":"too small","violations":[{"resource":"atomic.functions","used":5,"new_limit":1}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     tc.header,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			_, err := CheckResponse(resp, "do the thing")
			got, ok := err.(*APIError)
			if !ok {
				t.Fatalf("CheckResponse returned %T, want *APIError", err)
			}
			want := NewAPIError("do the thing", tc.status, tc.header, []byte(tc.body))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("CheckResponse built\n  %+v\nand NewAPIError built\n  %+v", got, want)
			}
		})
	}
}
