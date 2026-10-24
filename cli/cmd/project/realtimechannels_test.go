package project

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realtimeRoute is where the api forwards the slice's own client-channel route.
const realtimeRoute = "/ops/backbone/realtime/channels"

// needRealtimeSchema installs the platform's schema and skips when it is older
// than the release that added `backbone.realtime`: a file declaring the block
// cannot compile against a schema that has no such key. The cases run wherever
// the api serves 2.1.0 or later.
func needRealtimeSchema(t *testing.T) {
	t.Helper()
	withRealSchema(t)
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".drift", "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"realtimeSection"`) {
		t.Skip("the cached Driftfile schema predates 2.1.0 and has no backbone.realtime")
	}
}

// realtimeManifest is a manifest whose document carries the given
// backbone.realtime value, for the cases that do not need a compile.
func realtimeManifest(realtime any) *Manifest {
	return &Manifest{slice: Node{"backbone": map[string]any{"realtime": realtime}}}
}

const realtimeFile = `slice "demo" { billing = "monthly" }

site "/" { dir = "site" }

realtime {
  client_channels = ["chat", "room:*"]
}
`

const noRealtimeFile = `slice "demo" { billing = "monthly" }

site "/" { dir = "site" }
`

// ─── reading the document ───────────────────────────────────────────────────

// A file with no realtime block declares nothing.
func TestClientChannels_AbsentDeclaresNothing(t *testing.T) {
	m := &Manifest{slice: Node{"backbone": map[string]any{}}}
	declared, channels, err := declaredClientChannels(m)
	if err != nil || declared || len(channels) != 0 {
		t.Errorf("declaredClientChannels = %v %v %v, want nothing declared", declared, channels, err)
	}
	if err := preflightClientChannels(m); err != nil {
		t.Errorf("an absent realtime block failed the preflight: %v", err)
	}
}

// The list is read as written, and an empty one is a declaration: it closes
// every channel.
func TestClientChannels_ReadsTheDeclaredList(t *testing.T) {
	declared, channels, err := declaredClientChannels(realtimeManifest(map[string]any{
		"client_channels": []any{"chat", "room:*"},
	}))
	if err != nil || !declared || strings.Join(channels, ",") != "chat,room:*" {
		t.Errorf("declaredClientChannels = %v %v %v, want chat and room:*", declared, channels, err)
	}

	declared, channels, err = declaredClientChannels(realtimeManifest(map[string]any{
		"client_channels": []any{},
	}))
	if err != nil || !declared || len(channels) != 0 {
		t.Errorf("an empty list = %v %v %v, want a declaration of no channels", declared, channels, err)
	}
}

func TestClientChannels_PreflightRefusesWhatIsNotAListOfNames(t *testing.T) {
	for _, c := range []struct {
		realtime any
		want     string
	}{
		{map[string]any{"client_channels": "chat"},
			`realtime.client_channels must be a list of channel names, such as ["chat", "room:*"]`},
		{map[string]any{"client_channels": []any{"chat", 7}},
			`realtime.client_channels[1]: 7 is not a channel name`},
		{map[string]any{},
			`realtime.client_channels must be a list of channel names, such as ["chat", "room:*"]`},
	} {
		err := preflightClientChannels(realtimeManifest(c.realtime))
		if err == nil || err.Error() != c.want {
			t.Errorf("preflight of %v = %v, want %q", c.realtime, err, c.want)
		}
	}
}

// ─── compiling the block ────────────────────────────────────────────────────

