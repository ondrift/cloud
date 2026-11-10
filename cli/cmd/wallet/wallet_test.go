package wallet

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// fakeAPI is the api's wallet routes with the answers payments gives. It records
// every request so a test can say what the CLI sent.
type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	// topUp answers POST /ops/wallet/topup.
	topUpStatus int
	topUpBody   string
	balance     int64
	history     string
}

type recorded struct {
	Method, Path, Query, Body string
}

func (f *fakeAPI) serve(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		f.mu.Unlock()
	}
	mux.HandleFunc("/ops/wallet/balance", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(map[string]any{"account": "tester", "balance_cents": f.balance})
	})
	mux.HandleFunc("/ops/wallet/history", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(f.history))
	})
	mux.HandleFunc("/ops/wallet/topup", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.topUpStatus)
		_, _ = w.Write([]byte(f.topUpBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.TokenEnv, "")
	t.Setenv(common.SliceEnv, "")
	t.Setenv(common.AccountEnv, "")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"tester","exp":9999999999}`))
	if err := common.SaveSession("h."+payload+".s", "refresh"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
}

// run executes `drift wallet <args>` and returns what it printed and its error.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	cmd := GetCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	runErr := cmd.Execute()

	_ = w.Close()
	os.Stdout = old
	return <-done, runErr
}

func TestBalance_PrintsTheBalanceInEuros(t *testing.T) {
	f := &fakeAPI{balance: 1250}
	f.serve(t)
	out, err := run(t, "balance")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "€12.50" {
		t.Errorf("balance printed %q, want €12.50", out)
	}
	// A balance of nothing is €0.00, not an empty line.
	f.balance = 0
	out, _ = run(t, "balance")
	if strings.TrimSpace(out) != "€0.00" {
		t.Errorf("an empty balance printed %q, want €0.00", out)
	}
}

// The account is the session's. Nothing the CLI sends names one.
func TestEveryVerbSendsNoAccount(t *testing.T) {
	f := &fakeAPI{
		balance: 500, history: `{"entries":[]}`,
		topUpStatus: 201, topUpBody: `{"payment_id":"p1","status":"pending","pay_url":"https://pay.example/p1"}`,
	}
	f.serve(t)
	for _, args := range [][]string{{"balance"}, {"history"}, {"topup", "10"}} {
		if _, err := run(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(f.requests) != 3 {
		t.Fatalf("expected 3 requests, got %d: %+v", len(f.requests), f.requests)
	}
	for _, r := range f.requests {
		if strings.Contains(r.Query, "account") || strings.Contains(r.Body, "account") {
			t.Errorf("%s %s sent an account: query %q body %q", r.Method, r.Path, r.Query, r.Body)
		}
	}
}

func TestHistory_ListsNewestFirstWithSignedAmountsAndOffersTheNextPage(t *testing.T) {
	f := &fakeAPI{history: `{"account":"tester","entries":[
		{"seq":3,"kind":"booking","amount_cents":-241,"balance_cents":759,"ref":"slice-a","at":"2026-11-03T10:00:00Z"},
		{"seq":2,"kind":"topup","amount_cents":1000,"balance_cents":1000,"ref":"mock_000001","at":"2026-11-02T10:00:00Z"}
	]}`}
	f.serve(t)
	out, err := run(t, "history", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-€2.41", "+€10.00", "€7.59", "slice period", "top-up", "slice-a", "drift wallet history --before 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("history output lacks %q:\n%s", want, out)
		}
	}
	if q := f.requests[0].Query; !strings.Contains(q, "limit=2") || strings.Contains(q, "before") {
		t.Errorf("first page asked %q", q)
	}

	// A page shorter than the limit is the last one: no hint to a next page.
	out, _ = run(t, "history", "--limit", "5")
	if strings.Contains(out, "--before") {
		t.Errorf("a short page offered a next page:\n%s", out)
	}
}

func TestHistory_AnEmptyLedgerSaysSo(t *testing.T) {
	f := &fakeAPI{history: `{"account":"tester","entries":[]}`}
	f.serve(t)
	out, err := run(t, "history")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing has moved your balance yet.") {
		t.Errorf("empty history printed %q", out)
	}
}

func TestHistory_RefusesAPageSizeTheCLIKnowsIsWrongWithoutAsking(t *testing.T) {
	f := &fakeAPI{history: `{"entries":[]}`}
	f.serve(t)
	for _, bad := range []string{"0", "201", "-1"} {
		if _, err := run(t, "history", "--limit", bad); err == nil {
			t.Errorf("--limit %s was accepted", bad)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("a refused page size still reached the api: %+v", f.requests)
	}
}

// The amount reaches the api as exact cents.
func TestTopUp_SendsExactCentsAndSaysWhereToPay(t *testing.T) {
	f := &fakeAPI{topUpStatus: 201, topUpBody: `{"payment_id":"p1","status":"pending","pay_url":"https://pay.example/p1"}`}
	f.serve(t)
	out, err := run(t, "topup", "12.50")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].Body; got != `{"amount_cents":1250}` {
		t.Errorf("the api was sent %s", got)
	}
	for _, want := range []string{"€12.50", "Pay at: https://pay.example/p1", "moves when it is paid"} {
		if !strings.Contains(out, want) {
			t.Errorf("top-up output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Your balance is") {
		t.Errorf("a pending top-up claimed a balance:\n%s", out)
	}
}

// Only "paid" says the money arrived, and then the balance is read again so the
// person sees it move.
func TestTopUp_APaidTopUpShowsTheBalanceItMade(t *testing.T) {
	f := &fakeAPI{balance: 1000, topUpStatus: 201, topUpBody: `{"payment_id":"mock_000001","status":"paid"}`}
	f.serve(t)
	out, err := run(t, "topup", "10")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Topped up €10.00") || !strings.Contains(out, "Your balance is €10.00.") {
		t.Errorf("a paid top-up printed:\n%s", out)
	}
}

// A payment that did not complete never reads as a top-up that did.
func TestTopUp_AFailedPaymentSaysNothingWasAdded(t *testing.T) {
	f := &fakeAPI{topUpStatus: 201, topUpBody: `{"payment_id":"p9","status":"failed"}`}
	f.serve(t)
	out, err := run(t, "topup", "10")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing was added to your balance") || strings.Contains(out, "Topped up") {
		t.Errorf("a failed payment printed:\n%s", out)
	}
}

// The minimum is the platform's. Its words reach the person, and the CLI holds
// no copy of the figure.
func TestTopUp_ThePlatformsRefusalReachesThePerson(t *testing.T) {
	f := &fakeAPI{topUpStatus: 400, topUpBody: `{"error":"wallet: that amount cannot be topped up: 100 cents, the minimum is 500"}`}
	f.serve(t)
	_, err := run(t, "topup", "1")
	if err == nil {
		t.Fatal("a refused top-up returned no error")
	}
	if !strings.Contains(err.Error(), "the minimum is 500") {
		t.Errorf("the platform's reason was lost: %v", err)
	}
}

// An amount that is not positive whole cents never reaches the api.
func TestTopUp_ABadAmountNeverReachesTheApi(t *testing.T) {
	f := &fakeAPI{topUpStatus: 201, topUpBody: `{}`}
	f.serve(t)
	for _, bad := range []string{"0", "-5", "ten", "10.999", ""} {
		if _, err := run(t, "topup", bad); err == nil {
			t.Errorf("topup %q was accepted", bad)
		}
	}
	if _, err := run(t, "topup"); err == nil {
		t.Error("topup with no amount was accepted")
	}
	if len(f.requests) != 0 {
		t.Errorf("a bad amount reached the api: %+v", f.requests)
	}
}

func TestJSONFlagPrintsThePlatformsBytes(t *testing.T) {
	f := &fakeAPI{balance: 42}
	f.serve(t)
	out, err := run(t, "balance", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["balance_cents"] != float64(42) {
		t.Errorf("--json printed %q (%v)", out, err)
	}
}
