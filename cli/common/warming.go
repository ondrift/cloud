package common

// warming.go — waiting for a slice that was cooled down.
//
// A free slice that no request has reached for a month is scaled to zero, and the
// first call that needs it starts bringing it back and is answered 503 with
// X-Drift-Warming and a Retry-After. That is not a failure, so a command waits it
// out: it says so once, sleeps the Retry-After the platform gave, and sends the
// same request again, for as long as a warm-up takes. Past that it hands the 503
// to the caller as what it is.
//
// A slice that no request has reached for months has its data stored away as an
// archive, and the same answer carries X-Drift-Restoring beside X-Drift-Warming:
// bringing it back takes minutes, so the command says that and waits longer.

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

	// RestoringHeader marks the warm-up as a restore from an archive. It rides with
	// WarmingHeader, never instead of it.
	RestoringHeader = "X-Drift-Restoring"

	// warmingMaxWait bounds the wait. A warm-up takes about twenty seconds; the
	// platform gives up on a pod that is not ready after three minutes, and so
	// does a command that has waited this long.
	warmingMaxWait = 3 * time.Minute

	// restoringMaxWait bounds the wait for a restore: the platform allows its pod
	// three minutes to be ready and the replay of the archive eight more, and a
	// command that has waited this long has waited out both.
	restoringMaxWait = 12 * time.Minute

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

// IsRestoring reports whether a warming response is a restore from an archive,
// which takes minutes and not seconds.
func IsRestoring(resp *http.Response) bool {
	return IsWarming(resp) && resp.Header.Get(RestoringHeader) == "1"
}

// waitWhileWarming sends the request and, while the answer is a slice warming,
// waits and sends it again. send must build a fresh request each time.
func waitWhileWarming(ctx context.Context, send func() (*http.Response, error)) (*http.Response, error) {
	var waited time.Duration
	limit := warmingMaxWait
	told, toldRestoring := false, false
	for {
		resp, err := send()
		if err != nil || !IsWarming(resp) {
			return resp, err
		}
		restoring := IsRestoring(resp)
		if restoring {
			limit = restoringMaxWait
		}
		delay := warmingDelay(resp)
		if waited+delay > limit {
			return resp, nil // still warming after the limit: the caller sees the 503
		}
		_ = resp.Body.Close()
		switch {
		case restoring && !toldRestoring:
			fmt.Fprintln(warmingNotice, "This slice was stored away after months without requests. Restoring it takes a few minutes...")
			told, toldRestoring = true, true
		case !told:
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
