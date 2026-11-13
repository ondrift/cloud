package project

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A plan's diff records every fact it could not read, so what the plan prints
// at its end is decided from that record and not from the wording of its lines.

// diffIn gathers the plan of the fxevm project, edited by edit, from the
// platform the test has stubbed, and returns its diff.
func diffIn(t *testing.T, edit func(string) string) *planDiff {
	t.Helper()
	inFixture(t, edit)
	c, err := CompileDriftfile("Driftfile")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(common.SliceEnv, c.Manifest.Name())
	p, err := gatherPlan(c, false)
	if err != nil {
		t.Fatalf("gather the plan: %v", err)
	}
	return p.diff()
}

// factsOf names each recorded fact as "address|what", sorted.
func factsOf(d *planDiff) []string {
	var out []string
	for _, f := range d.unreadFacts {
		out = append(out, f.address+"|"+f.what)
	}
	slices.Sort(out)
	return out
}

// unread adds the line a plan prints for a live fact it could not read, with no
// mark, and records the fact with the address, kind and error it was given.
func TestPlanDiff_UnreadAddsABlankLineAndRecordsTheFact(t *testing.T) {
	d := &planDiff{}
	why := errors.New("the platform did not answer")
	d.unread("site /", "sites", why)

	want := planLine{" ", "site /", "live sites unknown (the platform did not answer)"}
	if len(d.lines) != 1 || d.lines[0] != want {
		t.Errorf("the plan's lines are %v, want [%v]", d.lines, want)
	}
	if d.changed {
		t.Error("a fact the plan could not read set a mark: it is no difference")
	}
	if len(d.unreadFacts) != 1 || d.unreadFacts[0] != (unreadFact{"site /", "sites", why}) {
		t.Errorf("the recorded facts are %v, want the site /, sites and the error it was given", d.unreadFacts)
	}
}

// noteUnread records a fact for a line that says so in its own words, or for
// none, and adds no line.
func TestPlanDiff_NoteUnreadRecordsTheFactAndAddsNoLine(t *testing.T) {
	d := &planDiff{}
	why := errors.New("the platform did not answer")
	d.noteUnread("secrets", "secrets", why)

	if len(d.lines) != 0 {
		t.Errorf("noting an unread fact added the lines %v", d.lines)
	}
	if len(d.unreadFacts) != 1 || d.unreadFacts[0] != (unreadFact{"secrets", "secrets", why}) {
		t.Errorf("the recorded facts are %v, want secrets, secrets and the error it was given", d.unreadFacts)
	}
}

// The control: a line that is not an unread fact records nothing, so the record
// is not every blank-marked line.
func TestPlanDiff_ABlankLineThatIsNotUnreadRecordsNothing(t *testing.T) {
	d := &planDiff{}
	d.add(" ", "secret API_KEY", "present; set again from $API_KEY on apply (write-only, not compared)")
	if len(d.unreadFacts) != 0 {
		t.Errorf("a write-only line recorded %v", d.unreadFacts)
	}
}

// addEverythingTheStubDoesNotAnswer adds to the fxevm file the edge blocks, an
// alert and the client channels, none of which the stub answers, so each read of
// them fails.
func addEverythingTheStubDoesNotAnswer(t *testing.T, s string) string {
	t.Helper()
	s = mustReplace(t, s, "edge {\n  site_auth {",
		"edge {\n  egress {\n    mode  = \"allowlist\"\n    hosts = [\"api.example.com\"]\n  }\n"+
			"  links = [\"billing\"]\n  allow = [\"203.0.113.5/32\"]\n  hosts = { \"admin.example.com\" = \"admin\" }\n"+
			"  domain \"shop.example.com\" {}\n  site_auth {")
	s = mustReplace(t, s, "      memory   = \"32MiB\"\n    }\n  }\n}\n\nbackbone {",
		"      memory   = \"32MiB\"\n      alert \"bench-errors\" {\n        threshold = 3\n        window    = \"10m\"\n"+
			"        webhook   = env(\"BENCH_HOOK\")\n      }\n    }\n  }\n}\n\nbackbone {\n  realtime {\n    client_channels = [\"chat\"]\n  }\n")
	return s
}

