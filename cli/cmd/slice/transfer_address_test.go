// transfer_address_test.go: what the transfer verbs tell a person about the
// slice's address.
//
// A slice's default address is `<account>-<slice>.<domain>`, named after the
// account that controls it. Accepting a transfer moves the slice to the
// recipient's account, so its address takes the recipient's name. The address it
// leaves behind redirects to the new one for 30 days and then stops answering
// (the platform's routes/slice_transfer.go, transferRedirectWindow, and the
// router's redirect).
//
// These tests run the real verbs against a fake API that answers with the
// platform's own wire shapes, and assert on what a person reads: that the address
// changes, what it changes from and to, what the old one does and for how long,
// and that no sentence says the address stays.
package slice

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The prompts the verbs print, which split what a person reads before they
// answer from what they read after. The split is how an assertion about the
// message that follows an accept is kept from being satisfied by the terms that
// came before it.
const (
	offerPrompt  = "Offer it? [y/N]: "
	acceptPrompt = "Accept it? [y/N]: "
)

// addressMove is the part of an offer view that says the address moves: the
// address the slice leaves and how many days it redirects.
const addressMove = `"previous_host":"alice-shop.ondrift.eu","redirect_days":30,`

// offerView is one offer as the platform writes it (routes/slice_transfer.go,
// transferView): alice offers slice `shop` to bob, so the address it takes is
// bob's and the address it leaves is alice's.
func offerView(direction string) string {
	return offerViewWith(direction, addressMove)
}

// offerViewWith is offerView with move in place of the part that says the address
// moves, so a platform that does not say it can be written too.
func offerViewWith(direction, move string) string {
	return fmt.Sprintf(`{"id":"0123456789abcdef0123456789abcdef","slice":"shop","from":"alice","to":"bob",`+
		`"status":"pending","direction":%q,"host":"bob-shop.ondrift.eu",%s`+
		`"created_at":"2026-11-14T10:00:00Z","expires_at":"2026-11-21T10:00:00Z",`+
		`"terms":{"tier":"custom","monthly_cost_cents":900,"billing_period_months":12,`+
		`"paid_until":"2027-01-01T00:00:00Z","config":{"atomic":{"functions":[{},{}]}}}}`, direction, move)
}

// platformAcceptNote is the note the platform sends with an accepted transfer
// for a slice with no custom domain (routes/slice_transfer.go, acceptReply), with
// the redirect ending on 14 Dec 2026.
const platformAcceptNote = "The previous owner set this slice's secrets, its function API keys and its site " +
	"password, and still knows them. Rotate every one you rely on. " +
	"Its address is now https://bob-shop.ondrift.eu. The old address, https://alice-shop.ondrift.eu, " +
	"redirects there until 14 Dec 2026 10:00 UTC and then stops answering; a caller that does not follow " +
	"redirects, such as most webhook senders, is not served by it even before then. Change anything outside " +
	"Drift that names the old address before it ends: a webhook or callback URL, an OAuth redirect URI, a link, " +
	"a DNS record. The slice restarts once, within a few minutes, to learn its new address."

// fakeTransferAPI answers the three transfer routes the verbs call and records
// the requests it was sent. note is the note its accept reply carries. unmoved
// makes it a platform whose offers name no address the slice leaves, and days a
// platform whose redirect lasts that many days instead of 30.
type fakeTransferAPI struct {
	note    string
	unmoved bool
	days    int

	mu   sync.Mutex
	sent []string
}

// view is one offer as this platform writes it.
func (a *fakeTransferAPI) view(direction string) string {
	switch {
	case a.unmoved:
		return offerViewWith(direction, "")
	case a.days != 0:
		return offerViewWith(direction,
			fmt.Sprintf(`"previous_host":"alice-shop.ondrift.eu","redirect_days":%d,`, a.days))
	}
	return offerView(direction)
}

func (a *fakeTransferAPI) record(r *http.Request, body []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, r.Method+" "+r.URL.Path+" "+string(body))
}

