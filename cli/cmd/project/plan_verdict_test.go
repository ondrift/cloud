package project

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// What a plan concludes is one of four things: the slice would be created, the
// slice differs from the file, part of the slice could not be read, or the slice
// is the file. A plan that could not read a fact must not claim the last: "nothing
// to change" is a statement about everything, and a read that failed is not
// everything.

// matchingSlice is the live slice that is exactly the fxevm file, and what that
// slice reports deployed: the body of GET /ops/slice?name= and of GET /ops/atomic.
func matchingSlice(t *testing.T) (live, deployed string) {
	t.Helper()
	withRealSchema(t)
	c, err := CompileDriftfile(filepath.Join(mustAbs(t, "testdata"), "fxevm", "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	liveBody, err := json.Marshal(map[string]any{"name": "fxevm-testnode", "tier": "custom", "config": c.Shape})
	if err != nil {
		t.Fatal(err)
	}

	els, err := atomic_cmd.BuildBackends(AtomicSpecs(c.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	atomic_cmd.SetDeclaredSchedules(declaredSchedules(c.Manifest))
	var list []map[string]string
	for _, el := range els {
		digest, err := atomic_cmd.BackendDigest(el.Dir, el.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range el.Funcs {
			method, name := f.Spec.Wire()
			list = append(list, map[string]string{
				"function_name": name, "method": method, "digest": atomic_cmd.DeployDigest(digest, f.Spec),
			})
		}
	}
	listBody, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(liveBody), string(listBody)
}

// mustReplace is strings.Replace that fails the test when there is nothing to
// replace, so an edit to the fixture cannot quietly do nothing.
func mustReplace(t *testing.T, s, old, replacement string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("the fixture no longer holds %q, which this test edits:\n%s", old, s)
	}
	return strings.Replace(s, old, replacement, 1)
}

// unreadCase is one fact a plan can fail to read, on a slice that is otherwise
// the file.
type unreadCase struct {
	name string
	// failing is the path the platform answers 500, or "" when edit breaks the
	// project the build reads instead.
	failing string
	edit    func(t *testing.T, driftfile string) string
	// fact is what the plan names as unread, and reason is the cause it gives.
	fact, reason string
}

// stub answers as the platform of the matching slice, with this fact unreadable.
func (c unreadCase) stub(t *testing.T) {
	t.Helper()
	live, deployed := matchingSlice(t)
	var failing []string
	if c.failing != "" {
		failing = append(failing, c.failing)
	}
	planStub(t, live, deployed, failing...)
}

// editor is the edit of the fixture that makes this fact unreadable, or nil.
func (c unreadCase) editor(t *testing.T) func(string) string {
	if c.edit == nil {
		return nil
	}
	return func(s string) string { return c.edit(t, s) }
}

// plan runs `drift plan` on the matching slice with this fact unreadable.
func (c unreadCase) plan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c.stub(t)
	return planIn(t, c.editor(t), args...)
}

// unreadCases are the facts a plan reads that the fxevm file makes it read, one
// per way the plan is blind.
func unreadCases() []unreadCase {
	return []unreadCase{
		{name: "the live sites", failing: "/ops/canvas", fact: "sites", reason: "list canvas sites"},
		// The file declares no secret, and the slice's list is how a secret the
		// file does not name is found, so a plan that cannot read it cannot say
		// that no secret would be deleted.
		{name: "the live secrets", failing: "/ops/backbone/secret/list", fact: "secrets", reason: "list secrets"},
		{name: "the live keys", failing: "/ops/atomic/auth", fact: "key", reason: "list API keys"},
		{name: "the deployed functions", failing: "/ops/atomic", fact: "code", reason: "list atomic functions"},
		{
			name: "the build",
			edit: func(t *testing.T, s string) string {
				return mustReplace(t, s, `handler  = "GetBench"`, `handler  = "GetNothing"`)
			},
			fact: "build", reason: "GetNothing",
		},
	}
}

// headerOf is what the plan's slice line says about the slice: the text after
// its name, which is padded to 50 columns.
func headerOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  slice ") && len(line) > 53 {
			return strings.TrimSpace(line[53:])
		}
	}
	t.Fatalf("the plan has no slice line:\n%s", out)
	return ""
}

const (
	closingMatch  = "Nothing to change."
	closingUnread = "Nothing differs in what could be read."
	closingApply  = "Apply with:"
)

// A plan that could not read a fact does not say the slice has nothing to
// change. It says nothing differs in what it could read, names each fact it
// could not and why, and its header says the slice was not fully read.
func TestPlan_ABlindPlanDoesNotClaimAMatch(t *testing.T) {
	for _, c := range unreadCases() {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.plan(t)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if strings.Contains(out, closingMatch) {
				t.Errorf("the plan could not read %s and still says %q:\n%s", c.name, closingMatch, out)
			}
			if got := headerOf(t, out); got != "unchanged (not fully read)" {
				t.Errorf("the plan could not read %s and its header says %q, not %q", c.name, got, "unchanged (not fully read)")
			}
			_, unread, found := strings.Cut(out, closingUnread)
			if !found {
				t.Fatalf("the plan could not read %s and does not say %q:\n%s", c.name, closingUnread, out)
			}
			for _, want := range []string{c.fact, c.reason} {
				if !strings.Contains(unread, want) {
					t.Errorf("after %q the plan does not name %q, which says what could not be read about %s:\n%s",
						closingUnread, want, c.name, unread)
				}
			}
		})
	}
}

