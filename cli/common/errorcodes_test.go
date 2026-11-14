package common

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE test the registry exists for.
//
// A code is minted once and never reused, renumbered or deleted, because a code
// pasted from a chat log a year old must still resolve to the same failure. That
// rule cannot be enforced by remembering it, so it is enforced by DENSITY: the
// numbers run 1..N with no gaps, which means removing an entry fails here, and
// reuse is impossible without first removing.
//
// To stop producing a code, set `retired: true` and leave it in place.
func TestErrorCodes_TheSequenceIsDenseSoNoneCanBeDeletedOrReused(t *testing.T) {
	codes := ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so every failure is anonymous again")
	}

	seen := map[int]string{}
	for _, c := range codes {
		n := errorCodeNumber(c.Code)
		if n < 0 {
			t.Errorf("%q is not shaped like a code (want DRIFT-<number>)", c.Code)
			continue
		}
		if prev, dup := seen[n]; dup {
			t.Errorf("DRIFT-%d is registered twice (%q and %q) — a reused code resolves to "+
				"the wrong failure for everyone holding the old one", n, prev, c.Code)
		}
		seen[n] = c.Code
	}

	// Dense from the first code to the last. A gap means an entry was removed,
	// and a removed entry is a number free to be minted again.
	lo, hi := 1<<30, 0
	for n := range seen {
		if n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	for n := lo; n <= hi; n++ {
		if _, ok := seen[n]; !ok {
			t.Errorf("DRIFT-%d is missing from the sequence — a code is never deleted, "+
				"only retired, or the number becomes free to reuse", n)
		}
	}
}

// A code with no remedy is a name for a problem and nothing else, which is the
// state this registry was built to leave.
func TestErrorCodes_EveryEntrySaysWhatHappenedAndWhatToDo(t *testing.T) {
	for _, c := range ErrorCodes() {
		if strings.TrimSpace(c.Meaning) == "" {
			t.Errorf("%s has no meaning", c.Code)
		}
		if strings.TrimSpace(c.Remedy) == "" {
			t.Errorf("%s has no remedy — a code that only names the failure leaves the "+
				"user exactly where they were", c.Code)
		}
	}
}

// keyShape is what a key looks like: lowercase words joined by single hyphens,
// "session-expired".
var keyShape = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// keyProblems reports every way the entries break the key rule: each carries a
// key in the shape of one, and no two share it. A problem names the code it is
// about.
func keyProblems(entries []ErrorCode) []string {
	var problems []string
	owners := map[string][]string{}
	for _, e := range entries {
		switch {
		case strings.TrimSpace(e.Key) == "":
			problems = append(problems, fmt.Sprintf("%s has no key", e.Code))
		case !keyShape.MatchString(e.Key):
			problems = append(problems, fmt.Sprintf(
				"%s has the key %q, which is not lowercase words joined by hyphens", e.Code, e.Key))
		default:
			owners[e.Key] = append(owners[e.Key], e.Code)
		}
	}

	keys := make([]string, 0, len(owners))
	for key := range owners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if codes := owners[key]; len(codes) > 1 {
			problems = append(problems, fmt.Sprintf(
				"%s share the key %q, but a key names one failure", strings.Join(codes, " and "), key))
		}
	}
	return problems
}

// Every entry carries a key naming the failure its code stands for, and no two
// entries share one. The key is what a pull request's registry is held to
// against the base branch's.
func TestErrorCodes_EveryEntryHasAUniqueKey(t *testing.T) {
	codes := ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so there is nothing to hold a key")
	}
	for _, problem := range keyProblems(codes) {
		t.Error(problem)
	}
}