func TestCompile_RealtimeWritesTheDocumentKey(t *testing.T) {
	needRealtimeSchema(t)
	c, err := CompileDriftfile(writeDriftfile(t, realtimeFile, "site/"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := c.Manifest.Slice().Strings("backbone", "realtime", "client_channels")
	if strings.Join(got, ",") != "chat,room:*" {
		t.Errorf("backbone.realtime.client_channels = %v, want chat and room:*", got)
	}
}

func TestCompile_AnEmptyRealtimeListIsKept(t *testing.T) {
	needRealtimeSchema(t)
	c, err := CompileDriftfile(writeDriftfile(t,
		noRealtimeFile+"realtime {\n  client_channels = []\n}\n", "site/"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !c.Manifest.Slice().Has("backbone", "realtime", "client_channels") {
		t.Error("an empty client_channels was dropped from the document, so it would not close anything")
	}
}

func TestCompile_RealtimeMustNameClientChannels(t *testing.T) {
	needRealtimeSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, noRealtimeFile+"realtime {}\n", "site/"))
	if err == nil || !strings.Contains(err.Error(), "client_channels") {
		t.Errorf("an empty realtime block compiled, or was refused without naming client_channels: %v", err)
	}
}

func TestCompile_OneRealtimeBlock(t *testing.T) {
	needRealtimeSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t,
		noRealtimeFile+"realtime {\n  client_channels = [\"a\"]\n}\nrealtime {\n  client_channels = [\"b\"]\n}\n", "site/"))
	if err == nil || !strings.Contains(err.Error(), "one realtime block") {
		t.Errorf("two realtime blocks = %v, want a refusal saying a slice has one", err)
	}
}

// An entry the slice would refuse is refused by the schema, at the file's line.
func TestCompile_AnInvalidChannelIsRefusedAtItsLine(t *testing.T) {
	needRealtimeSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t,
		noRealtimeFile+"realtime {\n  client_channels = [\"chat\", \"not a channel\"]\n}\n", "site/"))
	if err == nil || !strings.Contains(err.Error(), "Driftfile:5:") {
		t.Errorf("an invalid channel = %v, want a refusal at Driftfile:5", err)
	}
}

// ─── applying ───────────────────────────────────────────────────────────────

type channelsSlice struct {
	rec    *recorder
	live   []string // what GET answers, as the slice's route does
	status int      // GET's status; 0 is 200
	posts  []string
	refuse *edgeReply
}

// realtimeSlice lays out body beside a monthly slice that already holds the
// file's shape, and answers the client-channel route from s.
func realtimeSlice(t *testing.T, body string, s *channelsSlice) {
	t.Helper()
	live := ""
	s.rec = stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/atomic/list", "/ops/backbone/secret/list":
			_, _ = w.Write([]byte(`[]`))
		case "/ops/canvas":
			_, _ = w.Write([]byte(`{"sites":[]}`))
		case realtimeRoute:
			if got := r.Header.Get("X-Slice"); got != "demo" {
				t.Errorf("%s %s acted on slice %q, want demo", r.Method, realtimeRoute, got)
			}
			switch r.Method {
			case http.MethodGet:
				if s.status != 0 {
					w.WriteHeader(s.status)
					_, _ = w.Write([]byte(`{"error":"not found"}`))
					return
				}
				declared := s.live != nil
				list := s.live
				if list == nil {
					list = []string{}
				}
				raw, _ := json.Marshal(map[string]any{"declared": declared, "channels": list, "refused_frames": 0})
				_, _ = w.Write(raw)
			case http.MethodPost:
				if s.refuse != nil {
					w.WriteHeader(s.refuse.status)
					_, _ = w.Write([]byte(s.refuse.body))
					return
				}
				raw, _ := io.ReadAll(r.Body)
				s.posts = append(s.posts, string(raw))
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})
	needRealtimeSchema(t)
	applyIn(t, body, "site")
	live = liveAtFileShape(t)
}

// The declared list reaches the slice as written, once.
func TestApply_SendsTheDeclaredClientChannels(t *testing.T) {
	s := &channelsSlice{}
	realtimeSlice(t, realtimeFile, s)

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(s.posts) != 1 || s.posts[0] != `{"channels":["chat","room:*"]}` {
		t.Errorf("sent %q, want one {\"channels\":[\"chat\",\"room:*\"]}", s.posts)
	}
	if !strings.Contains(out, "chat") || !strings.Contains(out, "room:*") {
		t.Errorf("the apply did not say which channels it opened:\n%s", out)
	}
}

