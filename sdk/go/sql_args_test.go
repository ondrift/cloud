package drift

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// sqlCapture stands in for a slice's backbone: it records the raw body of
// every SQL request and answers each path with a well-formed reply.
func sqlCapture(t *testing.T) func() map[string][]byte {
	t.Helper()
	var mu sync.Mutex
	got := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = raw
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sql/query":
			_, _ = w.Write([]byte(`{"columns":["n"],"rows":[[1]]}`))
		case "/sql/execute":
			_, _ = w.Write([]byte(`{"rows_affected":0,"last_insert_id":0}`))
		case "/sql/begin":
			_, _ = w.Write([]byte(`{"tx":"t1"}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BACKBONE_URL", srv.URL)
	return func() map[string][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := map[string][]byte{}
		for k, v := range got {
			out[k] = v
		}
		return out
	}
}

// argsOf returns the raw JSON the request carried under `args`.
func argsOf(t *testing.T, body []byte) string {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("the request body is not a JSON object: %v (%s)", err, body)
	}
	raw, ok := env["args"]
	if !ok {
		t.Fatalf("the request body carries no args: %s", body)
	}
	return string(raw)
}

// A statement with no placeholders sends `"args":[]`. A nil variadic slice
// marshals as `null`, and a slice that reads args as a list refuses the whole
// body as invalid JSON, so every argument-less statement failed.
func TestSQLWithNoArgsSendsAnEmptyArray(t *testing.T) {
	bodies := sqlCapture(t)
	db := Backbone.SQL("main")

	if _, err := db.Query("SELECT 1"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := argsOf(t, bodies()["/sql/query"]); got != "[]" {
		t.Errorf("Query with no args sent args=%s, want []", got)
	}

	if _, err := db.Execute("DELETE FROM t"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := argsOf(t, bodies()["/sql/execute"]); got != "[]" {
		t.Errorf("Execute with no args sent args=%s, want []", got)
	}
}

// The transaction variants share the envelope, and keep their token.
func TestSQLTxWithNoArgsSendsAnEmptyArray(t *testing.T) {
	bodies := sqlCapture(t)
	tx, err := Backbone.SQL("main").Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := tx.Query("SELECT 1"); err != nil {
		t.Fatalf("tx.Query: %v", err)
	}
	q := bodies()["/sql/query"]
	if got := argsOf(t, q); got != "[]" {
		t.Errorf("tx.Query with no args sent args=%s, want []", got)
	}

	if _, err := tx.Execute("DELETE FROM t"); err != nil {
		t.Fatalf("tx.Execute: %v", err)
	}
	e := bodies()["/sql/execute"]
	if got := argsOf(t, e); got != "[]" {
		t.Errorf("tx.Execute with no args sent args=%s, want []", got)
	}

	for path, body := range map[string][]byte{"query": q, "execute": e} {
		var env struct {
			Tx string `json:"tx"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if env.Tx != "t1" {
			t.Errorf("tx.%s sent tx=%q, want the token Begin returned", path, env.Tx)
		}
	}
}

// THE CONTROL: arguments that are passed arrive unchanged and in order.
func TestSQLWithArgsSendsThemInOrder(t *testing.T) {
	bodies := sqlCapture(t)
	if _, err := Backbone.SQL("main").Query("SELECT ? , ?", "a", 2); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := argsOf(t, bodies()["/sql/query"]); got != `["a",2]` {
		t.Errorf("Query sent args=%s, want [\"a\",2]", got)
	}
}
