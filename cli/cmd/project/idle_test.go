package project

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE case. The Driftfile names five functions, the slice declares six slots,
// and the apply succeeds, because there is room. The sixth slot is billed for
// its booking while the tenant's own declaration says they do not want it, and
// the end of the apply is the one place both numbers are in hand.
//
// Driven through the real command, so the wiring is under test: the report must
// land after a SUCCESSFUL apply, leave the outcome alone, and reach for nothing
// that changes the slice.
func TestApply_SaysWhenTheSliceDeclaresASlotTheDriftfileDoesNotName(t *testing.T) {
	routes := []string{"ping", "users", "orders", "health", "login"}
	slots := make([]string, 0, 6)
	for _, r := range routes {
		slots = append(slots, "get:"+r)
	}
	slots = append(slots, "get:slot-6")

	rec := stubAPI(t, idleSliceStub("custom", slots))
	applyWithFunctions(t, routes)

	// The apply brings the slice to the file's shape, so the sixth slot is a
	// removal it is told to make. The stub keeps answering with the six slots it
	// was given, which is the slice as the report reads it after the deploy.
	var err error
	out := captureStdout(t, func() { err = runApplyAgreeingToRemove(t, "pulse GET /slot-6") })

	if err != nil {
		t.Fatalf("the apply must succeed, there is room, and the report must not change that: %v\n%s", err, out)
	}
	for _, want := range []string{
		"This slice declares 6 atomic slots and your Driftfile names 5.",
		"get:slot-6 is idle, and you are paying for its 16MiB booking.",
		"Run `drift slice resize demo` to release it.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the apply must say %q, got:\n%s", want, out)
		}
	}
	for _, named := range slots[:5] {
		if strings.Contains(out, named+" is idle") {
			t.Errorf("%s is named by the Driftfile and must not be reported idle:\n%s", named, out)
		}
	}
	// After the deploy, not in the middle of it.
	if strings.Index(out, "This slice declares") < strings.Index(out, "Done in") {
		t.Errorf("the report must come after the apply finished:\n%s", out)
	}

	// It reports. The apply sizes the slice once, before it deploys anything;
	// nothing the report does afterwards may reach a route that changes a slice.
	if n := rec.count("POST /ops/slice/create"); n != 0 {
		t.Errorf("the apply issued %d × POST /ops/slice/create for a slice that exists", n)
	}
	resized := rec.position("POST /ops/slice/resize")
	deployed := rec.position("POST /ops/atomic")
	if n := rec.count("POST /ops/slice/resize"); n != 1 || resized > deployed {
		t.Errorf("the slice must be resized once, before the deploy (resize at %d, deploy at %d, %d resizes)",
			resized, deployed, n)
	}
	// The last thing the apply does is read the slice for the report.
	if n := len(rec.hits); n == 0 || rec.hits[n-1] != "GET /ops/slice/get" {
		t.Errorf("the apply must end on the read the report is made from, got %v", rec.hits)
	}
}

// The control for the test above, and what stops it passing on noise: a slice
// declaring exactly what the Driftfile names prints no block at all.
func TestApply_SaysNothingWhenTheSetsAreEqual(t *testing.T) {
	routes := []string{"ping", "users"}
	stubAPI(t, idleSliceStub("custom", []string{"get:ping", "get:users"}))
	applyWithFunctions(t, routes)

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })

	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Done in") {
		t.Fatalf("the apply never finished, so its silence proves nothing:\n%s", out)
	}
	if strings.Contains(out, "This slice declares") || strings.Contains(out, "drift slice resize") {
		t.Errorf("nothing is idle, so nothing must be said:\n%s", out)
	}
}

// A slot serving a function this Driftfile does not name is not idle. Another
// project on the same slice may own it, and ReportOrphanedFunctions already
// names it; calling it idle would tell the tenant to release a live function.
func TestReportIdle_AServingSlotIsNotIdle(t *testing.T) {
	m := manifestNaming([]string{"get:ping"}, nil, nil, nil)
	live := idleLive("custom", []string{"get:ping", "get:theirs", "get:slot-3"})

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, deployedSet("get:ping", "get:theirs"), &out)

	got := out.String()
	if strings.Contains(got, "get:theirs") {
		t.Errorf("a slot serving a function is not idle:\n%s", got)
	}
	if !strings.Contains(got, "get:slot-3 is idle") {
		t.Errorf("the slot nothing serves must still be reported:\n%s", got)
	}
}

