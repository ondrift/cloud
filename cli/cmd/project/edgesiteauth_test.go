package project

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestEdgeSiteAuth_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeSiteAuth(m); err != nil {
		t.Errorf("an absent edge.site_auth failed the preflight: %v", err)
	}
	if err := applyEdgeSiteAuth(m); err != nil {
		t.Errorf("an absent edge.site_auth failed the apply: %v", err)
	}
}

// siteAuthFile is a Driftfile gating the site for alice and bob.
const siteAuthFile = `slice "demo" {}

canvas {
  site "/" { dir = "site" }
}

edge {
  site_auth {
    realm = "Staging"
    paths = ["/admin", "/secret/page"]
    user "alice" { password = env("SITE_PASS_ALICE") }
    user "bob" { password = env("SITE_PASS_BOB") }
  }
}
`

// siteAuthSlice lays out body as the working directory's project, beside a
// monthly slice that already holds the file's shape. `auth` answers the
// site-auth gate's route, which is the part each test decides.
func siteAuthSlice(t *testing.T, body string, auth http.HandlerFunc) *recorder {
	t.Helper()
	live := ""
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/slice/auth":
			auth(w, r)
		case "/ops/atomic/list", "/ops/backbone/secret/list":
			_, _ = w.Write([]byte(`[]`))
		case "/ops/canvas":
			_, _ = w.Write([]byte(`{"sites":[]}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})
	// After the stub, which gives the test a HOME of its own.
	withRealSchema(t)
	applyIn(t, body, "site")
	live = liveAtFileShape(t)
	return rec
}

// THE WHOLE TRUTH. The live gate lets alice and carol in; the file names alice
// and bob. What is sent is exactly the file's set, with each password read from
// the variable it names, so carol is gone and bob is in.
func TestApply_SiteAuthSetsExactlyTheDeclaredUsers(t *testing.T) {
	t.Setenv("SITE_PASS_ALICE", "alice-secret")
	t.Setenv("SITE_PASS_BOB", "bob-secret")
	var sent []map[string]any
	rec := siteAuthSlice(t, siteAuthFile, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Slice"); got != "demo" {
			t.Errorf("%s /ops/slice/auth acted on slice %q, want demo", r.Method, got)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"enabled":true,"users":[{"name":"alice"},{"name":"carol"}]}`))
		case http.MethodPost:
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body)
			_, _ = w.Write([]byte(`{"enabled":true,"realm":"Staging","users":[{"name":"alice"},{"name":"bob"}]}`))
		}
	})

	var err error
	out := captureStdout(t, func() { err = runApplyAgreeingToRemove(t, "site_auth carol") })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(sent) != 1 {
		t.Fatalf("%d gate writes, want exactly one", len(sent))
	}
	got, _ := json.Marshal(sent[0])
	want := `{"enabled":true,"paths":["/admin","/secret/page"],"realm":"Staging","users":[{"name":"alice","password":"alice-secret"},{"name":"bob","password":"bob-secret"}]}`
	if string(got) != want {
		t.Errorf("gate write = %s\nwant          %s", got, want)
	}
	if n := rec.count("DELETE /ops/slice/auth"); n != 0 {
		t.Errorf("apply removed the gate %d × on the way to setting it", n)
	}
	for _, secret := range []string{"alice-secret", "bob-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("the apply printed a password:\n%s", out)
		}
	}
	if !strings.Contains(out, "carol") {
		t.Errorf("the apply did not say carol was removed:\n%s", out)
	}
}

