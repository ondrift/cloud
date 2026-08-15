package common

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withStatusFeed points the health check at a stub for one test.
func withStatusFeed(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := StatusURL
	StatusURL = srv.URL
	t.Cleanup(func() { StatusURL = prev })
}

const feedOK = `[{"name":"API","status":"operational"},{"name":"Routing","status":"operational"}]`
const feedDegraded = `[{"name":"API","status":"operational"},{"name":"Routing","status":"degraded"}]`

// THE case this exists for: a platform fault must not read as the user's code
// being wrong. When a component is down, the failure says which one and says
// plainly whose it is.
func TestAPIError_A5xxNamesTheDegradedComponent(t *testing.T) {
	withStatusFeed(t, http.StatusOK, feedDegraded)

	got := (&APIError{Op: "deploy", Status: 500}).Error()
	if !strings.Contains(got, "Routing") {
		t.Errorf("the degraded component must be named, got: %s", got)
	}
	if !strings.Contains(got, "not your code") {
		t.Errorf("the message must say whose fault it is, got: %s", got)
	}
}

// The control that stops the check becoming an excuse: when everything is
// operational, a 5xx is still a 5xx and nothing is added. Claiming health this
// cannot confirm would be worse than saying nothing.
func TestAPIError_AHealthyPlatformAddsNothing(t *testing.T) {
	withStatusFeed(t, http.StatusOK, feedOK)

	got := (&APIError{Op: "deploy", Status: 500}).Error()
	if strings.Contains(got, "not your code") {
		t.Errorf("nothing is degraded, so no verdict should be offered, got: %s", got)
	}
	if !strings.Contains(got, "DRIFT-1005") {
		t.Errorf("the failure itself must survive unchanged, got: %s", got)
	}
}

// A 4xx is the platform telling the user something TRUE about their request.
// Offering component health there would hand them an excuse for a refusal that
// is correct.
func TestAPIError_A4xxNeverConsultsHealth(t *testing.T) {
	// A feed that would fail the test if it were consulted at all.
	withStatusFeed(t, http.StatusOK, feedDegraded)

	got := (&APIError{Op: "delete slice", Status: 404}).Error()
	if strings.Contains(got, "not your code") {
		t.Errorf("a 4xx must not be excused by platform health, got: %s", got)
	}
}

// Every way the check can fail ends in silence. During an outage the status page
// is itself likely to be unreachable, and a diagnostic that fails loudly while
// diagnosing a failure has made the screen worse.
func TestDegradedComponents_FailsQuietly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"status page is down", http.StatusBadGateway, ""},
		{"body is not JSON", http.StatusOK, "<html>maintenance</html>"},
		{"body is JSON but the wrong shape", http.StatusOK, `{"components":[]}`},
		{"empty body", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withStatusFeed(t, tc.status, tc.body)
			if got := DegradedComponents(); len(got) != 0 {
				t.Errorf("an unusable feed must yield nothing, got %v", got)
			}
		})
	}
}

// More than one degraded component reads as a list rather than a sentence per
// component.
func TestPlatformFaultNote_ListsSeveral(t *testing.T) {
	got := platformFaultNote([]string{"Routing", "Authentication"})
	if !strings.Contains(got, "Routing, Authentication") {
		t.Errorf("both must be named, got: %s", got)
	}
	if !strings.Contains(got, "are not operational") {
		t.Errorf("the plural must agree, got: %s", got)
	}
	if single := platformFaultNote([]string{"Routing"}); !strings.Contains(single, "is not operational") {
		t.Errorf("the singular must agree, got: %s", single)
	}
}

// Nothing degraded means nothing said — the distinction the whole file rests on.
func TestPlatformFaultNote_SaysNothingWhenItKnowsNothing(t *testing.T) {
	if got := platformFaultNote(nil); got != "" {
		t.Errorf("an empty verdict must render nothing, got: %q", got)
	}
}
