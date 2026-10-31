package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func answer(status int, warming bool, retryAfter string) *http.Response {
	h := http.Header{}
	if warming {
		h.Set(WarmingHeader, "1")
	}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
}

// noWaiting lets a test run the wait without sleeping or writing to the terminal,
// and records the pauses it asked for.
func noWaiting(t *testing.T) (*[]time.Duration, *bytes.Buffer) {
	t.Helper()
	var pauses []time.Duration
	var out bytes.Buffer
	oldSleep, oldNotice := warmingSleep, warmingNotice
	warmingSleep = func(_ context.Context, d time.Duration) error { pauses = append(pauses, d); return nil }
	warmingNotice = &out
	t.Cleanup(func() { warmingSleep, warmingNotice = oldSleep, oldNotice })
	return &pauses, &out
}

// A slice coming back from a cooldown is waited for, and the person is told once.
func TestWaitWhileWarming_WaitsOutAWarmingSlice(t *testing.T) {
	pauses, out := noWaiting(t)
	calls := 0
	resp, err := waitWhileWarming(t.Context(), func() (*http.Response, error) {
		calls++
		if calls < 3 {
			return answer(http.StatusServiceUnavailable, true, "7"), nil
		}
		return answer(http.StatusOK, false, ""), nil
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("got %v, %v; want the 200 once the slice is warm", resp, err)
	}
	if calls != 3 {
		t.Errorf("sent %d times, want 3", calls)
	}
	if len(*pauses) != 2 || (*pauses)[0] != 7*time.Second {
		t.Errorf("pauses = %v, want two of the 7 seconds the platform asked for", *pauses)
	}
	if strings.Count(out.String(), "cooled down") != 1 {
		t.Errorf("the notice was written %d times, want once:\n%s", strings.Count(out.String(), "cooled down"), out.String())
	}
}

// A 503 that is not the slice warming is a real 503, handed back at once.
func TestWaitWhileWarming_LeavesAnOrdinary503Alone(t *testing.T) {
	pauses, _ := noWaiting(t)
	calls := 0
	resp, _ := waitWhileWarming(t.Context(), func() (*http.Response, error) {
		calls++
		return answer(http.StatusServiceUnavailable, false, "7"), nil
	})
	if calls != 1 || resp.StatusCode != http.StatusServiceUnavailable || len(*pauses) != 0 {
		t.Errorf("an ordinary 503 was retried: %d sends, pauses %v", calls, *pauses)
	}
}

// A warm-up that does not finish is not waited for forever.
func TestWaitWhileWarming_GivesUpAfterTheLimit(t *testing.T) {
	pauses, _ := noWaiting(t)
	calls := 0
	resp, _ := waitWhileWarming(t.Context(), func() (*http.Response, error) {
		calls++
		return answer(http.StatusServiceUnavailable, true, "30"), nil
	})
	if !IsWarming(resp) {
		t.Fatalf("after the limit the caller should see the 503, got %v", resp)
	}
	var total time.Duration
	for _, p := range *pauses {
		total += p
	}
	if total > warmingMaxWait {
		t.Errorf("waited %s, more than the limit %s", total, warmingMaxWait)
	}
	if calls < 2 {
		t.Errorf("sent %d times, expected it to try again at least once", calls)
	}
}

// The wait honours the command being cancelled.
func TestWaitWhileWarming_StopsWhenCancelled(t *testing.T) {
	old := warmingSleep
	warmingSleep = func(context.Context, time.Duration) error { return context.Canceled }
	t.Cleanup(func() { warmingSleep = old })
	oldN := warmingNotice
	warmingNotice = io.Discard
	t.Cleanup(func() { warmingNotice = oldN })

	_, err := waitWhileWarming(t.Context(), func() (*http.Response, error) {
		return answer(http.StatusServiceUnavailable, true, ""), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
}

// The pause is the platform's, kept inside a sane range.
func TestWarmingDelay(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"": warmingDefaultDelay, "nonsense": warmingDefaultDelay, "0": warmingDefaultDelay,
		"1": 2 * time.Second, "15": 15 * time.Second, "600": 30 * time.Second,
	} {
		if got := warmingDelay(answer(503, true, in)); got != want {
			t.Errorf("Retry-After %q gives %s, want %s", in, got, want)
		}
	}
}