// bodiesOf returns the JSON bodies of the recorded requests to one route.
func (a *fakeTransferAPI) bodiesOf(t *testing.T, route string) []map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []map[string]any
	for _, s := range a.sent {
		prefix := route + " "
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(s, prefix)), &body); err != nil {
			t.Fatalf("a request to %s carried %q, which is not a JSON object: %v", route, s, err)
		}
		out = append(out, body)
	}
	return out
}

func (a *fakeTransferAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/ops/slice/transfer":
		a.record(r, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, a.view("outgoing"))
	case r.Method == http.MethodGet && r.URL.Path == "/ops/slice/transfer":
		a.record(r, body)
		_, _ = io.WriteString(w, `{"offers":[`+a.view("incoming")+`]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/ops/slice/transfer/accept":
		a.record(r, body)
		note, _ := json.Marshal(a.note)
		_, _ = fmt.Fprintf(w, `{"offer":%s,"host":"bob-shop.ondrift.eu","previous_host":"alice-shop.ondrift.eu",`+
			`"redirect_until":"2026-12-14T10:00:00Z","custom_domains":[],"note":%s}`, a.view("incoming"), note)
	default:
		http.NotFound(w, r)
	}
}

// accountToken builds an unsigned JWT that carries only the username claim. The
// CLI reads the account name out of the token's payload and leaves the signature
// to the platform.
func accountToken(username string) string {
	payload, _ := json.Marshal(map[string]any{"username": username})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "." + enc([]byte("sig"))
}

// transferVerbEnv carries the arguments of the verb TestTransferVerbProcess runs.
const transferVerbEnv = "DRIFT_TEST_TRANSFER_VERB"

// TestTransferVerbProcess is the child process runTransferVerb starts, not a test
// of its own: it runs one transfer verb in a process whose stdin, environment and
// stdout belong to the parent, and exits. The verbs read their answers through
// the CLI's shared stdin reader, which is bound to the process's real stdin when
// the process starts, so a process of their own is how a test types an answer.
func TestTransferVerbProcess(t *testing.T) {
	args := os.Getenv(transferVerbEnv)
	if args == "" {
		return
	}
	cmd := getTransferCmd()
	cmd.SetArgs(strings.Fields(args))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runTransferVerb runs `drift slice transfer <args>` as the account `as`, against
// api, with stdin as the typed answers, and returns everything the verb printed.
func runTransferVerb(t *testing.T, api http.Handler, as, stdin string, args ...string) string {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := common.SaveSession(accountToken(as), "refresh"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestTransferVerbProcess$")
	child.Env = append(os.Environ(),
		"HOME="+home,
		"DRIFT_API_URL="+srv.URL,
		"NO_COLOR=1",
		common.TokenEnv+"=",
		common.AccountEnv+"=",
		transferVerbEnv+"="+strings.Join(args, " "))
	child.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	if err := child.Run(); err != nil {
		t.Fatalf("drift slice transfer %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// transferHelp is what `drift slice transfer --help` prints.
func transferHelp(t *testing.T) string {
	t.Helper()
	cmd := getTransferCmd()
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetErr(&help)
	if err := cmd.Help(); err != nil {
		t.Fatalf("help: %v", err)
	}
	return help.String()
}

// splitAtPrompt returns what a verb printed before one of its prompts and what it
// printed after the prompt was answered.
func splitAtPrompt(t *testing.T, out, prompt string) (before, after string) {
	t.Helper()
	i := strings.Index(out, prompt)
	if i < 0 {
		t.Fatalf("the verb never asked %q; it printed:\n%s", prompt, out)
	}
	return out[:i], out[i+len(prompt):]
}

// oneLine lower-cases what a verb printed and joins its lines, so a sentence the
// CLI wraps across two lines is one sentence to an assertion.
func oneLine(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// mustSay fails for each phrase the screen does not contain.
func mustSay(t *testing.T, screen, text string, phrases ...string) {
	t.Helper()
	read := oneLine(text)
	for _, phrase := range phrases {
		if !strings.Contains(read, phrase) {
			t.Errorf("%s does not say %q:\n%s", screen, phrase, text)
		}
	}
}

// staysClaims are the sentences that claim a transfer leaves the address where it
// is, or leaves it named after the account that created the slice.
var staysClaims = []string{
	"address stay",
	"keeps the name of the account",
}

// mustNotSayTheAddressStays fails when the screen claims the address stays or
// keeps the previous owner's name.
func mustNotSayTheAddressStays(t *testing.T, screen, text string) {
	t.Helper()
	read := oneLine(text)
	for _, claim := range staysClaims {
		if strings.Contains(read, claim) {
			t.Errorf("%s says %q, and a transfer moves the address to the recipient's name:\n%s", screen, claim, text)
		}
	}
}

// The giver reads, before they type anything, that the address changes: the
// address it has now, the address it takes from the recipient's account name, and
// that the old one redirects to the new one for 30 days and then stops. The
// platform is not asked until they confirm, so the words come from the slice's
// name and the two account names; the domain they end in is not asserted.
func TestTransferOffer_TheConfirmationShowsTheAddressBeforeAndAfter(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{}, "alice", "", "offer", "shop", "--to", "bob")
	confirmation, _ := splitAtPrompt(t, out, offerPrompt)
	mustSay(t, "the offer's confirmation", confirmation, "alice-shop", "bob-shop", "30 days", "redirect", "stops")
}

// The transfer's help says the address follows the new owner and that the old
// one redirects for 30 days.
func TestTransferHelp_SaysTheOldAddressRedirectsFor30Days(t *testing.T) {
	mustSay(t, "`drift slice transfer --help`", transferHelp(t), "30 days", "redirect")
}

// The recipient reads, once the slice is theirs, the address it answers at now
// and how long the old one redirects. The platform's own note is left empty here
// so that these words are the CLI's own.
func TestTransferAccept_TheMessageNamesTheNewAddressAndTheRedirect(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{}, "bob", "y\n", "accept", "shop")
	_, message := splitAtPrompt(t, out, acceptPrompt)
	mustSay(t, "the accept message", message, "bob-shop.ondrift.eu", "30 days", "redirect")
}

// No screen of the transfer verbs says the address stays or keeps the previous
// owner's name, whichever side is reading and whatever the platform adds.
func TestTransfer_NoScreenSaysTheAddressStays(t *testing.T) {
	giver := runTransferVerb(t, &fakeTransferAPI{}, "alice", "y\nshop\n", "offer", "shop", "--to", "bob")
	recipient := runTransferVerb(t, &fakeTransferAPI{note: platformAcceptNote}, "bob", "y\n", "accept", "shop")
	list := runTransferVerb(t, &fakeTransferAPI{}, "bob", "", "list")

	for _, screen := range []struct{ name, text string }{
		{"`drift slice transfer --help`", transferHelp(t)},
		{"the giver's offer, from the confirmation to the receipt", giver},
		{"the recipient's accept, from the terms to the platform's note", recipient},
		{"`drift slice transfer list`", list},
	} {
		mustNotSayTheAddressStays(t, screen.name, screen.text)
	}
}

// The recipient reads, before they answer, the address the slice takes and the
// one it leaves, and how long the old one redirects.
func TestTransferAccept_TheTermsShowTheAddressBeforeAndAfter(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{}, "bob", "y\n", "accept", "shop")
	terms, _ := splitAtPrompt(t, out, acceptPrompt)
	mustSay(t, "the accept terms", terms,
		"address https://bob-shop.ondrift.eu", "old address https://alice-shop.ondrift.eu",
		"30 days", "redirect", "stops")
}

// The days a person is told come from the platform's offer when it names them, so
// a window the platform changes is the window the recipient reads.
func TestTransferAccept_TheRedirectWindowIsThePlatformsOwnFigure(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{days: 14}, "bob", "y\n", "accept", "shop")
	terms, message := splitAtPrompt(t, out, acceptPrompt)
	mustSay(t, "the accept terms", terms, "14 days")
	mustSay(t, "the accept message", message, "14 days")
	if read := oneLine(out); strings.Contains(read, "30 days") {
		t.Errorf("an offer that says 14 days is shown as 30:\n%s", out)
	}
}

// A platform that names no address the slice leaves does not move it, and the
// CLI claims no change on its behalf: one address, and no redirect to speak of.
func TestTransferAccept_AnOfferThatNamesNoOldAddressClaimsNoChange(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{unmoved: true}, "bob", "y\n", "accept", "shop")
	terms, message := splitAtPrompt(t, out, acceptPrompt)
	mustSay(t, "the accept terms", terms, "address https://bob-shop.ondrift.eu")
	mustSay(t, "the accept message", message, "answers at https://bob-shop.ondrift.eu")
	for _, screen := range []struct{ name, text string }{{"the accept terms", terms}, {"the accept message", message}} {
		read := oneLine(screen.text)
		for _, claim := range []string{"redirect", "old address", "alice-shop"} {
			if strings.Contains(read, claim) {
				t.Errorf("%s says %q for an offer that names no old address:\n%s", screen.name, claim, screen.text)
			}
		}
	}
}

// A giver whose account name cannot be read from the session still learns that the
// address changes and what it becomes. Its current address is not guessed.
func TestTransferOffer_AGiverWithNoAccountNameIsStillTold(t *testing.T) {
	out := runTransferVerb(t, &fakeTransferAPI{}, "", "", "offer", "shop", "--to", "bob")
	confirmation, _ := splitAtPrompt(t, out, offerPrompt)
	mustSay(t, "the offer's confirmation", confirmation, "address changes", "bob-shop", "30 days", "redirect", "stops")
	if strings.Contains(oneLine(confirmation), "//-shop") {
		t.Errorf("the confirmation names an address built from no account:\n%s", confirmation)
	}
}

// What the verbs do besides tell a person about the address is unchanged by what
// they say about it, and the harness above reaches the real verbs: the giver is
// shown the slice and the recipient by name and nothing is offered without a
// confirmation, the offer names the slice and the account, and an accept sends
// back the price it showed and prints the platform's own note.
func TestTransferVerbs_ControlTheHarnessReachesTheRealVerbs(t *testing.T) {
	declined := &fakeTransferAPI{}
	out := runTransferVerb(t, declined, "alice", "", "offer", "shop", "--to", "bob")
	confirmation, afterNo := splitAtPrompt(t, out, offerPrompt)
	mustSay(t, "the offer's confirmation", confirmation, "'shop'", "'bob'")
	mustSay(t, "the answer to an unconfirmed offer", afterNo, "nothing was offered")
	if got := declined.bodiesOf(t, "POST /ops/slice/transfer"); len(got) != 0 {
		t.Errorf("an offer nobody confirmed reached the platform: %v", got)
	}

	offering := &fakeTransferAPI{}
	runTransferVerb(t, offering, "alice", "y\nshop\n", "offer", "shop", "--to", "bob")
	offers := offering.bodiesOf(t, "POST /ops/slice/transfer")
	if len(offers) != 1 || offers[0]["slice"] != "shop" || offers[0]["to"] != "bob" {
		t.Errorf("a confirmed offer sent %v, want one offer of shop to bob", offers)
	}

	accepting := &fakeTransferAPI{note: platformAcceptNote}
	accepted := runTransferVerb(t, accepting, "bob", "y\n", "accept", "shop")
	accepts := accepting.bodiesOf(t, "POST /ops/slice/transfer/accept")
	if len(accepts) != 1 || accepts[0]["id"] != "0123456789abcdef0123456789abcdef" ||
		accepts[0]["acknowledge_monthly_cents"] != float64(900) {
		t.Errorf("an accept sent %v, want the offer's id and the monthly price it showed", accepts)
	}
	_, message := splitAtPrompt(t, accepted, acceptPrompt)
	mustSay(t, "the accept message", message, "the previous owner set this slice's secrets")
}