// Without the list of what the slice serves, a slot can only be called
// unnamed. "Idle" would be a claim this run could not check.
func TestReportIdle_WithoutTheServingListASlotIsUnnamedNotIdle(t *testing.T) {
	m := manifestNaming([]string{"get:ping"}, nil, nil, nil)
	live := idleLive("custom", []string{"get:ping", "get:slot-2"})

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, nil, &out)

	got := out.String()
	if !strings.Contains(got, "get:slot-2 is not named by your Driftfile, and you are paying for its 16MiB booking.") {
		t.Errorf("the slot must be reported as unnamed:\n%s", got)
	}
	if strings.Contains(got, "is idle") {
		t.Errorf("nothing checked what the slice serves, so nothing may be called idle:\n%s", got)
	}
}

// The compile folds every identity a Driftfile declares onto the booking key the
// slice stores, and the comparison happens after it: a pulse labelled with its
// method and route, a pulse labelled QUEUE and the queue it drains, and a nosql
// collection all match the slot they book. A false idle here would tell the
// tenant to release the function they are running.
func TestReportIdle_EveryDeclaredIdentityMatchesItsSlot(t *testing.T) {
	withRealSchema(t)
	m := planManifest(t, `slice "demo" { billing = "monthly" }

atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "GetPing"
      memory  = "16MiB"
    }
    pulse "POST /users" {
      handler = "PostUsers"
      memory  = "16MiB"
    }
    pulse "QUEUE orders" {
      handler = "HandleOrders"
      memory  = "16MiB"
    }
  }
}

backbone {
  nosql "ops" { size = "5MiB" }

  queue "orders" {}
}
`, "atomic/")
	live := idleLive("custom", []string{"get:ping", "post:users", "queue:orders"})
	live.Config.Backbone.NoSQL.Collections = map[string]int{"ops": 5 << 20}

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, deployedSet(), &out)

	if out.Len() != 0 {
		t.Errorf("every declared identity names its slot, so nothing is idle:\n%s", out.String())
	}
}

// The same reasoning covers every declared set. Each class gets its own count
// line, every name it lists, and the resize command is said once at the end.
func TestReportIdle_NamesEachUnnamedStorageClass(t *testing.T) {
	m := manifestNaming(nil, []string{"ops"}, []string{"uploads"}, []string{"ledger"})
	live := idleLive("custom", nil)
	live.Config.Backbone.NoSQL.Collections = map[string]int{"ops": 5 << 20, "sessions": 20 << 20}
	live.Config.Backbone.Blobs.Buckets = map[string]int{"uploads": 5 << 20, "avatars": 10 << 20}
	live.Config.Backbone.SQL.Databases = map[string]int{"ledger": 5 << 20, "audit": 10 << 20}
	live.Config.Backbone.Queues.Queues = map[string]int{"emails": 500}

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, deployedSet(), &out)
	got := out.String()

	for _, want := range []string{
		"This slice declares 2 collections and your Driftfile names 1.",
		"sessions is not named by your Driftfile, and you are paying for its 20MiB of declared storage.",
		"This slice declares 2 buckets and your Driftfile names 1.",
		"avatars is not named by your Driftfile, and you are paying for its 10MiB of declared storage.",
		"This slice declares 2 databases and your Driftfile names 1.",
		"audit is not named by your Driftfile, and you are paying for its 10MiB of declared storage.",
		"This slice declares 1 queue and your Driftfile names none of them.",
		"emails is not named by your Driftfile; queues are not billed, so this one is free.",
		"Run `drift slice resize demo` to release them.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report must say %q, got:\n%s", want, got)
		}
	}
	for _, named := range []string{"ops is", "uploads is", "ledger is"} {
		if strings.Contains(got, named) {
			t.Errorf("%q is named by the Driftfile and must not be listed:\n%s", named, got)
		}
	}
	if n := strings.Count(got, "drift slice resize"); n != 1 {
		t.Errorf("the resize command must be said once, after every block, got %d:\n%s", n, got)
	}
}

// A queue-triggered function names the queue it drains, whether or not the
// file also declares it with a `queue` block. Reporting it would call a queue
// with a live consumer unnamed.
func TestReportIdle_AQueueATriggerDrainsIsNamed(t *testing.T) {
	m := manifestNaming([]string{"queue:orders"}, nil, nil, nil)
	live := idleLive("custom", []string{"queue:orders"})
	live.Config.Backbone.Queues.Queues = map[string]int{"orders": 500}

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, deployedSet("queue:orders"), &out)

	if out.Len() != 0 {
		t.Errorf("the queue a trigger drains is named, so nothing is idle:\n%s", out.String())
	}
}