// An empty list is a declaration: it is sent as an empty list, not as null, and
// it closes what was open.
func TestApply_AnEmptyListClosesEveryChannel(t *testing.T) {
	s := &channelsSlice{live: []string{"chat"}}
	realtimeSlice(t, noRealtimeFile+"realtime {\n  client_channels = []\n}\n", s)

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(s.posts) != 1 || s.posts[0] != `{"channels":[]}` {
		t.Errorf("sent %q, want one {\"channels\":[]}", s.posts)
	}
}

// Taking the block out of the file closes what it opened: the list is the whole
// truth, so a file that no longer names a channel no longer opens it.
func TestApply_RemovingTheBlockClosesWhatItOpened(t *testing.T) {
	s := &channelsSlice{live: []string{"chat", "room:*"}}
	realtimeSlice(t, noRealtimeFile, s)

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(s.posts) != 1 || s.posts[0] != `{"channels":[]}` {
		t.Errorf("sent %q, want one {\"channels\":[]}", s.posts)
	}
	if !strings.Contains(out, "closed") {
		t.Errorf("the apply did not say it closed the channels:\n%s", out)
	}
}

// A file with no block, on a slice that has nothing open, writes nothing.
func TestApply_NoBlockAndNothingOpenWritesNothing(t *testing.T) {
	s := &channelsSlice{live: []string{}}
	realtimeSlice(t, noRealtimeFile, s)

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(s.posts) != 0 {
		t.Errorf("sent %q for a slice with nothing open and a file that opens nothing", s.posts)
	}
}

// A slice whose runtime has no such route must still apply a file that does not
// ask for client channels.
func TestApply_ASliceWithoutTheRouteStillAppliesAFileThatDoesNotUseIt(t *testing.T) {
	s := &channelsSlice{status: http.StatusNotFound}
	realtimeSlice(t, noRealtimeFile, s)

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(s.posts) != 0 {
		t.Errorf("sent %q to a slice that has no route", s.posts)
	}
}

// A file that DOES ask, against a slice that cannot honour it, fails and says
// whose declaration it was.
func TestApply_SurfacesTheRefusal(t *testing.T) {
	s := &channelsSlice{refuse: &edgeReply{http.StatusBadRequest, `{"error":"invalid client channel \"x y\""}`}}
	realtimeSlice(t, realtimeFile, s)

	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), "realtime.client_channels") ||
		!strings.Contains(err.Error(), `invalid client channel`) {
		t.Errorf("apply returned %v, want the slice's refusal under realtime.client_channels", err)
	}
}

// ─── planning ───────────────────────────────────────────────────────────────

func TestPlan_ShowsWhichChannelsOpenAndWhichClose(t *testing.T) {
	s := &channelsSlice{live: []string{"chat", "old"}}
	realtimeSlice(t, realtimeFile, s)

	var err error
	out := captureStdout(t, func() { err = runPlanCmd(t) })
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"realtime chat", "+ realtime room:*", "- realtime old"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not show %q:\n%s", want, out)
		}
	}
	if len(s.posts) != 0 {
		t.Errorf("the plan wrote to the slice: %q", s.posts)
	}
	if n := s.rec.count("POST " + realtimeRoute); n != 0 {
		t.Errorf("the plan POSTed %d × to %s", n, realtimeRoute)
	}
}

// Closing a channel is not a removal that needs an answer: no data goes with it.
func TestPlan_ClosingAChannelAsksNothing(t *testing.T) {
	s := &channelsSlice{live: []string{"chat", "old"}}
	realtimeSlice(t, realtimeFile, s)

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("an apply that closes a channel was refused: %v\n%s", err, out)
	}
	if len(s.posts) != 1 {
		t.Errorf("sent %q, want one write", s.posts)
	}
}

// A slice that cannot be read is said to be unknown, not shown as closed.
func TestPlan_AnUnreadableSliceIsUnknownNotClosed(t *testing.T) {
	s := &channelsSlice{status: http.StatusNotFound}
	realtimeSlice(t, realtimeFile, s)

	var err error
	out := captureStdout(t, func() { err = runPlanCmd(t) })
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "realtime") || !strings.Contains(out, "unknown") {
		t.Errorf("the plan does not say the live channels are unknown:\n%s", out)
	}
}
