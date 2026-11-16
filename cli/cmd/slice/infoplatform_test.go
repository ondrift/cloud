package slice

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// platformStub answers the routes `drift slice info` reads. `actions` is the
// whole behaviour of GET /ops/slice/actions: the status and body it returns.
type platformStub struct {
	actionsStatus int
	actionsBody   string
	schedulerDown bool
}

func (p platformStub) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ops/slice/actions":
		w.WriteHeader(p.actionsStatus)
		_, _ = w.Write([]byte(p.actionsBody))
	case "/ops/atomic/scheduler":
		if p.schedulerDown {
			http.Error(w, "scheduler unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"slice":{"committed_bytes":0,"budget_bytes":0},"pools":[]}`))
	case "/ops/atomic/workers":
		_, _ = w.Write([]byte(`[]`))
	case "/ops/slice/quota":
		_, _ = w.Write([]byte(`{}`))
	default:
		_, _ = w.Write([]byte(`[]`))
	}
}

// runInfo runs `drift slice info` against the stub and returns what it printed
// and the error it returned.
func runInfo(t *testing.T, p platformStub) (string, error) {
	t.Helper()
	stubLinkAPI(t, p.serve)
	cmd := getInfoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.RunE(cmd, nil)
	return out.String(), err
}

// actionsDocument renders the route's reply: the checks as given and the
// actions as given, each as the platform's JSON.
func actionsDocument(t *testing.T, checks, actions []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"checks": checks, "actions": actions})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

func check(label string, every int, last any) map[string]any {
	return map[string]any{"label": label, "every_seconds": every, "last": last}
}

func action(summary string, at time.Duration) map[string]any {
	return map[string]any{"at": ago(at), "summary": summary, "outcome": "success", "transient": false}
}

// The section names every check the route returns, how long ago it last ran
// and how often it should: one line each, however many there are.
func TestInfoPlatform_OneLinePerCheckTheRouteReturns(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{
			check("Slice keys", 3600, ago(10*time.Minute)),
			check("Slice build", 7200, ago(2*time.Hour)),
			check("Unfinished snapshots", 3600, ago(5*time.Minute)),
			check("Idle slices", 7200, ago(30*time.Minute)),
			check("Site password gate", 3600, ago(20*time.Minute)),
			check("A check that does not exist yet", 3600, ago(40*time.Minute)),
		},
		[]map[string]any{action("The slice was warmed up.", 3*time.Hour)})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc})
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Slice keys: checked 10m ago, every 1h",
		"Slice build: checked 2h ago, every 2h",
		"Unfinished snapshots: checked 5m ago, every 1h",
		"Idle slices: checked 30m ago, every 2h",
		"Site password gate: checked 20m ago, every 1h",
		"A check that does not exist yet: checked 40m ago, every 1h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the platform section does not say %q:\n%s", want, out)
		}
	}
}

// The actions come newest first, each as its sentence and how long ago.
func TestInfoPlatform_ActionsAreSentencesWithHowLongAgo(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{check("Slice keys", 3600, ago(time.Minute))},
		[]map[string]any{
			action("The slice was put to sleep after a quiet week.", 2*time.Hour),
			action("The slice was warmed up.", 3*24*time.Hour),
		})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc})
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	first := strings.Index(out, "The slice was put to sleep after a quiet week.")
	second := strings.Index(out, "The slice was warmed up.")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("the actions are missing or not in the route's order:\n%s", out)
	}
	for _, want := range []string{"2h ago", "3d ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("the actions do not say %q:\n%s", want, out)
		}
	}
}

// A slice with nothing done to it says so, and still says when the platform
// last looked, so quiet and stopped read differently.
func TestInfoPlatform_QuietStillSaysWhenItLastLooked(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{check("Slice keys", 3600, ago(10*time.Minute))},
		[]map[string]any{})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc})
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing done to this slice in the last 7 days") {
		t.Errorf("a quiet slice does not say it was quiet:\n%s", out)
	}
	if !strings.Contains(out, "Slice keys: checked 10m ago, every 1h") {
		t.Errorf("a quiet slice does not say when the platform last checked:\n%s", out)
	}
}

// A check older than three intervals is overdue, in plain words.
func TestInfoPlatform_ACheckOlderThanThreeIntervalsIsOverdue(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{
			check("Slice keys", 20, ago(10*time.Minute)),
			check("Slice build", 3600, ago(10*time.Minute)),
		},
		[]map[string]any{})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc})
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	var keys, build string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "Slice keys"):
			keys = line
		case strings.Contains(line, "Slice build"):
			build = line
		}
	}
	if !strings.Contains(keys, "overdue") || !strings.Contains(keys, "10m ago") || !strings.Contains(keys, "every 20s") {
		t.Errorf("a check ten minutes late on a twenty second interval is not called overdue with its times: %q", keys)
	}
	if strings.Contains(build, "overdue") {
		t.Errorf("a check inside its interval is called overdue: %q", build)
	}
}