// A user the live gate holds and the file does not name is a removal, and an
// apply that was not told it may remove carol sends nothing at all: the gate
// is not rewritten around the person who was never asked.
func TestApply_SiteAuthRemovingAUserNeedsAnAnswer(t *testing.T) {
	t.Setenv("SITE_PASS_ALICE", "alice-secret")
	t.Setenv("SITE_PASS_BOB", "bob-secret")
	rec := siteAuthSlice(t, siteAuthFile, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"enabled":true,"users":[{"name":"alice"},{"name":"carol"}]}`))
		}
	})

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err == nil || !strings.Contains(err.Error(), `--approve-remove "site_auth carol"`) {
		t.Fatalf("an unanswered removal of carol was not refused with its flag: %v\n%s", err, out)
	}
	if n := rec.count("POST /ops/slice/auth"); n != 0 {
		t.Errorf("the gate was written %d × before the removal was agreed", n)
	}
}

// AN UNSET PASSWORD REFUSES THE FILE BEFORE ANYTHING DEPLOYS. Sending the gate
// without it is impossible, and sending everything else first would leave the
// site live behind a gate the file says is there and is not.
func TestApply_SiteAuthRefusesAnUnsetPasswordBeforeAnyRoute(t *testing.T) {
	t.Setenv("SITE_PASS_ALICE", "alice-secret")
	t.Setenv("SITE_PASS_BOB", "")
	if err := os.Unsetenv("SITE_PASS_BOB"); err != nil {
		t.Fatal(err)
	}
	assertApplyRefusesBeforeAnyRoute(t, siteAuthFile,
		`edge.site_auth: the password for user "bob" is read from $SITE_PASS_BOB, which is not set. `+
			`Export it, pass --secret SITE_PASS_BOB=..., or add it to the .env beside the Driftfile`)
}

// Set and empty is refused the same way: an empty password is no password.
func TestApply_SiteAuthRefusesAnEmptyPasswordBeforeAnyRoute(t *testing.T) {
	t.Setenv("SITE_PASS_ALICE", "")
	t.Setenv("SITE_PASS_BOB", "bob-secret")
	assertApplyRefusesBeforeAnyRoute(t, siteAuthFile,
		`edge.site_auth: the password for user "alice" is read from $SITE_PASS_ALICE, which is empty. `+
			`Export it, pass --secret SITE_PASS_ALICE=..., or add it to the .env beside the Driftfile`)
}

// A password written out in the file is refused, and the refusal never repeats
// it: the reader's terminal and CI log are not where it belongs either.
func TestEdgeSiteAuth_RefusesALiteralWithoutPrintingIt(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"site_auth": map[string]any{
		"paths": []any{"*"},
		"users": []any{map[string]any{"user": "alice", "password": "hunter2"}},
	}}}}
	err := preflightEdgeSiteAuth(m)
	if err == nil {
		t.Fatal("a literal password was accepted")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal printed the password: %v", err)
	}
	if !strings.Contains(err.Error(), `"alice"`) {
		t.Errorf("the refusal does not name the user: %v", err)
	}
}

// The same refusal where the file is read: a Driftfile that writes the password
// instead of env("NAME") does not compile, and the message neither carries the
// password nor loses the user it belongs to.
func TestCompile_RefusesALiteralSitePasswordWithoutPrintingIt(t *testing.T) {
	withRealSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n"+
		"edge {\n  site_auth {\n    paths = [\"*\"]\n    user \"alice\" { password = \"hunter2\" }\n  }\n}\n"))
	if err == nil {
		t.Fatal("a literal password compiled")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal printed the password: %v", err)
	}
	if !strings.Contains(err.Error(), `"alice"`) {
		t.Errorf("the refusal does not name the user: %v", err)
	}
}

// Two entries for one user cannot both be the truth, and the platform refuses
// the write after everything else is live. Refused first, here.
func TestEdgeSiteAuth_RefusesADuplicateUser(t *testing.T) {
	t.Setenv("P1", "x")
	t.Setenv("P2", "y")
	m := &Manifest{slice: Node{"edge": map[string]any{"site_auth": map[string]any{
		"paths": []any{"*"},
		"users": []any{
			map[string]any{"user": "alice", "password": "$P1"},
			map[string]any{"user": "alice", "password": "$P2"},
		},
	}}}}
	err := preflightEdgeSiteAuth(m)
	if err == nil || err.Error() != `edge.site_auth: user "alice" is declared more than once` {
		t.Errorf("err = %v, want a refusal naming the repeated user", err)
	}
}

// A gate that does not say which paths it covers is refused where the file is
// read, with its position. Neither reading of the omission is safe: "every path"
// locks a site because a line was left out, and "none" protects nothing.
func TestCompile_SiteAuthMustNameItsPaths(t *testing.T) {
	withRealSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n"+
		"edge {\n  site_auth {\n    user \"alice\" { password = env(\"P\") }\n  }\n}\n"))
	if err == nil {
		t.Fatal("a site_auth with no paths compiled")
	}
	for _, want := range []string{"Missing paths", `paths = ["*"]`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// Every shape that could never match, or that means something other than it
// reads as, is refused at its line.
func TestCompile_SiteAuthRefusesAPathThatCouldNeverMatch(t *testing.T) {
	withRealSchema(t)
	for name, paths := range map[string]string{
		"relative":           `["admin"]`,
		"the root":           `["/"]`,
		"star and a path":    `["*", "/admin"]`,
		"a dot segment":      `["/a/../b"]`,
		"a query":            `["/a?x=1"]`,
		"the same one twice": `["/admin", "/admin/"]`,
		"none":               `[]`,
	} {
		_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n"+
			"edge {\n  site_auth {\n    paths = "+paths+"\n    user \"alice\" { password = env(\"P\") }\n  }\n}\n"))
		if err == nil {
			t.Errorf("%s: paths = %s compiled", name, paths)
		}
	}
}

// One trailing slash is dropped, so the document carries what the platform
// stores and a plan compares like with like.
func TestCompile_SiteAuthPathsAreWrittenNormalised(t *testing.T) {
	withRealSchema(t)
	c, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n"+
		"edge {\n  site_auth {\n    paths = [\"/admin/\", \"/secret/page\"]\n    user \"alice\" { password = env(\"P\") }\n  }\n}\n"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := c.Manifest.Slice().Sub("edge", "site_auth").Strings("paths")
	if strings.Join(got, ",") != "/admin,/secret/page" {
		t.Errorf("paths = %v, want /admin and /secret/page", got)
	}
}

// A gate set before paths existed reports none and covers the whole site, so a
// plan against it shows a narrower declaration as a change and an explicit `*`
// as no change.
func TestSamePaths_AGateWithNoPathsCoversTheWholeSite(t *testing.T) {
	if !samePaths(nil, []string{"*"}) {
		t.Error("a live gate reporting no paths differs from `*`, which is the whole site it covers")
	}
	if samePaths(nil, []string{"/admin"}) {
		t.Error("a whole-site gate was read as the same as /admin")
	}
	if !samePaths([]string{"/b", "/a"}, []string{"/a", "/b"}) {
		t.Error("the order of the paths is a change")
	}
}

// Absent, the gate is left exactly as it is: not read, not written, not
// removed.
func TestApply_AbsentSiteAuthCallsNoAuthRoute(t *testing.T) {
	rec := siteAuthSlice(t, "slice \"demo\" {}\ncanvas {\n  site \"/\" { dir = \"site\" }\n}\n",
		func(w http.ResponseWriter, r *http.Request) {})

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if n := rec.count(m + " /ops/slice/auth"); n != 0 {
			t.Errorf("a file with no edge.site_auth issued %d × %s /ops/slice/auth", n, m)
		}
	}
}

// The plan names who would be let in and who would be shut out, never a
// password, and writes nothing. An unset variable is reported by NAME, because
// the apply that follows would refuse on it.
func TestPlan_SiteAuthShowsUsersAndNeverAPassword(t *testing.T) {
	t.Setenv("SITE_PASS_ALICE", "alice-secret")
	t.Setenv("SITE_PASS_BOB", "")
	if err := os.Unsetenv("SITE_PASS_BOB"); err != nil {
		t.Fatal(err)
	}
	rec := siteAuthSlice(t, siteAuthFile, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"enabled":true,"realm":"Old","users":[{"name":"alice"},{"name":"carol"}]}`))
		}
	})

	var err error
	out := captureStdout(t, func() { err = runPlanCmd(t) })
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{
		"site_auth",
		"site_auth alice",
		"! site_auth bob",
		"- site_auth carol",
		"realm Old → Staging",
		"the whole site → /admin, /secret/page",
		"$SITE_PASS_BOB is not set here",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alice-secret") {
		t.Errorf("the plan printed a password:\n%s", out)
	}
	for _, m := range []string{"POST", "DELETE"} {
		if n := rec.count(m + " /ops/slice/auth"); n != 0 {
			t.Errorf("A PLAN ISSUED %d × %s /ops/slice/auth", n, m)
		}
	}
}
