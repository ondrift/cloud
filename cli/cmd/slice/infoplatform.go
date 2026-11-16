package slice

// infoplatform.go — the part of `drift slice info` that says what the platform
// did to the slice and when it last looked.
//
// The scheduler and the shape describe the slice. This describes the platform's
// side: the actions it took on the slice in the last week, and, for each thing
// it keeps checking, when it last did and how often it should. The second half
// is what lets a quiet slice be believed: "nothing happened" and "the platform
// stopped looking" print differently.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// PlatformAction is one thing the platform did to the slice, as its sentence.
type PlatformAction struct {
	At        time.Time `json:"at"`
	Summary   string    `json:"summary"`
	Outcome   string    `json:"outcome"`
	Transient bool      `json:"transient"`
}

// PlatformCheck is one thing the platform keeps checking. Last is nil when the
// platform cannot say when it last ran.
type PlatformCheck struct {
	Label        string     `json:"label"`
	EverySeconds int        `json:"every_seconds"`
	Last         *time.Time `json:"last"`
}

// PlatformActions is the whole /ops/slice/actions document.
type PlatformActions struct {
	Actions []PlatformAction `json:"actions"`
	Checks  []PlatformCheck  `json:"checks"`
}

// platformWindowDays is the window the route answers for unless asked
// otherwise; the section names it so a quiet slice says how far back "quiet"
// reaches.
const platformWindowDays = 7

// overdueIntervals is how many intervals a check may be late before it is
// called overdue.
const overdueIntervals = 3

// FetchPlatformActions reads what the platform did to the active slice and when
// each of its checks last ran. A platform that does not serve the route yet
// answers 404, which is returned as an error that says so.
func FetchPlatformActions() (*PlatformActions, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/actions", nil)
	if err != nil {
		return nil, common.TransportError("read what the platform did to the slice", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read what the platform did to the slice")
	if err != nil {
		var apiErr *common.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, errors.New("this platform does not report it yet")
		}
		return nil, err
	}

	var pa PlatformActions
	if err := json.Unmarshal(body, &pa); err != nil {
		return nil, fmt.Errorf("the API response didn't look right (%w)", err)
	}
	return &pa, nil
}

// printPlatformSection prints the platform's section: one line per check, then
// the actions, newest first, or a sentence saying there were none. A section
// that could not be read says so with the reason and nothing else.
func printPlatformSection(w io.Writer, pa *PlatformActions, err error, now time.Time) {
	if err != nil {
		fmt.Fprintf(w, "\n  platform        couldn't be read: %v\n", err)
		return
	}
	fmt.Fprintf(w, "\n  platform\n")
	for _, c := range pa.Checks {
		fmt.Fprintf(w, "    %s\n", checkLine(c, now))
	}
	if len(pa.Actions) == 0 {
		fmt.Fprintf(w, "    nothing done to this slice in the last %d days\n", platformWindowDays)
		return
	}
	fmt.Fprintf(w, "    done to this slice in the last %d days\n", platformWindowDays)
	for _, a := range pa.Actions {
		fmt.Fprintf(w, "      %-8s %s\n", agoLabel(now.Sub(a.At))+" ago", a.Summary)
	}
}

// checkLine says when a check last ran and how often it should. A check with no
// time is unknown, never a time; one later than three intervals is overdue.
func checkLine(c PlatformCheck, now time.Time) string {
	every := time.Duration(c.EverySeconds) * time.Second
	if c.Last == nil {
		return fmt.Sprintf("%s: unknown, every %s", c.Label, agoLabel(every))
	}
	age := now.Sub(*c.Last)
	if every > 0 && age > overdueIntervals*every {
		return fmt.Sprintf("%s: overdue, last checked %s ago, every %s", c.Label, agoLabel(age), agoLabel(every))
	}
	return fmt.Sprintf("%s: checked %s ago, every %s", c.Label, agoLabel(age), agoLabel(every))
}

// agoLabel renders a span at one unit: seconds under a minute, minutes under an
// hour, hours under a day, then days. A negative span is zero.
func agoLabel(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}