// A check with no time prints 'unknown', never a time.
func TestInfoPlatform_ACheckWithNoTimeIsUnknown(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{check("Slice keys", 20, nil)},
		[]map[string]any{})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc})
	if err != nil {
		t.Fatalf("info: %v\n%s", err, out)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Slice keys") {
			line = l
		}
	}
	if !strings.Contains(line, "unknown") || strings.Contains(line, "ago") || strings.Contains(line, "overdue") {
		t.Errorf("a check with no time is not printed as unknown: %q", line)
	}
}

// When the actions cannot be read the section says so with the reason, and the
// rest of the command still prints. A 404 from an older API is the same line.
func TestInfoPlatform_UnreadableSaysSoAndTheRestStillPrints(t *testing.T) {
	for name, p := range map[string]platformStub{
		"server error": {actionsStatus: 503, actionsBody: `{"error":"audit unavailable"}`},
		"older api":    {actionsStatus: 404, actionsBody: "404 page not found"},
		"not json":     {actionsStatus: 200, actionsBody: "<html>"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runInfo(t, p)
			if err != nil {
				t.Fatalf("an unreadable platform section failed the command: %v\n%s", err, out)
			}
			if !strings.Contains(out, "couldn't be read: ") {
				t.Errorf("the section does not say it couldn't be read:\n%s", out)
			}
			if !strings.Contains(out, "compute") || !strings.Contains(out, "shape") {
				t.Errorf("the rest of the command did not print:\n%s", out)
			}
		})
	}
}

// When the scheduler cannot be read the slice name and the platform section are
// still printed, and the scheduler's error is returned after them.
func TestInfoPlatform_SchedulerDownStillPrintsNameAndPlatform(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{check("Slice keys", 3600, ago(10*time.Minute))},
		[]map[string]any{action("The slice was warmed up.", time.Hour)})
	out, err := runInfo(t, platformStub{actionsStatus: 200, actionsBody: doc, schedulerDown: true})
	if err == nil {
		t.Fatalf("the scheduler's error was swallowed:\n%s", out)
	}
	if !strings.Contains(err.Error(), "scheduler") {
		t.Errorf("the error returned is not the scheduler's: %v", err)
	}
	if !strings.HasPrefix(out, "app\n") {
		t.Errorf("the slice name is not printed first:\n%s", out)
	}
	for _, want := range []string{"Slice keys: checked 10m ago, every 1h", "The slice was warmed up."} {
		if !strings.Contains(out, want) {
			t.Errorf("the platform section lacks %q when the scheduler is down:\n%s", want, out)
		}
	}
}

// Nothing the section prints names the infrastructure under the slice.
func TestInfoPlatform_NamesNoInfrastructure(t *testing.T) {
	doc := actionsDocument(t,
		[]map[string]any{check("Slice keys", 20, nil), check("Slice build", 20, ago(time.Hour))},
		[]map[string]any{action("The slice was warmed up.", time.Hour)})
	for name, p := range map[string]platformStub{
		"readable":   {actionsStatus: 200, actionsBody: doc},
		"unreadable": {actionsStatus: 404, actionsBody: "404 page not found"},
		"quiet":      {actionsStatus: 200, actionsBody: actionsDocument(t, nil, nil)},
	} {
		t.Run(name, func(t *testing.T) {
			out, _ := runInfo(t, p)
			lower := strings.ToLower(out)
			for _, leak := range []string{"pod", "kubernetes", "node", "container", "namespace", "deployment"} {
				if strings.Contains(lower, leak) {
					t.Errorf("the output names infrastructure (%q):\n%s", leak, out)
				}
			}
			if !strings.Contains(out, "platform") {
				t.Errorf("there is no platform section to have checked:\n%s", out)
			}
		})
	}
}

// The slice command has no events verb: the platform's actions are read where a
// person already looks, not from a feed.
func TestSliceHasNoEventsVerb(t *testing.T) {
	for _, c := range GetCmd().Commands() {
		if c.Name() == "events" || c.HasAlias("events") {
			t.Errorf("`drift slice %s` exists; the platform's actions are not a feed", c.Name())
		}
	}
}

// The help says the command now reads the platform's side.
func TestInfoHelpNamesThePlatformSection(t *testing.T) {
	long := getInfoCmd().Long
	for _, want := range []string{"what the platform did", "last looked"} {
		if !strings.Contains(long, want) {
			t.Errorf("`drift slice info --help` does not say %q:\n%s", want, long)
		}
	}
}
