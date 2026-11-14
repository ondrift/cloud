package project

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A plan says where each domain the file declares stands, and lists what the slice
// holds that the file does not name. Neither is a difference: nothing marks them,
// apply changes none of them, and the person is told what to do.

const noList = `[]`

// domainsOfEveryState is the slice's domains, one in each state the platform
// reports, in the order declaresEveryState declares them.
const domainsOfEveryState = `[
 {"host":"live.example.com","status":"live"},
 {"host":"verified.example.com","status":"verified"},
 {"host":"pending.example.com","status":"pending","txt_token":"abc123"},
 {"host":"failed.example.com","status":"failed","status_detail":"TXT record _drift-challenge.failed.example.com not found or did not contain drift-verify=abc123"}]`

var declaresEveryState = []string{
	`  domain "live.example.com" {}`,
	`  domain "verified.example.com" {}`,
	`  domain "pending.example.com" {}`,
	`  domain "failed.example.com" {}`,
}

// withDomains edits the fxevm file to declare each of blocks in its edge block.
func withDomains(t *testing.T, blocks ...string) func(string) string {
	t.Helper()
	return func(s string) string {
		return mustReplace(t, s, "edge {\n  site_auth {", "edge {\n"+strings.Join(blocks, "\n")+"\n  site_auth {")
	}
}

// stubSliceHolding answers as the platform of the slice that is the fxevm file
// and holds these domains and SQL databases, each a JSON list.
func stubSliceHolding(t *testing.T, domains, databases string) *recorder {
	t.Helper()
	live, deployed := matchingSlice(t)
	return planStubWith(t, live, deployed, map[string]string{
		"/ops/slice/domain":            domains,
		"/ops/backbone/sql/admin/list": databases,
	})
}

// planLinesFor is every line of the plan's body printed for address, with its
// mark and what it says. The body ends where the summary of price, restart and
// removals begins.
func planLinesFor(out, address string) []planLine {
	body, _, _ := strings.Cut(out, "\n  Price ")
	var found []planLine
	for _, line := range strings.Split(body, "\n") {
		if len(line) < 5 || !strings.HasPrefix(line, "  ") || line[3] != ' ' {
			continue
		}
		if rest := line[4:]; strings.HasPrefix(rest, address+" ") {
			found = append(found, planLine{string(line[2]), address, strings.TrimSpace(rest[len(address):])})
		}
	}
	return found
}

// sectionAfter is the lines that follow the line holding heading, up to the next
// blank line.
func sectionAfter(out, heading string) []string {
	_, after, found := strings.Cut(out, heading)
	if !found {
		return nil
	}
	lines := strings.Split(after, "\n")[1:]
	var section []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			break
		}
		section = append(section, line)
	}
	return section
}

const (
	verifyPending  = "not verified: set the DNS record the add printed, then run drift slice domain verify pending.example.com"
	verifyVerified = "verified, not live yet: run drift slice domain verify verified.example.com"
	verifyFailed   = "failed: TXT record _drift-challenge.failed.example.com not found or did not contain drift-verify=abc123; " +
		"once the record is right, run drift slice domain verify failed.example.com"
)