// verdictCase is one of the four things a plan concludes, with the slice that
// makes it conclude that.
type verdictCase struct {
	name    string // create, differs, unread or match
	verdict VerdictState
	// stub answers as the platform of that slice.
	stub func(t *testing.T)
	// header is the plan's slice line status; closing the line it ends on;
	// notClosing the lines of the other verdicts it must not end on.
	header, closing string
	notClosing      []string
	// exit is the status `drift plan --exit-code` ends with.
	exit int
}

// run runs `drift plan` on that slice.
func (c verdictCase) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c.stub(t)
	return planIn(t, nil, args...)
}

// state gathers the plan of that slice.
func (c verdictCase) state(t *testing.T) *planState {
	t.Helper()
	c.stub(t)
	return planStateIn(t, nil)
}

func verdictCases() []verdictCase {
	return []verdictCase{
		{
			name: "create", verdict: VerdictCreate,
			stub:   func(t *testing.T) { planStub(t, "", `[]`) },
			header: "+ create", closing: closingApply, notClosing: []string{closingMatch, closingUnread}, exit: 2,
		},
		{
			name: "differs", verdict: VerdictDiffers,
			stub:   func(t *testing.T) { planStub(t, fxevmLive, `[]`) },
			header: "~ change", closing: closingApply, notClosing: []string{closingMatch, closingUnread}, exit: 2,
		},
		{
			// The slice's sites are the unread fact, the first of the cases above.
			name: "unread", verdict: VerdictUnread,
			stub:   unreadCases()[0].stub,
			header: "unchanged (not fully read)", closing: closingUnread, notClosing: []string{closingMatch}, exit: 3,
		},
		{
			// The write-only lines of the fxevm file (a key and a gate password the
			// slice never reads back) are in this plan, and it is still a match.
			name: "match", verdict: VerdictMatch,
			stub: func(t *testing.T) {
				live, deployed := matchingSlice(t)
				planStub(t, live, deployed)
			},
			header: "unchanged", closing: closingMatch, notClosing: []string{closingUnread, closingApply}, exit: 0,
		},
	}
}

// Each verdict reads differently at the top of the plan and at its end, so a
// person and a script reading the text can tell the four apart.
func TestPlan_EachVerdictHasItsOwnHeaderAndClosingLine(t *testing.T) {
	for _, c := range verdictCases() {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.run(t)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if got := headerOf(t, out); got != c.header {
				t.Errorf("a plan that is %q has the header %q, want %q:\n%s", c.name, got, c.header, out)
			}
			if !strings.Contains(out, c.closing) {
				t.Errorf("a plan that is %q does not end on %q:\n%s", c.name, c.closing, out)
			}
			for _, not := range c.notClosing {
				if strings.Contains(out, not) {
					t.Errorf("a plan that is %q says %q:\n%s", c.name, not, out)
				}
			}
			if c.name == "match" && !strings.Contains(out, "write-only, not compared") {
				t.Errorf("the match has no write-only line, so it does not show a write-only value is not unread:\n%s", out)
			}
		})
	}
}

// exitStatusOf is the status the process exits with for the error a command
// returned, the function main exits with.
func exitStatusOf(err error) int { return common.ExitStatus(err) }

// With --exit-code a script reads the verdict from the status: 0 when the slice
// is the file, 2 when it would be created or differs, 3 when part of it could
// not be read, 1 when the plan fails.
func TestPlan_ExitCodeReportsTheVerdictAsAnExitStatus(t *testing.T) {
	for _, c := range verdictCases() {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.run(t, "--exit-code")
			if got := exitStatusOf(err); got != c.exit {
				t.Errorf("drift plan --exit-code on a slice that is %q exits %d, want %d (error: %v)", c.name, got, c.exit, err)
			}
		})
	}
}

// A plan that fails exits 1 with the failure's own error: the flag does not turn
// a failure into a verdict. The slice's own read is what fails here, so the plan
// has nothing to conclude.
func TestPlan_ExitCodeLeavesAFailureAsExitStatusOne(t *testing.T) {
	planStub(t, fxevmLive, `[]`, "/ops/slice")
	for _, c := range []struct {
		name string
		args []string
	}{
		{name: "without the flag"},
		{name: "with the flag", args: []string{"--exit-code"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := planIn(t, nil, c.args...)
			if err == nil || !strings.Contains(err.Error(), "fetch slice") {
				t.Fatalf("a plan that cannot read the slice does not fail with the read's own error: %v", err)
			}
			if got := exitStatusOf(err); got != 1 {
				t.Errorf("a plan that fails exits %d, want 1", got)
			}
		})
	}
}

// Without the flag the plan exits 0 whatever it finds, and 1 only when it fails,
// so a script that runs `drift plan` for its output is not failed by a
// difference.
func TestPlan_WithoutExitCodeADifferenceExitsZero(t *testing.T) {
	for _, c := range verdictCases() {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.run(t)
			if got := exitStatusOf(err); got != 0 {
				t.Errorf("drift plan on a slice that is %q exits %d without --exit-code, want 0 (error: %v)", c.name, got, err)
			}
		})
	}
}

// The flag is in the plan's help, which is where a script's author looks for
// what each status means.
func TestPlan_ExitCodeIsInThePlanHelp(t *testing.T) {
	flag := GetPlanCmd().Flags().Lookup("exit-code")
	if flag == nil {
		t.Fatal("drift plan has no --exit-code")
	}
	if strings.TrimSpace(flag.Usage) == "" {
		t.Error("--exit-code says nothing in the plan's help")
	}
}