// The control for the test above: a registry that breaks the rule in each way is
// reported with the code named, and one that keeps it is not. Without this the
// registry test would pass for a check that reports nothing.
func TestKeyProblems_NamesTheCodeThatBreaksTheRule(t *testing.T) {
	entry := func(code, key string) ErrorCode { return ErrorCode{Code: code, Key: key} }

	for _, tc := range []struct {
		name     string
		entries  []ErrorCode
		problems int
		mention  []string
	}{
		{"distinct keys",
			[]ErrorCode{entry("DRIFT-1001", "session-expired"), entry("DRIFT-1002", "forbidden")},
			0, nil},
		{"a digit in a key",
			[]ErrorCode{entry("DRIFT-1001", "tls-1-expired")},
			0, nil},
		{"an entry with no key",
			[]ErrorCode{entry("DRIFT-1001", "forbidden"), entry("DRIFT-1002", "")},
			1, []string{"DRIFT-1002"}},
		{"a key of spaces",
			[]ErrorCode{entry("DRIFT-1001", "   ")},
			1, []string{"DRIFT-1001"}},
		{"two entries share a key",
			[]ErrorCode{entry("DRIFT-1001", "quota"), entry("DRIFT-1002", "forbidden"), entry("DRIFT-1003", "quota")},
			1, []string{"DRIFT-1001", "DRIFT-1003", `"quota"`}},
		{"three entries share a key",
			[]ErrorCode{entry("DRIFT-1001", "quota"), entry("DRIFT-1002", "quota"), entry("DRIFT-1003", "quota")},
			1, []string{"DRIFT-1001", "DRIFT-1002", "DRIFT-1003"}},
		{"two keys are each shared",
			[]ErrorCode{entry("DRIFT-1001", "a"), entry("DRIFT-1002", "a"), entry("DRIFT-1003", "b"), entry("DRIFT-1004", "b")},
			2, []string{`"a"`, `"b"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keyProblems(tc.entries)
			if len(got) != tc.problems {
				t.Fatalf("%d problems %q, want %d", len(got), got, tc.problems)
			}
			all := strings.Join(got, "\n")
			for _, want := range tc.mention {
				if !strings.Contains(all, want) {
					t.Errorf("the problems do not mention %s: %q", want, got)
				}
			}
		})
	}

	// A key is a name, so the shape is held too: each of these is one problem
	// naming the entry, and none of them would be a stable name to type.
	for _, key := range []string{"Quota", "not found", "not_found", "-quota", "quota-", "not--found", "1st-failure"} {
		got := keyProblems([]ErrorCode{entry("DRIFT-1001", key)})
		if len(got) != 1 || !strings.Contains(got[0], "DRIFT-1001") {
			t.Errorf("the key %q gave %q, want one problem naming DRIFT-1001", key, got)
		}
	}
}

// appendOnlyRegistry is a small registry in the shape the real one has.
func appendOnlyRegistry() []ErrorCode {
	return []ErrorCode{
		{Code: "DRIFT-1001", Key: "session-expired", Meaning: "Your session has expired.", Remedy: "Log in again.", Command: "drift account login"},
		{Code: "DRIFT-1002", Key: "forbidden", Meaning: "The platform refused the action.", Remedy: "Check which account this is.", Command: "drift slice list"},
		{Code: "DRIFT-1003", Key: "not-found", Meaning: "The thing you named does not exist.", Remedy: "Check the spelling.", Command: "drift slice list"},
		{Code: "DRIFT-1004", Key: "quota", Meaning: "You have hit a quota.", Remedy: "Free a slot.", Command: "drift slice resize"},
	}
}

// swapContents gives each entry the other's key, meaning, remedy and command and
// leaves each its own code. The registry stays dense and every entry stays well
// formed, which is why no check that reads only one registry can see it.
func swapContents(a, b ErrorCode) (ErrorCode, ErrorCode) {
	a.Key, b.Key = b.Key, a.Key
	a.Meaning, b.Meaning = b.Meaning, a.Meaning
	a.Remedy, b.Remedy = b.Remedy, a.Remedy
	a.Command, b.Command = b.Command, a.Command
	return a, b
}

// checkAppendOnly reports every way head breaks the append-only rule against the
// base it grew from, each violation naming its code. Nothing reported means head
// only added to base.
//
// Every code base has is still in head and carries the key base recorded for it.
// A key is what a code stands for, so two entries that swap contents, or a code
// pointed at another failure, change a key even though the sequence stays dense
// and every entry stays well formed. Wording (meaning, remedy, command), the
// retired mark, the order of the entries and entries after base's are free to
// change. A base entry with no key records no identity yet and holds nothing to
// compare, so a registry that gains its keys passes against one that has none.
func checkAppendOnly(base, head []ErrorCode) []string {
	now := make(map[string]ErrorCode, len(head))
	for _, c := range head {
		now[c.Code] = c
	}

	was := append([]ErrorCode(nil), base...)
	sort.Slice(was, func(i, j int) bool { return was[i].Code < was[j].Code })

	var violations []string
	for _, b := range was {
		h, ok := now[b.Code]
		switch {
		case !ok:
			violations = append(violations, fmt.Sprintf(
				"%s is missing: a code is never deleted, so set retired to true and leave it in place", b.Code))
		case b.Key != "" && h.Key != b.Key:
			has := "no key"
			if h.Key != "" {
				has = fmt.Sprintf("the key %q", h.Key)
			}
			violations = append(violations, fmt.Sprintf(
				"%s has %s where it had the key %q: a code stands for one failure for good, so a new failure needs a new code",
				b.Code, has, b.Key))
		}
	}
	return violations
}

// baseRegistryEnv names the file that holds the registry as it stands on the
// branch this change merges into. The pull-request job writes it. By hand:
//
//	git show origin/main:cli/common/errorcodes.json > /tmp/base.json
//	DRIFT_ERRORCODES_BASE=/tmp/base.json go test ./common -run AppendOnlyAgainstBase -v
const baseRegistryEnv = "DRIFT_ERRORCODES_BASE"

// The registry only grew since the base branch's: no code is missing and no key
// moved. With no base file there is nothing to compare with, and the test says so
// by skipping. A base file that cannot be read, or holds no codes, fails: a
// comparison that could not be made must not read as one that passed.
func TestErrorCodes_AppendOnlyAgainstBase(t *testing.T) {
	path := os.Getenv(baseRegistryEnv)
	if path == "" {
		t.Skipf("%s is not set, so there is no base registry to compare with", baseRegistryEnv)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from the pull-request job
	if err != nil {
		t.Fatalf("reading the base registry named by %s: %v", baseRegistryEnv, err)
	}
	var doc struct {
		Codes []ErrorCode `json:"codes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the base registry in %s does not parse: %v", path, err)
	}
	if len(doc.Codes) == 0 {
		t.Fatalf("the base registry in %s holds no codes, so there is nothing to compare with", path)
	}

	head := ErrorCodes()
	inBase := make(map[string]bool, len(doc.Codes))
	baseKeyed := 0
	for _, c := range doc.Codes {
		inBase[c.Code] = true
		if c.Key != "" {
			baseKeyed++
		}
	}
	headKeyed := 0
	var added []string
	for _, c := range head {
		if c.Key != "" {
			headKeyed++
		}
		if !inBase[c.Code] {
			added = append(added, c.Code)
		}
	}
	t.Logf("base registry: %d codes, %d with a key. This registry: %d codes, %d with a key, %d added %v",
		len(doc.Codes), baseKeyed, len(head), headKeyed, len(added), added)

	for _, violation := range checkAppendOnly(doc.Codes, head) {
		t.Error(violation)
	}
}

// A registry may only grow: every code it had is still there under the key it
// had. Swapping two entries' contents, pointing a code at another failure and
// deleting a code each leave the sequence dense and every entry well formed, so
// only a comparison with the registry the change started from can see them.
func TestCheckAppendOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(head []ErrorCode) []ErrorCode
		named  []string // the codes the violations name, and no others
	}{
		{"nothing changed", func(h []ErrorCode) []ErrorCode { return h }, nil},
		{"two entries swap contents", func(h []ErrorCode) []ErrorCode {
			h[2], h[3] = swapContents(h[2], h[3])
			return h
		}, []string{"DRIFT-1003", "DRIFT-1004"}},
		{"the last code is missing", func(h []ErrorCode) []ErrorCode { return h[:3] }, []string{"DRIFT-1004"}},
		{"a code in the middle is missing", func(h []ErrorCode) []ErrorCode { return append(h[:1], h[2:]...) }, []string{"DRIFT-1002"}},
		{"a key changes", func(h []ErrorCode) []ErrorCode {
			h[3].Key = "limit"
			return h
		}, []string{"DRIFT-1004"}},
		{"a key is removed", func(h []ErrorCode) []ErrorCode {
			h[3].Key = ""
			return h
		}, []string{"DRIFT-1004"}},
		{"a code takes another's key", func(h []ErrorCode) []ErrorCode {
			h[3].Key = h[2].Key
			return h
		}, []string{"DRIFT-1004"}},

		{"an entry is added", func(h []ErrorCode) []ErrorCode {
			return append(h, ErrorCode{Code: "DRIFT-1005", Key: "platform-fault", Meaning: "The platform failed.", Remedy: "Retry once."})
		}, nil},
		{"every wording is reworded", func(h []ErrorCode) []ErrorCode {
			for i := range h {
				h[i].Meaning += " It reads better now."
				h[i].Remedy = "A clearer remedy."
				h[i].Command = ""
			}
			return h
		}, nil},
		{"the entries come in another order", func(h []ErrorCode) []ErrorCode {
			h[0], h[3] = h[3], h[0]
			return h
		}, nil},
		{"a code is retired", func(h []ErrorCode) []ErrorCode {
			h[0].Retired = true
			return h
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkAppendOnly(appendOnlyRegistry(), tc.change(appendOnlyRegistry()))

			for _, code := range []string{"DRIFT-1001", "DRIFT-1002", "DRIFT-1003", "DRIFT-1004", "DRIFT-1005"} {
				named := false
				for _, violation := range got {
					if strings.Contains(violation, code) {
						named = true
					}
				}
				want := false
				for _, w := range tc.named {
					if w == code {
						want = true
					}
				}
				if named != want {
					t.Errorf("%s is named = %v, want %v; the violations are %q", code, named, want, got)
				}
			}
			if (len(tc.named) == 0) != (len(got) == 0) {
				t.Errorf("the violations are %q, want %d of them", got, len(tc.named))
			}
			for _, violation := range got {
				if !strings.Contains(violation, "DRIFT-") {
					t.Errorf("the violation %q names no code", violation)
				}
			}
		})
	}
}

