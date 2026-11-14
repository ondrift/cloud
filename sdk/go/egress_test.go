package drift

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What the runtime does with a connect it refuses is answer it with EPERM, and
// the standard library reports that as a *net.OpError holding an
// *os.SyscallError holding the errno, inside the *url.Error the HTTP client
// returns. These tests pin that the SDK recognises that chain with errors.Is and
// nothing else, so a host that is merely down, a DNS or TLS failure and a
// timeout are never blamed on the allowlist.

// dialRefusedByTheRuntime is a DialContext whose connect fails with EPERM. The
// error comes out of the net package's own dial path, with the Control hook
// failing where connect(2) would, so its shape is the one a real refusal has and
// not one written out by hand.
func dialRefusedByTheRuntime() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Control: func(string, string, syscall.RawConn) error {
		return os.NewSyscallError("connect", syscall.EPERM)
	}}
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		// The address is never reached: Control fails first. A literal keeps the
		// dial from resolving the URL's host name.
		return d.DialContext(ctx, network, "127.0.0.1:9")
	}
}

// useTransport makes HTTPRequest send through rt for the rest of the test.
func useTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	prev := httpRequestClient.Transport
	httpRequestClient.Transport = rt
	t.Cleanup(func() { httpRequestClient.Transport = prev })
}

func requireNotEgressDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("got no error, want the transport's own")
	}
	var ee *EgressDeniedError
	if errors.As(err, &ee) {
		t.Fatalf("err = %q, want the transport error unchanged, not an EgressDeniedError for %q", err, ee.Host)
	}
}

// A connect the runtime refuses comes back as an *EgressDeniedError that names
// the destination, and the errno is still reachable through Unwrap.
func TestHTTPRequestReturnsEgressDeniedWhenTheRuntimeRefusesTheConnect(t *testing.T) {
	t.Skip("#3247 returns a refused connect as an EgressDeniedError and removes this skip")

	useTransport(t, &http.Transport{DialContext: dialRefusedByTheRuntime()})

	resp, err := HTTPRequest("GET", "https://api.stripe.com:8443/v1/charges", nil, nil)
	if resp != nil {
		t.Errorf("resp = %+v, want nil on an error", resp)
	}
	if err == nil {
		t.Fatal("a refused connect returned no error")
	}
	if !strings.Contains(err.Error(), "connect: operation not permitted") {
		t.Fatalf("the refused dial reads %q, want the runtime's 'connect: operation not permitted'", err)
	}
	ee, ok := err.(*EgressDeniedError)
	if !ok {
		t.Fatalf("err = %T %q, want *EgressDeniedError", err, err)
	}
	if ee.Host != "api.stripe.com" {
		t.Errorf("Host = %q, want the URL's host without its port", ee.Host)
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("errors.Is(err, syscall.EPERM) is false for %q, want the errno reachable through Unwrap", err)
	}
	var oe *net.OpError
	if !errors.As(err, &oe) {
		t.Errorf("the transport's *net.OpError is not reachable through Unwrap from %q", err)
	}
}

// The same classification, one call down, for every spelling of a refusal the
// transport can hand back.
func TestWrapEgressDeniedRecognisesEPERM(t *testing.T) {
	t.Skip("#3247 matches the errno and removes this skip")

	eperm := os.NewSyscallError("connect", syscall.EPERM)
	fromClient := func(inner error) error {
		return &url.Error{Op: "Get", URL: "https://api.stripe.com/v1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: inner}}
	}
	cases := []struct {
		name string
		url  string
		err  error
		host string
	}{
		{"the chain the HTTP client returns", "https://api.stripe.com/v1", fromClient(eperm), "api.stripe.com"},
		{"the errno alone", "https://api.stripe.com/v1", syscall.EPERM, "api.stripe.com"},
		{"wrapped again by the caller", "https://api.stripe.com/v1", fmt.Errorf("charging: %w", fromClient(eperm)), "api.stripe.com"},
		{"a URL with a port", "https://api.stripe.com:8443/v1", fromClient(eperm), "api.stripe.com"},
		{"an IPv6 literal", "http://[2001:db8::1]:8080/", fromClient(eperm), "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapEgressDenied(tc.url, tc.err)
			if got == nil {
				t.Fatalf("wrapEgressDenied(%q, %q) = nil, want an *EgressDeniedError", tc.url, tc.err)
			}
			if got.Host != tc.host {
				t.Errorf("Host = %q, want %q", got.Host, tc.host)
			}
			if !errors.Is(got, syscall.EPERM) {
				t.Errorf("errors.Is(got, syscall.EPERM) is false, want the errno reachable through Unwrap")
			}
			if got.Unwrap() != tc.err {
				t.Errorf("Unwrap() = %v, want the original error %v", got.Unwrap(), tc.err)
			}
		})
	}
}

