package slice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// seedSession gives the test a scratch HOME with a session in it.
//
// DoRequest attaches the token from ~/.drift/session.json and fails before the
// request leaves if there is none — so without this a test borrows whatever
// session the machine happens to have, passes on a developer's laptop, and every
// tick fails at the transport in CI.
func seedSession(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatal(err)
	}
}

// pollAgainst points the CLI at a stub configurator and runs the redeem loop
// with the wait removed, so a test measures what the loop DECIDES rather than
// how long it sleeps.
func pollAgainst(t *testing.T, h http.HandlerFunc) (json.RawMessage, error) {
	t.Helper()
	seedSession(t)

	stub := httptest.NewServer(h)
	t.Cleanup(stub.Close)

	prevURL, prevInterval := common.ConfiguratorBaseURL, pollInterval
	common.ConfiguratorBaseURL, pollInterval = stub.URL, 0
	t.Cleanup(func() { common.ConfiguratorBaseURL, pollInterval = prevURL, prevInterval })

	return pollRedeem("resize slice", "a-token")
}

// Nothing on the CLI side counts the session out. Two thousand pending ticks is
// well past the ~450 the old 15-minute cap allowed at the real cadence, and the
// loop is still there.
//
// This pins the ATTEMPT bound only. With the interval driven to zero a
// wall-clock deadline would not fire either, so it cannot see one — that is what
// TestNoWallClockDeadlineIsReintroduced is for, and saying so here keeps this
// test from being read as more than it is.
func TestThePollIsNotBoundedByAttempts(t *testing.T) {
	var ticks atomic.Int32
	result, err := pollAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ticks.Add(1) < 2000 {
			_, _ = w.Write([]byte(`{"status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","result":{"name":"prorata"}}`))
	})

	if err != nil {
		t.Fatalf("the poll gave up on a session the server had not ended: %v", err)
	}
	if len(result) == 0 {
		t.Error("the slice document did not come back")
	}
	if got := ticks.Load(); got < 2000 {
		t.Errorf("the loop stopped after %d ticks, so something other than the server ended it", got)
	}
}

// The server ending the session is what ends the poll. A 410 is the configurator
// saying the session expired, and the CLI has to report that rather than keep
// asking — this is the bound that replaces the wall clock.
func TestAnExpiredSessionEndsThePollAtOnce(t *testing.T) {
	var ticks atomic.Int32
	_, err := pollAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		ticks.Add(1)
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"session expired"}`))
	})

	if err == nil {
		t.Fatal("an expired session was not reported as an error")
	}
	if got := ticks.Load(); got != 1 {
		t.Errorf("the loop asked %d times after being told the session was gone, want 1", got)
	}
}

// A configurator that cannot be reached at all must not spin forever now that
// the wall clock is gone. The server's expiry bounds the happy path; a run of
// unanswered ticks bounds this one.
func TestAnUnreachableConfiguratorGivesUp(t *testing.T) {
	seedSession(t)
	prevURL, prevInterval := common.ConfiguratorBaseURL, pollInterval
	// A port nothing is listening on: every tick fails at the transport.
	common.ConfiguratorBaseURL, pollInterval = "http://127.0.0.1:1", 0
	t.Cleanup(func() { common.ConfiguratorBaseURL, pollInterval = prevURL, prevInterval })

	done := make(chan error, 1)
	go func() {
		_, err := pollRedeem("resize slice", "a-token")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unreachable configurator returned success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the poll never gave up on a configurator it could not reach")
	}
}

// A transient blip is not the server ending the session, so the loop recovers
// from one rather than treating it as an answer.
func TestOneBlipDoesNotEndThePoll(t *testing.T) {
	var ticks atomic.Int32
	result, err := pollAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch ticks.Add(1) {
		case 1:
			// A body that is not JSON at all, as a proxy hiccup would produce.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		_, _ = w.Write([]byte(`{"status":"completed","result":{"name":"prorata"}}`))
	})

	if err != nil {
		t.Fatalf("a single blip ended the poll: %v", err)
	}
	if len(result) == 0 {
		t.Error("the slice document did not come back after the blip")
	}
}

// The wall clock is gone, and a test driving the interval to zero cannot notice
// if it comes back — so this reads the source instead.
//
// The session's lifetime belongs to the configurator, which slides its expiry as
// the user works. Any deadline computed here is a second opinion about when that
// session ends, and it is wrong the moment the two disagree.
func TestNoWallClockDeadlineIsReintroduced(t *testing.T) {
	src, err := os.ReadFile("handoff.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func pollRedeem\(.*?\n\}\n`).Find(src)
	if body == nil {
		t.Fatal("pollRedeem is no longer where this test looks for it")
	}
	for _, banned := range []string{"deadline", "time.Now().Add(", "time.After(", "time.Since("} {
		if strings.Contains(string(body), banned) {
			t.Errorf("pollRedeem uses %q, which is a deadline of its own — the configurator "+
				"decides when a session is over, and it slides that time as the user works",
				banned)
		}
	}
	// Probe the probe: the extraction has to be finding real code.
	if !strings.Contains(string(body), "ops/session/redeem") {
		t.Error("the extracted body is not pollRedeem's, so the check above is vacuous")
	}
}