// Each violation says what is wrong with its code, so the person reading the job
// log knows what to undo: a code that went missing is missing, and a key that
// moved says the key it had. The table above cannot tell the two apart, since a
// missing entry has no key either.
func TestCheckAppendOnly_SaysWhatIsWrong(t *testing.T) {
	got := checkAppendOnly(appendOnlyRegistry(), appendOnlyRegistry()[:3])
	if len(got) != 1 || !strings.HasPrefix(got[0], "DRIFT-1004 is missing") {
		t.Errorf("a code that went missing gave %q, want one violation starting %q", got, "DRIFT-1004 is missing")
	}

	moved := appendOnlyRegistry()
	moved[3].Key = "limit"
	got = checkAppendOnly(appendOnlyRegistry(), moved)
	if want := `DRIFT-1004 has the key "limit" where it had the key "quota"`; len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Errorf("a key that changed gave %q, want one violation starting %q", got, want)
	}

	removed := appendOnlyRegistry()
	removed[3].Key = ""
	got = checkAppendOnly(appendOnlyRegistry(), removed)
	if want := `DRIFT-1004 has no key where it had the key "quota"`; len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Errorf("a key that was removed gave %q, want one violation starting %q", got, want)
	}
}

// A registry that has no keys yet holds nothing to compare them with, so the
// change that gives it keys passes, and a code that goes missing still fails.
func TestCheckAppendOnly_ABaseWithoutKeysHoldsTheCodesOnly(t *testing.T) {
	base := appendOnlyRegistry()
	for i := range base {
		base[i].Key = ""
	}

	if got := checkAppendOnly(base, appendOnlyRegistry()); len(got) != 0 {
		t.Errorf("a registry that gained its keys was refused: %q", got)
	}
	if got := checkAppendOnly(base, appendOnlyRegistry()[:3]); len(got) != 1 || !strings.Contains(got[0], "DRIFT-1004") {
		t.Errorf("a code missing from a registry without keys gave %q, want one violation naming DRIFT-1004", got)
	}
}

