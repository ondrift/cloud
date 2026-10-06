package drift

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// countingServer answers every request with a small JSON body and counts the
// connections clients open to it.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var opened atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &opened
}

// Concurrent Backbone calls reuse their loopback connections, on each of the
// three paths a Backbone or Deed call takes. Every connect a function makes is
// a round trip to the sandbox supervisor, and a client that keeps 2 idle
// connections per host reconnects for most of 16 concurrent callers on every
// call.
//
// The bound is twice the callers rather than exactly the callers: a goroutine
// that issues its next call before the transport has returned its previous
// connection to the idle pool dials one more, and that connection then joins
// the pool. What the test pins is that the pool stops growing: the default
// client opens around 50 here, and a body closed unread opens one per call.
func TestBackboneCallsReuseConnectionsUnderConcurrency(t *testing.T) {
	paths := map[string]func(base string) error{
		"json": func(base string) error {
			_, err := callBackboneHTTP(base, "GET", "cache/get?key=k", nil)
			return err
		},
		"raw": func(string) error {
			return callBackboneRaw("POST", "blob/put?bucket=b&key=k", []byte("x"), "text/plain")
		},
		"bearer": func(base string) error {
			_, err := callBackboneHTTPAuth(base, "GET", "deed/pocket/list", "token", nil)
			return err
		},
	}
	for name, call := range paths {
		t.Run(name, func(t *testing.T) {
			srv, opened := countingServer(t)
			t.Setenv("BACKBONE_URL", srv.URL)

			const callers, calls = 16, 10
			var wg sync.WaitGroup
			errs := make(chan error, callers*calls)
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range calls {
						if err := call(srv.URL); err != nil {
							errs <- err
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("backbone call: %v", err)
			}

			if n := opened.Load(); n > 2*callers {
				t.Fatalf("%d concurrent callers making %d calls each opened %d connections; want at most %d",
					callers, calls, n, 2*callers)
			}
			t.Logf("%d connections for %d calls", opened.Load(), callers*calls)
		})
	}
}
