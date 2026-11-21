package common

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withStatusFeed points the health check at a stub for one test.
func withStatusFeed(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := StatusURL
	StatusURL = srv.URL
	t.Cleanup(func() { StatusURL = prev })
}

// The feed is the api's own: status.ondrift.eu is plain files and answers no
// /api/status, so a URL built for that host reads nothing at all.
func TestStatusFeedURL_IsTheAPIsOwnFeed(t *testing.T) {
	prevBase, prevURL := APIBaseURL, StatusURL
	t.Cleanup(func() { APIBaseURL, StatusURL = prevBase, prevURL })
	StatusURL = ""

	for base, want := range map[string]string{
		"https://api.ondrift.eu":          "https://api.ondrift.eu/ops/status",
		"http://api.localhost:30036/":     "http://api.localhost:30036/ops/status",
		"https://api.staging.example.org": "https://api.staging.example.org/ops/status",
	} {
		APIBaseURL = base
		if got := StatusFeedURL(); got != want {
			t.Errorf("APIBaseURL %q: got %q, want %q", base, got, want)
		}
	}
}

const feedOK = `[{"name":"API","status":"operational"},{"name":"Routing","status":"operational"}]`
const feedDegraded = `[{"name":"API","status":"operational"},{"name":"Routing","status":"degraded"}]`

// THE case this exists for: a platform fault must not read as the user's code
// being wrong. When a component is down, the failure says which one and says
// plainly whose it is.
func TestAPIError_A5xxNamesTheDegradedComponent(t *testing.T) {
	withStatusFeed(t, http.StatusOK, feedDegraded)

	got := (&APIError{Op: "deploy", Status: 500}).Error()
	if !strings.Contains(got, "Routing") {
		t.Errorf("the degraded component must be named, got: %s", got)
	}
	if !strings.Contains(got, "not your code") {
		t.Errorf("the message must say whose fault it is, got: %s", got)
	}
}

// The control that stops the check becoming an excuse: when everything is
// operational, a 5xx is still a 5xx and nothing is added. Claiming health this
// cannot confirm would be worse than saying nothing.
func TestAPIError_AHealthyPlatformAddsNothing(t *testing.T) {
	withStatusFeed(t, http.StatusOK, feedOK)

	got := (&APIError{Op: "deploy", Status: 500}).Error()
	if strings.Contains(got, "not your code") {
		t.Errorf("nothing is degraded, so no verdict should be offered, got: %s", got)
	}
	if !strings.Contains(got, "DRIFT-1005") {
		t.Errorf("the failure itself must survive unchanged, got: %s", got)
	}
}

// A 4xx is the platform telling the user something TRUE about their request.
// Offering component health there would hand them an excuse for a refusal that
// is correct.
func TestAPIError_A4xxNeverConsultsHealth(t *testing.T) {
	// A feed that would fail the test if it were consulted at all.
	withStatusFeed(t, http.StatusOK, feedDegraded)

	got := (&APIError{Op: "delete slice", Status: 404}).Error()
	if strings.Contains(got, "not your code") {
		t.Errorf("a 4xx must not be excused by platform health, got: %s", got)
	}
}

// Every way the check can fail ends in silence. During an outage the status page
// is itself likely to be unreachable, and a diagnostic that fails loudly while
// diagnosing a failure has made the screen worse.
func TestDegradedComponents_FailsQuietly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"status page is down", http.StatusBadGateway, ""},
		{"body is not JSON", http.StatusOK, "<html>maintenance</html>"},
		{"body is JSON but the wrong shape", http.StatusOK, `{"components":[]}`},
		{"empty body", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withStatusFeed(t, tc.status, tc.body)
			if got := DegradedComponents(); len(got) != 0 {
				t.Errorf("an unusable feed must yield nothing, got %v", got)
			}
		})
	}
}

// More than one degraded component reads as a list rather than a sentence per
// component.
func TestPlatformFaultNote_ListsSeveral(t *testing.T) {
	got := platformFaultNote([]string{"Routing", "Authentication"})
	if !strings.Contains(got, "Routing, Authentication") {
		t.Errorf("both must be named, got: %s", got)
	}
	if !strings.Contains(got, "are not operational") {
		t.Errorf("the plural must agree, got: %s", got)
	}
	if single := platformFaultNote([]string{"Routing"}); !strings.Contains(single, "is not operational") {
		t.Errorf("the singular must agree, got: %s", single)
	}
}

// Nothing degraded means nothing said — the distinction the whole file rests on.
func TestPlatformFaultNote_SaysNothingWhenItKnowsNothing(t *testing.T) {
	if got := platformFaultNote(nil); got != "" {
		t.Errorf("an empty verdict must render nothing, got: %q", got)
	}
}

