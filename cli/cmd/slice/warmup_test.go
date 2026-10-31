package slice

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// captured runs fn with stdout captured.
func captured(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func fastWarmup(t *testing.T) {
	t.Helper()
	oldPoll, oldLimit := warmupPoll, warmupLimit
	warmupPoll, warmupLimit = time.Millisecond, 2*time.Second
	t.Cleanup(func() { warmupPoll, warmupLimit = oldPoll, oldLimit })
}

// A cooled slice: the first ask starts the warm-up (202), the next ones find it
// still on its way, and the one after finds it running. The command says what it
// is doing and returns when the slice is up.
func TestWarmup_WaitsUntilTheSliceIsRunning(t *testing.T) {
	fastWarmup(t)
	asks := 0
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ops/slice/warmup" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		asks++
		if asks < 3 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"state":"warming"}`)
			return
		}
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})

	var err error
	out := captured(t, func() { err = getWarmupCmd().RunE(getWarmupCmd(), nil) })
	if err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if asks != 3 {
		t.Errorf("asked %d times, want 3", asks)
	}
	if !strings.Contains(out, "cooled down") || !strings.Contains(out, "is warm") {
		t.Errorf("the output does not say the slice was cooled and is now warm:\n%s", out)
	}
}

// A slice that is not cooled is left as it is, and the command says so.
func TestWarmup_ARunningSliceIsAlreadyRunning(t *testing.T) {
	fastWarmup(t)
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})

	var err error
	out := captured(t, func() { err = getWarmupCmd().RunE(getWarmupCmd(), nil) })
	if err != nil || !strings.Contains(out, "already running") {
		t.Errorf("warmup of a running slice: %v\n%s", err, out)
	}
}

// A warm-up that never finishes ends in an error that says what to do, not in a
// command that waits for ever.
func TestWarmup_GivesUpOnASliceThatNeverComesBack(t *testing.T) {
	fastWarmup(t)
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"state":"warming"}`)
	})

	var err error
	captured(t, func() { err = getWarmupCmd().RunE(getWarmupCmd(), nil) })
	if err == nil || !strings.Contains(err.Error(), "still warming") {
		t.Errorf("err = %v, want it to say the slice is still warming", err)
	}
}

// `drift slice list` shows a cooled slice as one, and a running slice as nothing
// at all.
func TestList_NamesACooledSlice(t *testing.T) {
	now := time.Now()
	if got := cooledNote(SliceEntry{CooledAt: &now}); !strings.Contains(got, "cooled down") || !strings.Contains(got, "drift slice warmup") {
		t.Errorf("cooled note = %q, want it to say cooled and name the command", got)
	}
	if got := cooledNote(SliceEntry{WarmingAt: &now}); !strings.Contains(got, "warming") {
		t.Errorf("warming note = %q", got)
	}
	if got := cooledNote(SliceEntry{}); got != "" {
		t.Errorf("a running slice has a note: %q", got)
	}
}