// The input is something a person retyped from a terminal they have already
// closed, so the lookup must not be pedantic about how they wrote it.
func TestLookupErrorCode_AcceptsHowAPersonWouldType(t *testing.T) {
	want, ok := LookupErrorCode("DRIFT-1001")
	if !ok {
		t.Fatal("DRIFT-1001 is registered and must resolve")
	}
	for _, spelling := range []string{"drift-1001", "Drift-1001", "1001", "  DRIFT-1001  "} {
		got, ok := LookupErrorCode(spelling)
		if !ok {
			t.Errorf("%q did not resolve", spelling)
			continue
		}
		if got.Code != want.Code {
			t.Errorf("%q resolved to %s, want %s", spelling, got.Code, want.Code)
		}
	}
}

// The control: an unregistered code must NOT resolve. Without this the test
// above would pass for a lookup that returns something for any input.
func TestLookupErrorCode_AnUnknownCodeDoesNotResolve(t *testing.T) {
	for _, unknown := range []string{"DRIFT-9999", "banana", ""} {
		if _, ok := LookupErrorCode(unknown); ok {
			t.Errorf("%q resolved, but nothing is registered under it", unknown)
		}
	}
}

// A code numbered above every registered one is what a newer drift prints. Where
// the line falls is read from the registry, so adding a code moves it.
func TestNewerErrorCode(t *testing.T) {
	highest := 0
	for _, c := range ErrorCodes() {
		if n := errorCodeNumber(c.Code); n > highest {
			highest = n
		}
	}
	if highest == 0 {
		t.Fatal("the registry is empty")
	}
	top := fmt.Sprintf("DRIFT-%d", highest)
	above := fmt.Sprintf("DRIFT-%d", highest+1)

	for _, tc := range []struct {
		name, typed string
		code        string // the registry's spelling of typed, for a newer code
		newer       bool
	}{
		{"the code itself", above, above, true},
		{"lower case", strings.ToLower(above), above, true},
		{"a bare number", fmt.Sprintf("%d", highest+1), above, true},
		{"padded", fmt.Sprintf("  %d ", highest+1), above, true},
		{"far above", "DRIFT-99999999", "DRIFT-99999999", true},

		{"the highest registered code", top, "", false},
		{"the first registered code", "DRIFT-1001", "", false},
		{"below the first code", "DRIFT-1000", "", false},
		{"zero", "0", "", false},
		{"words", "banana", "", false},
		{"nothing", "", "", false},
		{"the prefix alone", "DRIFT-", "", false},
		{"letters after the prefix", "DRIFT-ABC", "", false},
		{"a suffix after the number", fmt.Sprintf("%dx", highest+1), "", false},
		{"a sign before the number", fmt.Sprintf("+%d", highest+1), "", false},
		{"a number too large to be one", "99999999999999999999", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, gotHighest, newer := NewerErrorCode(tc.typed)
			if newer != tc.newer {
				t.Fatalf("NewerErrorCode(%q) = newer %v, want %v", tc.typed, newer, tc.newer)
			}
			if !tc.newer {
				return
			}
			if code != tc.code {
				t.Errorf("NewerErrorCode(%q) code = %q, want %q", tc.typed, code, tc.code)
			}
			if gotHighest != top {
				t.Errorf("NewerErrorCode(%q) highest = %q, want %q", tc.typed, gotHighest, top)
			}
		})
	}
}