// A declared domain that is attached says where it stands, with no change mark:
// live, verified and not live yet, not verified, or failed with the platform's
// reason. Each that is not live is what a person has to act on, so it is in the
// verdict's attention list and under "Needs you"; the slice is still the file.
func TestPlan_EachDeclaredDomainSaysWhereItStands(t *testing.T) {
	t.Skip("until #3029 prints each declared domain's state")
	stubSliceHolding(t, domainsOfEveryState, noList)
	out, err := planIn(t, withDomains(t, declaresEveryState...))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, c := range []struct{ host, want string }{
		{"live.example.com", "live"},
		{"verified.example.com", verifyVerified},
		{"pending.example.com", verifyPending},
		{"failed.example.com", verifyFailed},
	} {
		lines := planLinesFor(out, "domain "+c.host)
		if len(lines) != 1 {
			t.Errorf("the plan prints %d lines for domain %s, want one:\n%s", len(lines), c.host, out)
			continue
		}
		if lines[0].mark != " " || lines[0].detail != c.want {
			t.Errorf("the plan says %q with the mark %q for domain %s, want %q with no mark", lines[0].detail, lines[0].mark, c.host, c.want)
		}
	}

	wantAttention := []string{
		"domain verified.example.com  " + verifyVerified,
		"domain pending.example.com  " + verifyPending,
		"domain failed.example.com  " + verifyFailed,
	}
	_, afterNeeds, found := strings.Cut(out, "Needs you")
	if !found {
		t.Fatalf("the plan has no \"Needs you\" line for the three domains that are not live:\n%s", out)
	}
	for _, want := range wantAttention {
		if !strings.Contains(afterNeeds, want) {
			t.Errorf("under \"Needs you\" the plan does not say %q:\n%s", want, afterNeeds)
		}
	}
	if strings.Contains(afterNeeds, "domain live.example.com") {
		t.Errorf("a live domain needs no one, and the plan lists it under \"Needs you\":\n%s", afterNeeds)
	}

	v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	if v.State != VerdictMatch || len(v.Changes) != 0 {
		t.Errorf("a slice whose domains only need a person is %q with the changes %q, want a match with none", v.State, v.Changes)
	}
	if strings.Join(v.Attention, "\n") != strings.Join(wantAttention, "\n") {
		t.Errorf("the attention list is %q, want %q", v.Attention, wantAttention)
	}
	if got := headerOf(t, out); got != "unchanged" {
		t.Errorf("the header says %q, want %q: nothing differs", got, "unchanged")
	}
	if !strings.Contains(out, closingMatch) {
		t.Errorf("nothing differs and the plan does not end on %q:\n%s", closingMatch, out)
	}

	// Needing a person is not a difference, so a script is not failed by it.
	if _, err := planOutput(t, "--no-env-file", "--exit-code"); exitStatusOf(err) != 0 {
		t.Errorf("drift plan --exit-code exits %d for a slice that matches and has domains to verify, want 0 (%v)", exitStatusOf(err), err)
	}
}