// Every fact the plan could not read is recorded, once, with the address of the
// line it belongs to, what it is, and why it could not be read. The facts are
// exactly these: a plan that records more or fewer says something false about
// what it read.
func TestPlanDiff_EveryFactThePlanCouldNotReadIsRecorded(t *testing.T) {
	slots := []string{"function GET /bench|code", "function POST /admin/reset|code", "function POST /rpc|code", "worker sequencer|code"}
	for _, c := range []struct {
		name    string
		failing []string
		edit    func(*testing.T, string) string
		want    []string
		// reason is a fragment every recorded error carries.
		reason string
	}{
		{name: "the live sites", failing: []string{"/ops/canvas"}, want: []string{"site /|sites"}, reason: "list canvas sites"},
		// The file declares no secret, and the slice's list is how a secret the file
		// does not name is found: the fact is recorded with no line to hang on.
		{name: "the live secrets", failing: []string{"/ops/backbone/secret/list"}, want: []string{"secrets|secrets"}, reason: "list secrets"},
		{name: "the live keys", failing: []string{"/ops/atomic/auth"}, want: []string{"function POST /admin/reset key|key"}, reason: "list API keys"},
		{name: "the site gate", failing: []string{"/ops/slice/auth"}, want: []string{"site_auth|gate"}, reason: "read the site gate"},
		{name: "the deployed functions", failing: []string{"/ops/atomic/list"}, want: slots, reason: "list atomic functions"},
		{
			name: "the build",
			edit: func(t *testing.T, s string) string {
				return mustReplace(t, s, `handler  = "GetBench"`, `handler  = "GetNothing"`)
			},
			want: slots, reason: "GetNothing",
		},
		{
			// The line is a removal, marked, and the fact is recorded all the same.
			name:    "the deployed functions, for a slot the file no longer declares",
			failing: []string{"/ops/atomic/list"},
			edit: func(t *testing.T, s string) string {
				const bench = "  backend \"bench\" {\n    dir = \"atomic/bench\"\n\n    function \"GET /bench\" {\n" +
					"      handler  = \"GetBench\"\n      response = \"json\"\n      memory   = \"32MiB\"\n    }\n  }\n"
				return mustReplace(t, s, bench, "")
			},
			want:   slots,
			reason: "list atomic functions",
		},
		{
			// The line is a change, marked, and the code half of it is still unread.
			name:    "the deployed functions, for a slot whose booking changes",
			failing: []string{"/ops/atomic/list"},
			edit: func(t *testing.T, s string) string {
				return mustReplace(t, s, "concurrent = true\n      memory     = \"64MiB\"", "concurrent = true\n      memory     = \"128MiB\"")
			},
			want:   slots,
			reason: "list atomic functions",
		},
		{
			name: "the edge, the alerts and the client channels",
			edit: addEverythingTheStubDoesNotAnswer,
			want: []string{
				"alert bench-errors|alerts", "egress|egress", "link|link", "allow|allow", "host|host", "domain|domain", "realtime|client channels",
			},
			reason: "wasn't found",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			live, deployed := matchingSlice(t)
			planStub(t, live, deployed, c.failing...)
			t.Setenv("BENCH_HOOK", "https://hooks.example.com/x")
			var edit func(string) string
			if c.edit != nil {
				edit = func(s string) string { return c.edit(t, s) }
			}
			d := diffIn(t, edit)

			want := slices.Clone(c.want)
			slices.Sort(want)
			if got := factsOf(d); !slices.Equal(got, want) {
				t.Errorf("the plan recorded the unread facts %q, want %q", got, want)
			}
			for _, f := range d.unreadFacts {
				if f.err == nil {
					t.Errorf("%s|%s was recorded with no error saying why it was not read", f.address, f.what)
				} else if !strings.Contains(f.err.Error(), c.reason) {
					t.Errorf("%s|%s was recorded for %q, which does not say %q", f.address, f.what, f.err, c.reason)
				}
			}
		})
	}
}

// The controls. A plan that read everything the file asked of the slice records
// nothing, though it holds lines that say a value is not compared: a secret, a
// key and a gate password are write-only, and a write-only value is not unread.
// The stub does not answer the alerts or the client channels and the file names
// neither, so nothing in the file asks for them.
func TestPlanDiff_APlanThatReadWhatItWasAskedToReadRecordsNothing(t *testing.T) {
	live, deployed := matchingSlice(t)
	planStub(t, live, deployed)
	d := diffIn(t, nil)

	if facts := factsOf(d); len(facts) != 0 {
		t.Errorf("the plan read everything and recorded %q", facts)
	}
	writeOnly := 0
	for _, l := range d.lines {
		if strings.Contains(l.detail, "write-only, not compared") {
			writeOnly++
		}
	}
	if writeOnly == 0 {
		t.Error("the plan has no write-only line, so this proves nothing about write-only values")
	}
}

// A slice that does not exist is read nowhere, so nothing in it is unread.
func TestPlanDiff_ASliceThatWouldBeCreatedRecordsNothing(t *testing.T) {
	planStub(t, "", `[]`)
	if facts := factsOf(diffIn(t, nil)); len(facts) != 0 {
		t.Errorf("a slice to create recorded %q", facts)
	}
}

// A price the platform could not quote is the price line's to say, and is not a
// fact of the slice the plan failed to read.
func TestPlanDiff_AnUnquotablePriceIsNotAnUnreadFact(t *testing.T) {
	live, deployed := matchingSlice(t)
	planStub(t, live, deployed, "/ops/slice/price")
	if facts := factsOf(diffIn(t, nil)); len(facts) != 0 {
		t.Errorf("a price that could not be quoted recorded %q", facts)
	}
}