// The failure carries its own name, so nobody has to already know the verb.
func TestWithCode_PrintsTheCodeAndHowToExpandIt(t *testing.T) {
	got := withCode("Couldn't create slice: your session expired.", "DRIFT-1001")
	if !strings.Contains(got, "DRIFT-1001") {
		t.Errorf("the code must appear in the message, got: %s", got)
	}
	if !strings.Contains(got, "drift doctor explain DRIFT-1001") {
		t.Errorf("the message must name the command that expands it, got: %s", got)
	}
	if !strings.HasPrefix(got, "Couldn't create slice") {
		t.Errorf("the original message must lead, got: %s", got)
	}
}

// A failure with no registered code renders exactly as it did before. The
// registry is added to over time, and a path that has not been named yet must
// not grow an empty suffix.
func TestWithCode_LeavesAnUncodedMessageAlone(t *testing.T) {
	const msg = "Couldn't do the thing: reasons."
	if got := withCode(msg, ""); got != msg {
		t.Errorf("an uncoded message must be untouched, got: %s", got)
	}
}

// A failure the platform returned must carry its name, or the registry is a file
// nobody reaches. Keyed on STATUS rather than wording, so improving a message
// cannot silently drop the code.
func TestAPIError_CarriesItsCode(t *testing.T) {
	// A 5xx consults component health, so without a stub this test would reach
	// the real status page: slow, and dependent on the platform being up to
	// assert something that has nothing to do with it.
	withStatusFeed(t, 200, feedOK)

	for _, tc := range []struct {
		name   string
		err    APIError
		want   string
		absent bool
	}{
		{name: "expired session", err: APIError{Op: "list slices", Status: 401}, want: "DRIFT-1001"},
		{name: "forbidden", err: APIError{Op: "delete slice", Status: 403}, want: "DRIFT-1002"},
		{name: "not found", err: APIError{Op: "delete atomic function", Status: 404}, want: "DRIFT-1003"},
		{name: "quota", err: APIError{Op: "deploy", Status: 429}, want: "DRIFT-1004"},
		{name: "platform fault", err: APIError{Op: "deploy", Status: 500}, want: "DRIFT-1005"},
		// A refused LOGIN is not an expired session. Pointing at `account login`
		// would send the user back to the command they just ran.
		{name: "refused login", err: APIError{Op: "log in", Status: 401}, absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if tc.absent {
				if strings.Contains(got, "DRIFT-") {
					t.Errorf("this failure must carry no code, got: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %s in the message, got: %s", tc.want, got)
			}
			if _, ok := LookupErrorCode(tc.want); !ok {
				t.Errorf("%s is rendered but not registered, so `doctor explain` cannot answer it", tc.want)
			}
		})
	}
}

