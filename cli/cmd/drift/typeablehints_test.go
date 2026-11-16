package main

import (
	"strings"
	"testing"

	account "github.com/ondrift/cloud/cli/cmd/account"
	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
	"github.com/ondrift/cloud/cli/cmd/doctor"
	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// Anything the CLI tells someone to type has to be typeable.
//
// Help text is already held to that: an Example is walked for flags that exist,
// and an error remedy is resolved against the command tree. Neither check can
// see the text below, because it is printed at runtime from inside a RunE
// closure and a printed string is not a call. So the strings are declared where
// the tree can be asked about them, and this is the asking.
//
// The registry is the post-signup banner: the literal first commands a new
// account is told to type, where a wrong line is the first thing the product
// ever does to someone. It grows as other runtime-printed suggestions are found.
var typeableHints = map[string][]string{
	"the post-signup banner (drift account create)": account.FirstRunHints,
	// The first failure a new install hits, and the most-reached error in the
	// CLI. It told people to run `drift account signup`, which has never been a
	// command — caught by the flow suite (IDN-104), not by this test, because
	// the text was inlined where nothing could ask about it.
	"the not-logged-in error (DRIFT-1011)": common.NotLoggedInHints,
	// Every reader answers a YAML Driftfile with this refusal, and it names
	// what to run instead.
	"the refusal of a YAML Driftfile": {yamlRefusal()},
	// Printed on stderr by the request helper the first time the platform states a
	// route contract ahead of this binary's.
	"the older-than-the-platform line": {common.NewerPlatformNotice(common.ImplementedRouteContract + 1)},
	// A bare 404 from a platform ahead of this binary (DRIFT-1015).
	"the not-found from a newer platform (DRIFT-1015)": {(&common.APIError{
		Op:                 "delete function",
		Status:             404,
		PlatformAPIVersion: common.ImplementedRouteContract + 1,
	}).Error()},
	// `drift doctor explain` for a code numbered above every registered one, which
	// is what a newer drift prints. The number is one no registry reaches.
	"the newer-code answer of drift doctor explain": {doctor.NotRegistered("DRIFT-99999999").Error()},
	// A refresh the platform rejected (DRIFT-1001), a session it ended on purpose
	// (DRIFT-1018) and a token it refused (DRIFT-1019) each name what to run.
	"the rejected-refresh sentence (DRIFT-1001)": {common.SessionExpiredMessage},
	"the revoked-session sentence (DRIFT-1018)":  {common.SessionRevokedMessage},
	"the refused-token sentence (DRIFT-1019)":    {common.TokenRefusedMessage},
	// What a machine with no copy of the Driftfile format is told to run (DRIFT-1008).
	"the no-schema refusal (DRIFT-1008)": {project.ErrNoSchema.Error()},
	// What a compiled function booked under the floor is told to run (DRIFT-1010).
	"the compiled-floor refusal (DRIFT-1010)": {project.FloorRefusal("16MB", []string{"  get:ping booked 8MiB, in go"}).Error()},
}

// yamlRefusal is the message every reader gives a YAML Driftfile.
func yamlRefusal() string {
	err := project.RefuseYAML("Driftfile", []byte("slice: demo\n"))
	if err == nil {
		return "the YAML refusal is empty"
	}
	return err.Error()
}

func TestPrintedHintsNameRealCommands(t *testing.T) {
	root := newRootCmd("test")

	var checked int
	for site, lines := range typeableHints {
		for _, line := range lines {
			for _, invocation := range quotedInvocations(line) {
				checked++
				if problem := unrunnable(root, invocation); problem != "" {
					t.Errorf("%s tells the user to run %q, which %s:\n  %s",
						site, invocation, problem, strings.TrimSpace(line))
				}
			}
		}
	}

	// Without this, an extractor that finds nothing passes every line.
	if checked == 0 {
		t.Error("no invocation was extracted from any hint, so this test checked nothing")
	}
}

// The control for the resolver. Each of these either names nothing or stops at
// a group that runs nothing, and the first is the exact shape a banner line
// takes when it names a verb that was never a top-level command. If the
// resolver calls any of them runnable it cannot detect the case it exists for.
func TestTheHintResolverRejectsACommandThatDoesNotExist(t *testing.T) {
	root := newRootCmd("test")

	for _, bogus := range []string{
		"drift deploy drift.yaml",
		"drift nosuchgroup",
		"drift slice nosuchverb",
		"drift slice",
	} {
		if unrunnable(root, bogus) == "" {
			t.Errorf("%q was judged runnable, so the check above cannot detect a hint "+
				"that names nothing", bogus)
		}
	}
}

// A survey that offers a value its own validation rejects is a dead end reached
// by using the command exactly as designed: the menu is the CLI's own
// suggestion, and the step after it, inside the same invocation, refuses the
// answer with no way back but re-running the wizard.
//
// Survey options are a plain slice rather than something the command tree
// exposes, so the call sites are named here by hand. The registry grows as
// surveys are added.
func TestSurveyOptionsAreAcceptedByTheirOwnValidation(t *testing.T) {
	surveys := []struct {
		site    string
		options []string
		accept  func(string) error
	}{
		{"drift atomic new, the Auth: prompt", atomic_cmd_new.AuthOptions, atomic_cmd_new.ValidateAuth},
	}

	for _, s := range surveys {
		if len(s.options) == 0 {
			t.Errorf("%s offers nothing, so it cannot be checked", s.site)
			continue
		}
		for _, opt := range s.options {
			if err := s.accept(opt); err != nil {
				t.Errorf("%s offers %q, and its own validation then refuses it: %v",
					s.site, opt, err)
			}
		}
		// The control, per site: the validation has to refuse something, or
		// every option passes for a reason that has nothing to do with the menu.
		if s.accept("definitely-not-an-option") == nil {
			t.Errorf("%s's validation accepts anything, so the check above proves nothing", s.site)
		}
	}
}

// quotedInvocations returns the quoted `drift …` commands in a printed line.
// The hints quote what to type precisely so it can be lifted back out.
//
// BOTH delimiters, because the CLI uses both: banners quote with ' and error
// text with backticks. Checking only one is how an unrunnable suggestion sat in
// the not-logged-in error while this test passed.
//
// Each delimiter is scanned independently, and a span only counts when it
// begins with the binary's name. That guard is load-bearing for the apostrophe:
// "you're not logged in…" splits on ' into spans that are ordinary prose, and
// none of them starts with "drift ".
func quotedInvocations(line string) []string {
	var out []string
	for _, delim := range []string{"'", "`"} {
		parts := strings.Split(line, delim)
		// Quoted spans are the odd-indexed parts: a'B'c'D'e.
		for i := 1; i < len(parts); i += 2 {
			if strings.HasPrefix(parts[i], "drift ") || parts[i] == "drift" {
				out = append(out, parts[i])
			}
		}
	}
	return out
}

// unrunnable reports why an invocation cannot be typed, or "" when it names a
// real, executable command.
//
// It descends the tree word by word rather than calling cobra's Find, which
// returns the deepest command it matched and so answers "drift" for
// `drift deploy …` without complaint. The first word after the binary name has
// to name a subcommand; once a word no longer does, the rest is the arguments
// the user supplies, and what matters then is whether the command reached can
// actually run. `drift slice` is a group with no verb, so `drift slice
// nosuchverb` is a hint that prints a usage block, not a hint that works.
func unrunnable(root *cobra.Command, invocation string) string {
	words := strings.Fields(invocation)
	if len(words) == 0 || words[0] != root.Name() {
		return "does not start with `" + root.Name() + "`"
	}

	cur := root
	for i, w := range words[1:] {
		// A placeholder or a flag ends the command path: `drift slice info
		// <name>` is `slice info` plus what the user fills in.
		if strings.HasPrefix(w, "<") || strings.HasPrefix(w, "[") || strings.HasPrefix(w, "-") {
			break
		}
		child := subcommand(cur, w)
		if child == nil {
			if i == 0 {
				return "names no `" + root.Name() + "` command"
			}
			break // an argument to cur, not a command
		}
		cur = child
	}

	// A group's own run function, the one refuseUnknownVerbs installs, prints
	// help or refuses a verb: it is not a command a hint can send someone to.
	if !cur.Runnable() || cur.Annotations[unknownVerbAnnotation] != "" {
		return "stops at `" + cur.CommandPath() + "`, which is a group and runs nothing"
	}
	return ""
}

// subcommand returns the child of cmd that name addresses, including by alias,
// or nil.
func subcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}