// A refused connection is a host that is down. The kernel says so with
// ECONNREFUSED, and the runtime never does: it says EPERM. Neither the errno
// nor its words are a refusal by the platform.
func TestWrapEgressDeniedIgnoresAConnectionRefused(t *testing.T) {
	t.Skip("#3247 stops matching the text 'connection refused' and removes this skip")

	refused := os.NewSyscallError("connect", syscall.ECONNREFUSED)
	cases := map[string]error{
		"the chain the HTTP client returns": &url.Error{Op: "Get", URL: "https://api.stripe.com/v1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: refused}},
		"the errno alone":                   syscall.ECONNREFUSED,
		"the words alone":                   errors.New("dial tcp 192.0.2.1:443: connect: connection refused"),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := wrapEgressDenied("https://api.stripe.com/v1", err); got != nil {
				t.Errorf("wrapEgressDenied(%q) = %q, want nil: a host that is down is not a refusal by the platform", err, got)
			}
		})
	}
}

// Everything else the HTTP client can fail with is returned as it is.
func TestWrapEgressDeniedIgnoresEveryOtherFailure(t *testing.T) {
	dial := func(inner error) error {
		return &url.Error{Op: "Get", URL: "https://api.stripe.com/v1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: inner}}
	}
	cases := map[string]error{
		"no error":         nil,
		"a timeout":        dial(os.ErrDeadlineExceeded),
		"a deadline":       &url.Error{Op: "Get", URL: "https://api.stripe.com/v1", Err: context.DeadlineExceeded},
		"host unreachable": dial(os.NewSyscallError("connect", syscall.EHOSTUNREACH)),
		"a DNS failure": dial(&net.DNSError{
			Err: "no such host", Name: "api.stripe.invalid", IsNotFound: true,
		}),
		// The words of a refusal are not one: only the errno, reached with
		// errors.Is, says the platform refused.
		"a DNS failure that mentions the words": dial(&net.DNSError{
			Err: "dial udp 10.96.0.10:53: connect: operation not permitted", Name: "api.stripe.com",
		}),
		"a TLS failure": &url.Error{Op: "Get", URL: "https://api.stripe.com/v1", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := wrapEgressDenied("https://api.stripe.com/v1", err); got != nil {
				t.Errorf("wrapEgressDenied(%v) = %q, want nil", err, got)
			}
		})
	}
}

// A request to a host that is down comes back as the transport reported it,
// from a real closed port.
func TestHTTPRequestReturnsAConnectionRefusedByAHostThatIsDownUnchanged(t *testing.T) {
	t.Skip("#3247 stops matching the text 'connection refused' and removes this skip")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, err = HTTPRequest("GET", "http://"+addr+"/", nil, nil)
	requireNotEgressDenied(t, err)
	if _, ok := err.(*url.Error); !ok {
		t.Errorf("err = %T, want the HTTP client's own *url.Error", err)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("err = %q, want ECONNREFUSED reachable from a closed port", err)
	}
}

// A request that runs out of time comes back as a timeout, from a real server
// that never answers.
func TestHTTPRequestReturnsATimeoutUnchanged(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	_, err := HTTPRequestWithTimeout("GET", srv.URL, nil, nil, 100*time.Millisecond)
	requireNotEgressDenied(t, err)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("err = %q, want a net.Error that reports Timeout()", err)
	}
}

// A certificate the client does not trust comes back as the TLS failure it is,
// from a real TLS server.
func TestHTTPRequestReturnsATLSFailureUnchanged(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	_, err := HTTPRequest("GET", srv.URL, nil, nil)
	requireNotEgressDenied(t, err)
	var cve *tls.CertificateVerificationError
	if !errors.As(err, &cve) {
		t.Errorf("err = %q, want the certificate verification failure", err)
	}
}

// A name that does not resolve comes back as the resolver's error, in the shape
// the net package gives a failed lookup inside a dial.
func TestHTTPRequestReturnsADNSFailureUnchanged(t *testing.T) {
	useTransport(t, &http.Transport{DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{
			Err: "no such host", Name: "api.stripe.invalid", IsNotFound: true,
		}}
	}})

	_, err := HTTPRequest("GET", "https://api.stripe.invalid/v1", nil, nil)
	requireNotEgressDenied(t, err)
	var de *net.DNSError
	if !errors.As(err, &de) || !de.IsNotFound {
		t.Errorf("err = %q, want the not-found *net.DNSError", err)
	}
}

// The message names the host and the Driftfile key that allows it, and says who
// refused: the platform, not a network rule.
func TestEgressDeniedErrorSaysThePlatformRefusedTheConnection(t *testing.T) {
	t.Skip("#3248 rewords the message and removes this skip")

	underlying := os.NewSyscallError("connect", syscall.EPERM)
	msg := (&EgressDeniedError{Host: "api.stripe.com", Err: underlying}).Error()

	for _, want := range []string{
		"api.stripe.com",
		"edge.egress.hosts",
		"the platform refused the connection",
		underlying.Error(),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	for _, banned := range []string{"NetworkPolicy", "network policy", "network rule", "ipset", "—", "–"} {
		if strings.Contains(msg, banned) {
			t.Errorf("message %q contains %q", msg, banned)
		}
	}
}