// Every code the CLI can PRINT must be registered, or a user pastes one that
// `doctor explain` does not know. This walks the statuses the renderer maps and
// checks each resolves — the half that would otherwise be caught only by a user.
func TestAPIError_EveryRenderedCodeIsRegistered(t *testing.T) {
	for status := 400; status < 600; status++ {
		e := APIError{Op: "do the thing", Status: status}
		code := e.code()
		if code == "" {
			continue
		}
		if _, ok := LookupErrorCode(code); !ok {
			t.Errorf("status %d renders %s, which is not in the registry", status, code)
		}
	}
}

// A refusal must say what the ceiling is and where the user is against it,
// without them running anything else. The numbers come from the server's FIELDS,
// so the CLI cannot be wrong about someone's quota.
func TestAPIError_ARefusalCarriesTheNumbers(t *testing.T) {
	withStatusFeed(t, 200, feedOK)

	body := []byte(`{"error":"limit reached for atomic.deploy_function (current: 6, max: 6)",` +
		`"action":"atomic.deploy_function","limit":6,"current":6}`)
	action, limit, current, has := quotaFromBody(body)
	if !has || limit != 6 || current != 6 || action != "atomic.deploy_function" {
		t.Fatalf("the fields did not parse: %q %d %d %v", action, limit, current, has)
	}

	e := &APIError{Op: "deploy", Status: 429, Detail: "limit reached",
		Action: action, Limit: limit, Current: current, HasLimit: has}
	got := e.Error()
	if !strings.Contains(got, "6 of 6 used") {
		t.Errorf("the refusal must state current and ceiling, got: %s", got)
	}
	if !strings.Contains(got, "atomic.deploy_function") {
		t.Errorf("the refusal must name WHICH limit, got: %s", got)
	}
	if !strings.Contains(got, "DRIFT-1004") {
		t.Errorf("the refusal must still carry its code, got: %s", got)
	}
}

// The control, and the honest case: a platform that publishes no ceiling must
// produce a refusal that says nothing about limits rather than inventing "0 of 0".
func TestAPIError_ARefusalWithoutNumbersSaysNothingAboutThem(t *testing.T) {
	withStatusFeed(t, 200, feedOK)

	if _, _, _, has := quotaFromBody([]byte(`{"error":"limit reached"}`)); has {
		t.Fatal("a body with no ceiling must not report one")
	}
	got := (&APIError{Op: "deploy", Status: 429, Detail: "limit reached"}).Error()
	if strings.Contains(got, "used") {
		t.Errorf("no ceiling was published, so none should be rendered, got: %s", got)
	}
}

// Numbers are read from the fields, never re-parsed out of the sentence — that
// coupling is what publishing fields exists to remove.
func TestQuotaFromBody_IgnoresNumbersInTheProse(t *testing.T) {
	if _, _, _, has := quotaFromBody([]byte(`{"error":"limit reached (current: 6, max: 6)"}`)); has {
		t.Error("the figures in the message must NOT be scraped — only fields count")
	}
}
