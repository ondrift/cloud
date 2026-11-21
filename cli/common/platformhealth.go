package common

// platformhealth.go — answering "is this my fault?" before the user assumes it is.
//
// A 5xx or an unreachable API reads as your own code being wrong, and the user's
// next move is to go looking for a bug they did not write. The platform already
// publishes per-component health for the status page; consulting it at the moment
// of failure turns "something went wrong" into "Routing is degraded — this is not
// your code", which is the difference between a wasted afternoon and a coffee.
//
// # Only on failure, and never at the cost of the failure
//
// This runs on 5xx and on unreachable, and nowhere else: the happy path pays
// nothing. The check is best-effort with a short timeout, and every way it can go
// wrong ends in silence — an unreachable status page during an outage is the
// LIKELY case, and a diagnostic that fails loudly while diagnosing a failure has
// made the screen worse rather than better.
//
// The health comes from the api's own status feed, the one status.ondrift.eu
// shows. The api composes it from its downstreams' readiness, so a 5xx from an
// unwell service behind the api is named by it. When the api itself does not
// answer, neither does the feed, and that ends in silence like every other
// failure here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// StatusURL overrides where per-component health is read. Empty means the api's
// feed, from StatusFeedURL. A variable so a test can point it at a stub; there is
// no reason for a user to change it.
var StatusURL = ""

// StatusFeedURL is the api's public status feed, GET /ops/status: one row per
// capability, the rows status.ondrift.eu shows. It follows APIBaseURL, so a local
// build and DRIFT_API_URL read their own platform's.
func StatusFeedURL() string {
	if StatusURL != "" {
		return StatusURL
	}
	return strings.TrimRight(APIBaseURL, "/") + "/ops/status"
}

// StatusTimeout is how long one read of the feed waits. It is short on purpose:
// the user is already waiting on a failed command, and a slow answer here is worse
// than none. A variable so a test can shorten it; there is no reason for a user to
// change it.
var StatusTimeout = 3 * time.Second

// ComponentOperational is the state of a capability that is working.
const ComponentOperational = "operational"

// StatusRow is one capability of the platform and the state it reports:
// operational, degraded or down. A state the feed words another way is kept as it
// came.
type StatusRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Operational reports whether the row says the capability is working.
func (r StatusRow) Operational() bool {
	return strings.EqualFold(r.Status, ComponentOperational)
}

// FeedFailure says why a read of the feed gave no rows.
type FeedFailure int

const (
	// FeedUnreadable is a feed that answered with something that is not rows: a
	// status other than 200, a body that is not the list, or a failure that is
	// neither a refusal nor a timeout.
	FeedUnreadable FeedFailure = iota
	// FeedRefused is a connection nothing accepted: the API is down, or the
	// address does not resolve.
	FeedRefused
	// FeedTimedOut is a feed that was reached and did not answer in time.
	FeedTimedOut
)

// StatusFeed is what one read of the api's status feed found.
type StatusFeed struct {
	// Rows is every capability the feed listed, operational or not, with the
	// state it reported. Empty unless Read.
	Rows []StatusRow
	// Read is whether the feed could be read at all. A feed that listed nothing
	// was read; one that could not be reached or understood was not.
	Read bool
	// Failure and Reason say why Read is false. Reason is one clause a person can
	// read after "could not read the status feed:".
	Failure FeedFailure
	Reason  string
}

// Degraded returns the names of the rows that are not operational, in the order
// the feed listed them.
func (f StatusFeed) Degraded() []string {
	var names []string
	for _, r := range f.Rows {
		if !r.Operational() {
			names = append(names, r.Name)
		}
	}
	return names
}

// ReadStatusFeed reads the api's status feed once, for every caller that wants
// it: the note beside a 5xx, the note beside a refused connection, the
// dashboard's status popup and `drift doctor`. It is the one place the feed's
// address is built and its rows are decoded, so those callers cannot disagree
// about what the platform said.
//
// It is bounded by StatusTimeout and by ctx, whichever ends first, and it never
// returns an error to raise: a feed that could not be read is a StatusFeed with
// Read false and the reason, because what to do about that differs by caller and
// the only wrong answer is to fail loudly while diagnosing a failure. A row with
// no name is dropped, since it names nothing.
func ReadStatusFeed(ctx context.Context) StatusFeed {
	ctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, StatusFeedURL(), nil)
	if err != nil {
		return StatusFeed{Failure: FeedUnreadable, Reason: "the status feed's address is not usable: " + err.Error()}
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- URL derived from the configured API base
	if err != nil {
		return feedTransportFailure(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return StatusFeed{Failure: FeedUnreadable, Reason: fmt.Sprintf("it answered %d", resp.StatusCode)}
	}

	var rows []StatusRow
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rows); err != nil {
		if ctx.Err() != nil {
			return feedTransportFailure(ctx.Err())
		}
		return StatusFeed{Failure: FeedUnreadable, Reason: "it answered something that is not a list of components"}
	}
	feed := StatusFeed{Read: true}
	for _, r := range rows {
		if r.Name != "" {
			feed.Rows = append(feed.Rows, r)
		}
	}
	return feed
}

// feedTransportFailure classifies a request that never got an answer.
func feedTransportFailure(err error) StatusFeed {
	var netErr net.Error
	switch msg := err.Error(); {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return StatusFeed{Failure: FeedTimedOut, Reason: "it did not answer in time"}
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") || strings.Contains(msg, "dial tcp"):
		return StatusFeed{Failure: FeedRefused, Reason: "the connection was refused"}
	default:
		return StatusFeed{Failure: FeedUnreadable, Reason: err.Error()}
	}
}

// DegradedComponents returns the names of components that are not operational.
//
// Empty means either "everything is fine" or "we could not tell", and callers
// must treat those the same: the only safe use of this is to ADD information
// when it is available, never to claim health it did not confirm. A caller that
// has to tell the two apart reads the feed itself with ReadStatusFeed.
func DegradedComponents() []string {
	return ReadStatusFeed(context.Background()).Degraded()
}

// platformFaultNote renders the line that tells a user this is not theirs, or ""
// when nothing can be said.
//
// "" is the common case and the important one. A component list that came back
// clean means the platform believes it is healthy, and saying so beside a 5xx
// would be asserting something this cannot know — the failure is real whatever
// the status page thinks. Silence leaves the original message intact.
func platformFaultNote(degraded []string) string {
	if len(degraded) == 0 {
		return ""
	}
	if len(degraded) == 1 {
		return "  " + degraded[0] + " is not operational right now — this is the platform, not your code."
	}
	return "  " + strings.Join(degraded, ", ") + " are not operational right now — this is the platform, not your code."
}