// ReadStatusFeed returns every row with the state it reported, operational ones
// included, and says the feed was read.
func TestReadStatusFeed_ReturnsEveryRowWithItsState(t *testing.T) {
	withStatusFeed(t, http.StatusOK, `[{"name":"API","status":"operational"},{"name":"Routing","status":"degraded"},{"name":"","status":"down"}]`)

	feed := ReadStatusFeed(context.Background())
	if !feed.Read {
		t.Fatalf("a feed that answered was not read: %+v", feed)
	}
	want := []StatusRow{{Name: "API", Status: "operational"}, {Name: "Routing", Status: "degraded"}}
	if !reflect.DeepEqual(feed.Rows, want) {
		t.Errorf("rows = %+v, want %+v (a row with no name names nothing)", feed.Rows, want)
	}
	if got := feed.Degraded(); !reflect.DeepEqual(got, []string{"Routing"}) {
		t.Errorf("Degraded() = %v, want [Routing]", got)
	}
}

// A feed that listed nothing was read; the two are not the same answer.
func TestReadStatusFeed_AnEmptyListIsReadNotUnreadable(t *testing.T) {
	withStatusFeed(t, http.StatusOK, `[]`)
	if feed := ReadStatusFeed(context.Background()); !feed.Read || len(feed.Rows) != 0 {
		t.Errorf("an empty list: %+v, want Read with no rows", feed)
	}
}

// Every way the feed fails to be read is Read=false with a reason, and a refusal
// and a timeout are told apart from an answer that is not rows.
func TestReadStatusFeed_SaysWhyItCouldNotRead(t *testing.T) {
	t.Run("unreadable answers", func(t *testing.T) {
		for name, tc := range map[string]struct {
			status int
			body   string
		}{
			"not found":    {http.StatusNotFound, ""},
			"not json":     {http.StatusOK, "<html>maintenance</html>"},
			"wrong shape":  {http.StatusOK, `{"components":[]}`},
			"empty body":   {http.StatusOK, ""},
			"server error": {http.StatusBadGateway, "[]"},
		} {
			withStatusFeed(t, tc.status, tc.body)
			feed := ReadStatusFeed(context.Background())
			if feed.Read || feed.Failure != FeedUnreadable || feed.Reason == "" {
				t.Errorf("%s: %+v, want unreadable with a reason", name, feed)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := srv.URL
		srv.Close()
		prev := StatusURL
		StatusURL = addr
		t.Cleanup(func() { StatusURL = prev })
		feed := ReadStatusFeed(context.Background())
		if feed.Read || feed.Failure != FeedRefused || feed.Reason == "" {
			t.Errorf("a closed port: %+v, want refused with a reason", feed)
		}
	})

	t.Run("timed out", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		prev, prevTimeout := StatusURL, StatusTimeout
		StatusURL, StatusTimeout = srv.URL, 100*time.Millisecond
		t.Cleanup(func() { StatusURL, StatusTimeout = prev, prevTimeout })
		feed := ReadStatusFeed(context.Background())
		if feed.Read || feed.Failure != FeedTimedOut || feed.Reason == "" {
			t.Errorf("a feed that never answers: %+v, want timed out with a reason", feed)
		}
	})
}

// One function reads the feed. A source file other than platformhealth.go that
// names the feed's path or asks for its address is a second reader, and a second
// reader is how the popup and the 5xx note came to decode the same rows twice.
func TestTheStatusFeedHasOneReader(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	readers := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		isOwner := rel == filepath.Join("common", "platformhealth.go")
		ast.Inspect(file, func(n ast.Node) bool {
			var what string
			switch node := n.(type) {
			case *ast.BasicLit:
				if v, uerr := strconv.Unquote(node.Value); node.Kind == token.STRING && uerr == nil && strings.HasSuffix(v, "/ops/status") {
					what = "names the feed's path " + node.Value
				}
			case *ast.Ident:
				if node.Name == "StatusFeedURL" {
					what = "uses StatusFeedURL"
				}
			case *ast.SelectorExpr:
				if node.Sel.Name == "StatusFeedURL" {
					what = "uses StatusFeedURL"
				}
			}
			if what == "" {
				return true
			}
			if isOwner {
				readers++
				return true
			}
			t.Errorf("%s %s: the feed is read by common.ReadStatusFeed and nowhere else", rel, what)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("reading the module's source: %v", err)
	}
	if readers == 0 {
		t.Error("platformhealth.go does not name the feed, so this test cannot tell a reader from none")
	}
}