// The controls. A domain the slice lacks is added, which is a difference with a
// mark and needs no one; a live domain whose wildcard flag agrees has nothing to
// say but that it is live.
func TestPlan_ADomainTheSliceLacksIsAddedAndALiveOneNeedsNoOne(t *testing.T) {
	t.Skip("until #3029 prints each declared domain's state")
	stubSliceHolding(t, `[{"host":"live.example.com","status":"live","wildcard":true}]`, noList)
	out, err := planIn(t, withDomains(t,
		`  domain "live.example.com" {`+"\n    wildcard = true\n  }",
		`  domain "new.example.com" {}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := planLinesFor(out, "domain new.example.com"); len(got) != 1 || got[0].mark != "+" || got[0].detail != "added" {
		t.Errorf("the plan says %v for the domain the slice lacks, want \"+ added\"", got)
	}
	if got := planLinesFor(out, "domain live.example.com"); len(got) != 1 || got[0].mark != " " || got[0].detail != "live" {
		t.Errorf("the plan says %v for the live domain, want one line that says live", got)
	}

	v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	if v.State != VerdictDiffers || len(v.Attention) != 0 {
		t.Errorf("the verdict is %q with the attention %q, want differs with none", v.State, v.Attention)
	}
	if strings.Contains(out, "Needs you") {
		t.Errorf("nothing needs a person and the plan says so:\n%s", out)
	}
}

// A status the CLI does not know is not known to be live, so it needs a person
// too, and the plan names the status it was given.
func TestPlan_ADomainInAnUnknownStatusIsNotKnownToBeLive(t *testing.T) {
	t.Skip("until #3029 prints each declared domain's state")
	stubSliceHolding(t, `[{"host":"odd.example.com","status":"suspended"}]`, noList)
	out, err := planIn(t, withDomains(t, `  domain "odd.example.com" {}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := `status "suspended" is not one this CLI knows: drift slice domain list shows what the platform says`
	if got := planLinesFor(out, "domain odd.example.com"); len(got) != 1 || got[0].mark != " " || got[0].detail != want {
		t.Errorf("the plan says %v, want one line without a mark that says %q", got, want)
	}
	v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	if len(v.Attention) != 1 || v.Attention[0] != "domain odd.example.com  "+want {
		t.Errorf("the attention list is %q, want the domain once", v.Attention)
	}
}

// A declared wildcard that differs from the slice's, either way, is a fact apply
// cannot change: the platform's add is idempotent and has no update. It is no
// difference to show, so it is a thing a person has to do.
func TestPlan_ADomainWhoseWildcardDiffersNamesHowToChangeIt(t *testing.T) {
	t.Skip("until #3030 compares the wildcard flag")
	stubSliceHolding(t, `[
 {"host":"shop.example.com","status":"live"},
 {"host":"plain.example.com","status":"live","wildcard":true},
 {"host":"same.example.com","status":"live","wildcard":true}]`, noList)
	out, err := planIn(t, withDomains(t,
		`  domain "shop.example.com" {`+"\n    wildcard = true\n  }",
		`  domain "plain.example.com" {}`,
		`  domain "same.example.com" {`+"\n    wildcard = true\n  }"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	onInFileOffOnSlice := "wildcard is on in the file and off on the slice: apply leaves it; drift slice domain remove shop.example.com, then apply"
	offInFileOnOnSlice := "wildcard is off in the file and on on the slice: apply leaves it; drift slice domain remove plain.example.com, then apply"
	for _, c := range []struct{ host, want string }{
		{"shop.example.com", onInFileOffOnSlice},
		{"plain.example.com", offInFileOnOnSlice},
	} {
		found := false
		for _, l := range planLinesFor(out, "domain "+c.host) {
			if l.detail == c.want {
				found = true
				if l.mark != " " {
					t.Errorf("the plan marks the wildcard line of %s with %q: apply cannot change it", c.host, l.mark)
				}
			}
		}
		if !found {
			t.Errorf("the plan does not say %q for %s:\n%s", c.want, c.host, out)
		}
	}
	if got := planLinesFor(out, "domain same.example.com"); len(got) != 1 || got[0].detail != "live" {
		t.Errorf("a wildcard that agrees says %v, want the domain's state alone", got)
	}

	v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	wantAttention := []string{
		"domain shop.example.com  " + onInFileOffOnSlice,
		"domain plain.example.com  " + offInFileOnOnSlice,
	}
	if strings.Join(v.Attention, "\n") != strings.Join(wantAttention, "\n") {
		t.Errorf("the attention list is %q, want %q", v.Attention, wantAttention)
	}
	if v.State != VerdictMatch || len(v.Changes) != 0 {
		t.Errorf("a wildcard that differs is %q with the changes %q, want a match with none", v.State, v.Changes)
	}
}

// A domain or database the slice holds and the file does not name is listed under
// "Held, not in the file" with the command that removes it. Nothing marks it, the
// plan does not say it removes it, and a host matches whatever its case.
func TestPlan_ListsWhatTheSliceHoldsAndTheFileDoesNotName(t *testing.T) {
	t.Skip("until #3031 builds the list of what the slice holds")
	stubSliceHolding(t,
		`[{"host":"live.example.com","status":"live"},{"host":"old.example.com","status":"live"}]`,
		`[{"name":"chain","size_bytes":1},{"name":"ledger","size_bytes":2}]`)
	out, err := planIn(t, withDomains(t, `  domain "Live.Example.com" {}`))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	held := sectionAfter(out, "Held, not in the file")
	if len(held) == 0 {
		t.Fatalf("the plan has no \"Held, not in the file\" section:\n%s", out)
	}
	text := strings.Join(held, "\n")
	for _, want := range []string{
		"sql database ledger", "drift backbone sql drop ledger",
		"domain old.example.com", "drift slice domain remove old.example.com",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the held section does not say %q:\n%s", want, text)
		}
	}
	for _, not := range []string{"chain", "live.example.com"} {
		if strings.Contains(text, not) {
			t.Errorf("the held section lists %q, which the file names:\n%s", not, text)
		}
	}
	if len(held) != 2 {
		t.Errorf("the held section has %d lines, want one for the database and one for the domain:\n%s", len(held), text)
	}

	if !strings.Contains(out, "Removes   nothing") {
		t.Errorf("the plan removes something it holds and does not name:\n%s", out)
	}
	for _, l := range planLinesFor(out, "domain old.example.com") {
		t.Errorf("the body of the plan has the line %v for a domain the file does not name", l)
	}
	v, err := PlanVerdict(PlanOptions{NoEnvFile: true}, io.Discard)
	if err != nil {
		t.Fatalf("PlanVerdict: %v", err)
	}
	if v.State != VerdictMatch || len(v.Changes) != 0 || len(v.Attention) != 0 {
		t.Errorf("what is held is %q with the changes %q and the attention %q, want a match with neither", v.State, v.Changes, v.Attention)
	}
}

// A database the plan itself removes (the shape has it and the file does not) is
// said by its own line, so it is not also held and offered a second removal.
func TestPlan_ADatabaseThePlanRemovesIsNotAlsoHeld(t *testing.T) {
	t.Skip("until #3031 builds the list of what the slice holds")
	planStubWith(t, fxevmLive, `[]`, map[string]string{
		"/ops/backbone/sql/admin/list": `[{"name":"chain"},{"name":"database-1"},{"name":"ledger"}]`,
	})
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := planLinesFor(out, "sql database-1"); len(got) != 1 || got[0].mark != "-" {
		t.Fatalf("the plan says %v for the database it removes, want its - line:\n%s", got, out)
	}
	text := strings.Join(sectionAfter(out, "Held, not in the file"), "\n")
	if !strings.Contains(text, "sql database ledger") {
		t.Errorf("the held section does not list the database no line of the plan mentions:\n%s", out)
	}
	if strings.Contains(text, "database-1") {
		t.Errorf("the held section lists database-1, which the plan removes:\n%s", text)
	}
}

// A site the file does not name keeps its - line, and is not moved under "Held":
// the line is documented, and moving it is a decision of its own.
func TestPlan_ASiteTheFileDoesNotNameKeepsItsLine(t *testing.T) {
	live, deployed := matchingSlice(t)
	planStubWith(t, live, deployed, map[string]string{
		"/ops/canvas": `{"deployed":true,"sites":[{"slug":"default","route":"/"},{"slug":"docs","route":"/docs"}]}`,
	})
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := planLinesFor(out, "site /docs")
	if len(got) != 1 || got[0].mark != "-" || got[0].detail != "still served: no drift command removes a site yet" {
		t.Errorf("the plan says %v for the site the file does not name, want its - line", got)
	}
	if strings.Contains(out, "Held, not in the file") {
		t.Errorf("a site is listed under \"Held\", where only domains and databases are:\n%s", out)
	}
}

// The plan reads the slice's domains and databases whether or not the file
// declares any, because what the file does not name is exactly what the file
// cannot tell it to read, and it still writes nothing.
func TestPlan_ReadsTheDomainAndDatabaseListsWhateverTheFileDeclares(t *testing.T) {
	t.Skip("until #3031 builds the list of what the slice holds")
	rec := stubSliceHolding(t, noList, noList)
	if _, err := planIn(t, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, read := range []string{"GET /ops/slice/domain", "GET /ops/backbone/sql/admin/list"} {
		if n := rec.count(read); n != 1 {
			t.Errorf("the plan of a file that declares no domain made %d of %q, want 1", n, read)
		}
	}
	for _, hit := range rec.hits {
		if strings.HasPrefix(hit, "GET ") || hit == "POST /ops/slice/price" {
			continue
		}
		t.Errorf("A PLAN ISSUED %q. It reads, asks for a quote, and writes nothing.", hit)
	}
}

// A slice that does not exist yet holds nothing, so the plan reads its lists nowhere.
func TestPlan_ASliceToCreateHasNoListsToRead(t *testing.T) {
	rec := planStub(t, "", `[]`)
	if _, err := planIn(t, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, read := range []string{"GET /ops/slice/domain", "GET /ops/backbone/sql/admin/list"} {
		if n := rec.count(read); n != 0 {
			t.Errorf("the plan of a slice that does not exist made %d of %q, want none", n, read)
		}
	}
}

// A list the plan could not read is unread, never an empty list: the plan does
// not say nothing is held, and does not say it has nothing to change.
func TestPlan_AListItCouldNotReadIsUnreadAndNotEmpty(t *testing.T) {
	t.Skip("until #3031 builds the list of what the slice holds")
	for _, c := range []struct {
		name, failing, fact, reason string
		edit                        func(*testing.T, string) string
	}{
		{name: "the slice's domains", failing: "/ops/slice/domain", fact: "domain", reason: "list domains"},
		{name: "the slice's SQL databases", failing: "/ops/backbone/sql/admin/list", fact: "sql databases", reason: "list sql databases"},
		{
			// The file declares no site, so nothing in it asks for the list but the
			// sites the file does not name.
			name: "the slice's sites", failing: "/ops/canvas", fact: "sites", reason: "list canvas sites",
			edit: func(t *testing.T, s string) string {
				return mustReplace(t, s, "canvas {\n  site \"/\" {\n    dir = \"canvas\"\n  }\n}\n", "")
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			live, deployed := matchingSlice(t)
			planStub(t, live, deployed, c.failing)
			var edit func(string) string
			if c.edit != nil {
				edit = func(s string) string { return c.edit(t, s) }
			}
			d := diffIn(t, edit)
			if got, want := factsOf(d), []string{c.fact + "|" + c.fact}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("the plan recorded the unread facts %q, want %q", got, want)
			}
			if err := d.unreadFacts[0].err; err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("the fact says it was unread because %v, want %q", err, c.reason)
			}

			planStub(t, live, deployed, c.failing)
			out, err := planIn(t, edit)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if strings.Contains(out, closingMatch) || !strings.Contains(out, closingUnread) {
				t.Errorf("a plan that could not read %s does not end on %q:\n%s", c.name, closingUnread, out)
			}
			if got := headerOf(t, out); got != "unchanged (not fully read)" {
				t.Errorf("the header says %q, want %q", got, "unchanged (not fully read)")
			}
		})
	}
}

// ─── apply's closing report ─────────────────────────────────────────────────

// dash is the em dash the closing report's line about a site carries.
const dash = "\u2014"

// `drift apply` closes with what the slice holds and the file does not name, and
// that list is the plan's: the report does not read the slice a second time. The
// stub answers the first read of each list with what is held and every later one
// with nothing, so a report that read again would be empty.
func TestApply_ClosesWithTheListThePlanRead(t *testing.T) {
	t.Skip("until #3032 prints the plan's list in apply's closing report")
	var mu sync.Mutex
	reads := map[string]int{}
	answer := func(w http.ResponseWriter, r *http.Request) bool {
		var first, later string
		switch r.URL.Path {
		case "/ops/backbone/sql/admin/list":
			first, later = `[{"name":"ledger"},{"name":"audit"}]`, `[{"name":"ledger"}]`
		case "/ops/slice/domain":
			first, later = `[{"host":"old.example.com","status":"live"}]`, `[]`
		case "/ops/canvas":
			first, later = `{"deployed":true,"sites":[{"slug":"blog","route":"/blog"}]}`, `{"deployed":false,"sites":[]}`
		default:
			return false
		}
		if r.Method != http.MethodGet {
			return false
		}
		mu.Lock()
		reads[r.URL.Path]++
		n := reads[r.URL.Path]
		mu.Unlock()
		body := first
		if n > 1 {
			body = later
		}
		_, _ = io.WriteString(w, body)
		return true
	}
	rec := orderProjectAnswering(t, answer)

	out := captureStdout(t, func() {
		if err := applyAgreeingAtZero(t); err != nil {
			t.Errorf("apply: %v", err)
		}
	})

	want := "  \u00b7 Live on this slice, and not named in this Driftfile:\n" +
		"      sql database audit\n" +
		"        remove it with: drift backbone sql drop audit\n" +
		"      domain old.example.com\n" +
		"        remove it with: drift slice domain remove old.example.com\n" +
		"      canvas site blog\n" +
		"        still served " + dash + " no drift command removes a site\n" +
		"    Nothing was removed: a slice can serve more than one project, and a\n" +
		"    deploy cannot tell a resource that is not yours from one you deleted.\n"
	if !strings.Contains(out, want) {
		t.Errorf("the closing report is not the plan's list, one database, one domain and one site:\n%s", out)
	}
	if n := strings.Count(out, "Live on this slice"); n != 1 {
		t.Errorf("apply printed the report %d times, want once", n)
	}
	if strings.Contains(out, "Held, not in the file") {
		t.Errorf("apply prints the plan's held section as well as its report:\n%s", out)
	}
	for read, want := range map[string]int{
		"GET /ops/canvas":                  1,
		"GET /ops/backbone/sql/admin/list": 1,
		// The plan's read, and the read apply decides which domains to add by.
		"GET /ops/slice/domain": 2,
	} {
		if n := rec.count(read); n != want {
			t.Errorf("apply made %d of %q, want %d", n, read, want)
		}
	}
}