// A free slice's sets are the preset's placeholders, granted at no charge and
// bounded by count and size rather than by name. None is paid for and
// releasing one saves nothing, so a free apply says nothing about them.
func TestReportIdle_AFreeSliceIsNotTold(t *testing.T) {
	m := manifestNaming([]string{"get:hello"}, nil, nil, nil)
	live := idleLive("hacker", []string{"get:hello", "get:slot-2", "get:slot-3", "get:slot-4", "get:slot-5"})
	live.Config.Backbone.NoSQL.Collections = map[string]int{"default": 20 << 20, "collection-2": 20 << 20}
	live.Config.Backbone.Queues.Queues = map[string]int{"queue-1": 500}

	var out bytes.Buffer
	ReportIdleDeclarations(m, live, deployedSet("get:hello"), &out)

	if out.Len() != 0 {
		t.Errorf("a free slice's grant is not something the tenant pays for, so it is not reported:\n%s", out.String())
	}
}

// `drift plan` names the slot the slice holds and the Driftfile does not as a
// removal, with the flag that agrees to it, and writes nothing to get there.
func TestPlan_ReportsTheUnnamedSlotAndChangesNothing(t *testing.T) {
	rec := stubAPI(t, idleSliceStub("custom", []string{"get:ping", "get:slot-2"}))
	applyWithFunctions(t, []string{"ping"})

	var err error
	out := captureStdout(t, func() { err = runPlanCmd(t) })

	if err != nil {
		t.Fatalf("a plan must not fail: %v", err)
	}
	for _, want := range []string{
		"- pulse GET /slot-2",
		"Removes   pulse GET /slot-2",
		`--approve-remove "pulse GET /slot-2"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan must say %q, got:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{
		"POST /ops/slice/resize", "POST /ops/slice/create", "POST /ops/atomic", "POST /ops/canvas",
	} {
		if n := rec.count(forbidden); n != 0 {
			t.Errorf("a plan issued %d × %q", n, forbidden)
		}
	}
}

// idleLive is a live slice of the given tier declaring the given slots, each
// booked at 16 MB.
func idleLive(tier string, slots []string) *LiveSlice {
	l := liveWith(slots, nil, nil, nil)
	l.Name = "demo"
	l.Tier = tier
	return l
}

// idleSliceStub answers as a slice declaring `slots`, serving nothing, ready,
// and accepting every deploy call.
func idleSliceStub(tier string, slots []string) http.HandlerFunc {
	var fns []string
	for _, s := range slots {
		method, route, _ := strings.Cut(s, ":")
		fns = append(fns, `{"name":"`+s+`","route":"`+route+`","method":"`+method+`","memory_bytes":16777216}`)
	}
	slice := `{"name":"demo","tier":"` + tier + `","config":{"atomic":{"functions":[` + strings.Join(fns, ",") + `]}}}`
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(slice))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/atomic/list", "/ops/backbone/secret/list", "/ops/backbone/sql/admin/list",
			"/ops/slice/domain", "/ops/atomic/alert":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}
}

// applyWithFunctions lays out a project declaring one interpreted GET Pulse per
// route, each booking 16MiB with its handler on disk. It installs the platform
// schema, so it is called after the stub, which gives the test a HOME of its own.
func applyWithFunctions(t *testing.T, routes []string) {
	t.Helper()
	var doc, src strings.Builder
	doc.WriteString("slice \"demo\" { billing = \"monthly\" }\n\natomic {\n  element \"default\" {\n")
	var exports []string
	for _, r := range routes {
		handler := "Get" + strings.ToUpper(r[:1]) + r[1:]
		doc.WriteString("    pulse \"GET /" + r + "\" {\n      handler = \"" + handler + "\"\n      memory  = \"16MiB\"\n    }\n")
		src.WriteString("function " + handler + "(req) { return [200, 'OK', {}]; }\n")
		exports = append(exports, handler)
	}
	doc.WriteString("  }\n}\n")
	src.WriteString("module.exports = { " + strings.Join(exports, ", ") + " };\n")

	withRealSchema(t)
	applyIn(t, doc.String(), "atomic")
	if err := os.WriteFile(filepath.Join(".", "atomic", "handlers.js"), []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn with stdout redirected, and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()

	fn()
	os.Stdout = prev
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
