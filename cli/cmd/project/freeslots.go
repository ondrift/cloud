package project

// freeslots.go — which of the account's free slices a new slice would take.
//
// The Driftfile does not say how a slice is paid for. A shape inside the free
// grant can be one of the account's free slices, and the platform says how many
// an account may hold and which it holds (GET /ops/slice/free). The number is the
// platform's and is read at the moment it is needed, never kept here: raising it
// there changes the question this asks, and nothing in the CLI.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// FreeSliceRef is one free slice an account holds.
type FreeSliceRef struct {
	Name string `json:"name"`
	Slot int    `json:"slot"`
	// Failed is a free slice that never provisioned: the one to delete first.
	Failed bool `json:"failed,omitempty"`
}

// FreeSlots is the account's free slices and how many it may hold.
type FreeSlots struct {
	Limit int            `json:"limit"`
	Held  []FreeSliceRef `json:"slices"`
}

// Open is how many more free slices the account may create.
func (f FreeSlots) Open() int {
	if open := f.Limit - len(f.Held); open > 0 {
		return open
	}
	return 0
}

// Full is whether the account holds every free slice it may.
func (f FreeSlots) Full() bool { return f.Limit > 0 && f.Open() == 0 }

// fetchFreeSlots is a variable so a test can answer it without a platform.
var fetchFreeSlots = FetchFreeSlots

// FetchFreeSlots reads the account's free slices.
func FetchFreeSlots() (FreeSlots, error) {
	resp, err := common.DoJSONRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/free", nil)
	if err != nil {
		return FreeSlots{}, common.TransportError("read the free slices", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "read the free slices")
	if err != nil {
		return FreeSlots{}, err
	}
	var f FreeSlots
	if err := json.Unmarshal(body, &f); err != nil {
		return FreeSlots{}, fmt.Errorf("read the free slices: the platform sent a reply we couldn't read")
	}
	return f, nil
}

// ordinal says 1 as "1st": the Nth free slice an account would hold.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// freeQuestion is the one question a new slice that fits the free grant puts to
// a person, in the platform's numbers.
func (f FreeSlots) freeQuestion() string {
	return fmt.Sprintf("You have %d of %d free slices — make this the %s?", len(f.Held), f.Limit, ordinal(len(f.Held)+1))
}

// fullRefusal names the free slices an account holds when it holds all it may,
// and the ways forward. It never offers to replace one: emptying a slice is the
// person's act, made on purpose, not a consequence of applying a file.
func (f FreeSlots) fullRefusal() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the free tier is %d slice%s per account and you hold all %d:\n", f.Limit, pluralS(f.Limit), len(f.Held))
	for _, s := range f.Held {
		fmt.Fprintf(&b, "    %s", s.Name)
		if s.Failed {
			fmt.Fprintf(&b, "   (never provisioned: `drift slice delete %s` gives the slot back)", s.Name)
		}
		b.WriteByte('\n')
	}
	b.WriteString("  Empty one first (delete it, or give it a shape that is priced). Idle cooldown lowers what an idle " +
		"free slice costs, but it does not free its slot.\n" +
		"  Or make this slice a priced one by agreeing to its price: --approve-price <cents>, as `drift plan` quotes it.")
	return b.String()
}

// errPricedInstead is settle's answer when a person declined the free slice: the
// plan is read again as a priced slice and the price is asked.
var errPricedInstead = errors.New("the person chose a priced slice instead of a free one")
