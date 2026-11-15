package common

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DRIFT-1011 says no credential was found on this machine. A token the platform
// turned down is the opposite: a credential was found and was refused, and its
// remedy is a new token, not a login. The two print different codes, and a person
// who pastes either can be told which failure it was.

// stubExchange points the CLI at a platform that answers the token exchange with
// the status and nothing else.
func stubExchange(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	was := APIBaseURL
	APIBaseURL = srv.URL
	t.Cleanup(func() { APIBaseURL = was })
}

// A refused DRIFT_TOKEN prints the code for a refused token and not DRIFT-1011.
func TestARefusedTokenPrintsItsOwnCodeAndNotDRIFT1011(t *testing.T) {
	t.Skip("DRIFT-1019 is registered by the next commit and printed by exchangePAT in the one after it")
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		stubExchange(t, status)

		_, err := exchangePAT(context.Background(), "drift_pat_revoked")

		assertCodedOnce(t, err, "DRIFT-1019")
		if strings.Contains(err.Error(), "DRIFT-1011") {
			t.Errorf("a refused token printed the code for no credential at all (HTTP %d):\n%s", status, err)
		}
		for _, want := range []string{TokenEnv, "drift account token list"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal no longer says %q (HTTP %d):\n%s", want, status, err)
			}
		}
	}
}

// The hint for a value that is not a Drift token at all stays, after the sentence
// and before the code's line.
func TestARefusedTokenThatIsNotATokenStillSaysSo(t *testing.T) {
	t.Skip("DRIFT-1019 is registered by the next commit and printed by exchangePAT in the one after it")
	stubExchange(t, http.StatusUnauthorized)

	_, err := exchangePAT(context.Background(), "hunter2")

	assertCodedOnce(t, err, "DRIFT-1019")
	if !strings.Contains(err.Error(), "does not look like a Drift token") {
		t.Errorf("the hint for a value that is not a token is gone:\n%s", err)
	}
	if !strings.HasSuffix(err.Error(), codeLineOf("DRIFT-1019")) {
		t.Errorf("the code's line does not end the failure:\n%s", err)
	}
}

// The control: DRIFT-1011 is still printed where no credential exists at all, so
// the change cannot have moved it.
func TestNoCredentialAtAllStillPrintsDRIFT1011(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(TokenEnv, "")

	_, err := newAuthenticatedRequestCtx(context.Background(), http.MethodGet, "http://127.0.0.1/", nil)

	assertCodedOnce(t, err, "DRIFT-1011")
}

// The control: a platform that cannot answer the exchange is not a refused token.
func TestAnUnavailablePlatformIsNotARefusedToken(t *testing.T) {
	stubExchange(t, http.StatusServiceUnavailable)

	_, err := exchangePAT(context.Background(), "drift_pat_fine")

	if !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatalf("a 503 on the exchange = %v, want an unavailable platform", err)
	}
	if got := CodeOf(err); got == "DRIFT-1019" || got == "DRIFT-1011" {
		t.Errorf("an unavailable platform printed %s", got)
	}
}

// A connection failure TransportError does not recognise prints the code for one
// and keeps the original text after the lead. The failure here is a real one: a
// TLS server whose certificate this client does not trust.
func TestATLSVerificationFailurePrintsTheConnectionCodeAndKeepsItsText(t *testing.T) {
	t.Skip("DRIFT-1020 is registered by the next commit and printed by TransportError in the one after it")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	_, cause := (&http.Client{}).Get(srv.URL)
	if cause == nil {
		t.Fatal("an untrusted certificate was accepted")
	}

	err := TransportError("list slices", cause)

	assertCodedOnce(t, err, "DRIFT-1020")
	if !strings.HasPrefix(err.Error(), "Couldn't list slices: ") {
		t.Errorf("the lead is gone:\n%s", err)
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Errorf("the original error text is gone.\nwant it to contain: %s\ngot:\n%s", cause, err)
	}
}

// A response whose body is cut off mid-read is a connection failure too, and it
// keeps its sentence and the text of what cut it off.
func TestABodyCutOffMidReadPrintsTheConnectionCode(t *testing.T) {
	t.Skip("DRIFT-1020 is registered by the next commit and printed by CheckResponse in the one after it")
	cutOff := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(`{"slices":[`), errReader{io.ErrUnexpectedEOF})),
	}

	_, err := CheckResponse(cutOff, "list slices")

	assertCodedOnce(t, err, "DRIFT-1020")
	for _, want := range []string{"Couldn't list slices: failed to read response body", io.ErrUnexpectedEOF.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%s", want, err)
		}
	}
}

// errReader fails every read with its error.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// The controls: a failure TransportError does recognise keeps the code it has,
// and is not given the connection code as well.
func TestARecognisedConnectionFailureKeepsItsOwnCode(t *testing.T) {
	refused := TransportError("deploy", errors.New("dial tcp 127.0.0.1:9: connect: connection refused"))
	assertCodedOnce(t, refused, "DRIFT-1006")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	_, cause := (&http.Client{}).Get(srv.URL)
	if cause == nil {
		t.Fatal("a closed server answered")
	}
	if got := CodeOf(TransportError("deploy", cause)); got != "DRIFT-1006" {
		t.Errorf("a refused connection printed %q, want DRIFT-1006", got)
	}
}
