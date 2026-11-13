package project

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// What a plan concludes is one of four things: the slice would be created, the
// slice differs from the file, part of the slice could not be read, or the slice
// is the file. A plan that could not read a fact must not claim the last: "nothing
// to change" is a statement about everything, and a read that failed is not
// everything.

// matchingSlice is the live slice that is exactly the fxevm file, and what that
// slice reports deployed: the body of /ops/slice/get and of /ops/atomic/list.
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

// plan runs `drift plan` on the matching slice with this fact unreadable.
func (c unreadCase) plan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	live, deployed := matchingSlice(t)
	var failing []string
	if c.failing != "" {
		failing = append(failing, c.failing)
	}
	planStub(t, live, deployed, failing...)
	var edit func(string) string
	if c.edit != nil {
		edit = func(s string) string { return c.edit(t, s) }
	}
	return planIn(t, edit, args...)
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
		{name: "the deployed functions", failing: "/ops/atomic/list", fact: "code", reason: "list atomic functions"},
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
// its name.
func headerOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if _, status, ok := strings.Cut(line, "slice fxevm-testnode"); ok {
			return strings.TrimSpace(status)
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

// Why a test below is skipped, and which task makes it pass.
const (
	skipUntilTheVerdictDecidesTheText = "fails until #3024: the header and the closing line read only the marked lines, " +
		"so a plan that could not read a fact says it has nothing to change"
	skipUntilTheFlagExists = "fails until #3026: drift plan has no --exit-code flag"
)

// A plan that could not read a fact does not say the slice has nothing to
// change. It says nothing differs in what it could read, names each fact it
// could not and why, and its header says the slice was not fully read.
func TestPlan_ABlindPlanDoesNotClaimAMatch(t *testing.T) {
	t.Skip(skipUntilTheVerdictDecidesTheText)
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
	name string // create, differs, unread or match
	run  func(t *testing.T, args ...string) (string, error)
	// header is the plan's slice line status; closing the line it ends on;
	// notClosing the lines of the other verdicts it must not end on.
	header, closing string
	notClosing      []string
	// exit is the status `drift plan --exit-code` ends with.
	exit int
	// skip is why the case cannot pass yet, and which task makes it pass.
	skip string
}

func verdictCases() []verdictCase {
	return []verdictCase{
		{
			name: "create",
			run: func(t *testing.T, args ...string) (string, error) {
				planStub(t, "", `[]`)
				return planIn(t, nil, args...)
			},
			header: "+ create", closing: closingApply, notClosing: []string{closingMatch, closingUnread}, exit: 2,
		},
		{
			name: "differs",
			run: func(t *testing.T, args ...string) (string, error) {
				planStub(t, fxevmLive, `[]`)
				return planIn(t, nil, args...)
			},
			header: "~ change", closing: closingApply, notClosing: []string{closingMatch, closingUnread}, exit: 2,
		},
		{
			// The slice's sites are the unread fact, the first of the cases above.
			name: "unread",
			run: func(t *testing.T, args ...string) (string, error) {
				return unreadCases()[0].plan(t, args...)
			},
			header: "unchanged (not fully read)", closing: closingUnread, notClosing: []string{closingMatch}, exit: 3,
			skip: skipUntilTheVerdictDecidesTheText,
		},
		{
			// The write-only lines of the fxevm file (a key and a gate password the
			// slice never reads back) are in this plan, and it is still a match.
			name: "match",
			run: func(t *testing.T, args ...string) (string, error) {
				live, deployed := matchingSlice(t)
				planStub(t, live, deployed)
				return planIn(t, nil, args...)
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
			if c.skip != "" {
				t.Skip(c.skip)
			}
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
// returned: the one the error carries as ExitCode() int, as os/exec's does; 1
// for any other error; 0 for none.
func exitStatusOf(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

// With --exit-code a script reads the verdict from the status: 0 when the slice
// is the file, 2 when it would be created or differs, 3 when part of it could
// not be read, 1 when the plan fails.
func TestPlan_ExitCodeReportsTheVerdictAsAnExitStatus(t *testing.T) {
	t.Skip(skipUntilTheFlagExists)
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
	planStub(t, fxevmLive, `[]`, "/ops/slice/get")
	for _, c := range []struct {
		name string
		args []string
		skip string
	}{
		{name: "without the flag"},
		{name: "with the flag", args: []string{"--exit-code"}, skip: skipUntilTheFlagExists},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
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
	t.Skip(skipUntilTheFlagExists)
	flag := GetPlanCmd().Flags().Lookup("exit-code")
	if flag == nil {
		t.Fatal("drift plan has no --exit-code")
	}
	if strings.TrimSpace(flag.Usage) == "" {
		t.Error("--exit-code says nothing in the plan's help")
	}
}
