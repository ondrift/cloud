package common

// warming.go — waiting for a slice that was cooled down.
//
// A free slice that no request has reached for a month is scaled to zero, and the
// first call that needs it starts bringing it back and is answered 503 with
// X-Drift-Warming and a Retry-After. That is not a failure, so a command waits it
// out: it says so once, sleeps the Retry-After the platform gave, and sends the
// same request again, for as long as a warm-up takes. Past that it hands the 503
// to the caller as what it is.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	// WarmingHeader marks a 503 as a slice warming.
	WarmingHeader = "X-Drift-Warming"

	// warmingMaxWait bounds the wait. A warm-up takes about twenty seconds; the
	// platform gives up on a pod that is not ready after three minutes, and so
	// does a command that has waited this long.
	warmingMaxWait = 3 * time.Minute

	// warmingDefaultDelay is the pause when the answer names none.
	warmingDefaultDelay = 10 * time.Second
)

var (
	// warmingSleep and warmingNotice are variables so a test neither waits nor
	// writes to the terminal.
	warmingSleep            = func(ctx context.Context, d time.Duration) error { return sleepCtx(ctx, d) }
	warmingNotice io.Writer = os.Stderr
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// IsWarming reports whether a response is the platform saying the slice is coming
// back from a cooldown.
func IsWarming(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(WarmingHeader) == "1"
}

// waitWhileWarming sends the request and, while the answer is a slice warming,
// waits and sends it again. send must build a fresh request each time.
func waitWhileWarming(ctx context.Context, send func() (*http.Response, error)) (*http.Response, error) {
	var waited time.Duration
	told := false
	for {
		resp, err := send()
		if err != nil || !IsWarming(resp) {
			return resp, err
		}
		delay := warmingDelay(resp)
		if waited+delay > warmingMaxWait {
			return resp, nil // still warming after the limit: the caller sees the 503
		}
		_ = resp.Body.Close()
		if !told {
			fmt.Fprintln(warmingNotice, "This slice was cooled down for want of requests. Warming it up, about 20 seconds...")
			told = true
		}
		if err := warmingSleep(ctx, delay); err != nil {
			return nil, err
		}
		waited += delay
	}
}

// warmingDelay is the pause the platform asked for, in seconds, bounded to a sane
// range, or the default when it named none.
func warmingDelay(resp *http.Response) time.Duration {
	n, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || n <= 0 {
		return warmingDefaultDelay
	}
	return time.Duration(min(max(n, 2), 30)) * time.Second
}
