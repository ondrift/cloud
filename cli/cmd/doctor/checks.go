package doctor

// checks.go — the checklist, in the order its lines print.

import (
	"context"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// Checklist is every check the checkup runs, in order. A later check is a new
// entry here, and the order is the order of the lines.
func Checklist() []Check {
	return []Check{platformCheck()}
}

// platformCheck asks the platform's own status feed, the one read the 5xx note,
// the refused-connection note and the dashboard's popup also make, so the checkup
// and the automatic path cannot disagree about which component is down.
func platformCheck() Check {
	return Check{
		Name: "platform",
		Run: func(ctx context.Context) Result {
			return platformResult(common.ReadStatusFeed(ctx))
		},
		Possible: func() []Result {
			var all []Result
			for _, feed := range []common.StatusFeed{
				{Read: true, Rows: []common.StatusRow{{Name: "API", Status: "operational"}}},
				{Read: true, Rows: []common.StatusRow{{Name: "Routing", Status: "degraded"}}},
				{Read: true, Rows: []common.StatusRow{{Name: "Routing", Status: "down"}, {Name: "Storage", Status: "degraded"}}},
				{Read: true},
				{Failure: common.FeedRefused, Reason: "the connection was refused"},
				{Failure: common.FeedTimedOut, Reason: "it did not answer in time"},
				{Failure: common.FeedUnreadable, Reason: "it answered 404"},
			} {
				all = append(all, platformResult(feed))
			}
			return all
		},
	}
}

// platformResult is what a read of the feed means. A component that is degraded or
// down is a problem under the registry's code for it; an API that nothing
// answered or that did not answer in time is a problem under the codes for those;
// a feed that answered something unreadable is unknown, because nothing was found
// either way.
func platformResult(feed common.StatusFeed) Result {
	if !feed.Read {
		switch feed.Failure {
		case common.FeedRefused:
			return Result{Status: StatusProblem, Code: "DRIFT-1006",
				Sentence: "the Drift API did not accept a connection, so the platform's status could not be read"}
		case common.FeedTimedOut:
			return Result{Status: StatusProblem, Code: "DRIFT-1007",
				Sentence: "the Drift API did not answer in time, so the platform's status could not be read"}
		default:
			return Result{Status: StatusUnknown, Sentence: "the platform's status could not be read", Reason: feed.Reason}
		}
	}
	if len(feed.Rows) == 0 {
		return Result{Status: StatusUnknown, Sentence: "the platform's status could not be read",
			Reason: "the status feed listed no components"}
	}

	var notOperational []string
	for _, r := range feed.Rows {
		if !r.Operational() {
			notOperational = append(notOperational, r.Name+" is "+r.Status)
		}
	}
	if len(notOperational) == 0 {
		return Result{Status: StatusOK, Sentence: "every component is operational"}
	}
	return Result{Status: StatusProblem, Code: "DRIFT-1022", Sentence: strings.Join(notOperational, ", ")}
}
