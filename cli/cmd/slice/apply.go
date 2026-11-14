package slice

// apply.go: what `drift apply` asks of a slice's shape: make one from a
// Driftfile's compiled shape, or bring one to it.
//
// Both go through the same endpoints the form and `resize --config` use, so the
// platform asks a Driftfile the questions it asks every caller: a resize that
// moves the price is refused until the figure is sent back, and one that takes
// something away until the slice is named. `drift apply` collects both answers
// before it sends anything, from the plan the person agreed to, so a refusal
// here means the slice moved between the plan and the apply.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// CreateWithShape creates a slice from a compiled shape, and waits for it to
// answer. A free slice takes the free preset's limits and the names the file
// declares, none of the preset's placeholders for what it leaves out: the shape
// goes with the request, marked as the file's whole declaration, so the slice
// the platform stores is the one the next plan compares against. Any other is
// created with the shape and billed monthly.
func CreateWithShape(name string, shape any, free bool) error {
	payload := map[string]any{"name": name}
	if free {
		payload["tier"] = "hacker"
		payload["config"] = shape
		payload["declared"] = true
	} else {
		payload["tier"] = "custom"
		payload["config"] = shape
		payload["billing_period_months"] = 1
		payload["payment_token"] = "cli"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice", bytes.NewBuffer(body))
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()
	if _, err := common.CheckResponse(resp, "create slice"); err != nil {
		return err
	}
	if werr := common.WaitForSliceReady(name); werr != nil {
		return fmt.Errorf("slice %q was created and is still starting: %w", name, werr)
	}
	return nil
}

// ResizeRefused is a resize the platform refused, with what it asked.
type ResizeRefused struct {
	// AskedCents is the figure the platform asks to have agreed to, when it
	// asks the price question; -1 when it does not.
	AskedCents int
	// Destroys lists what the resize would take away, when it asks the
	// removal question.
	Destroys []string
	// Violations are limits below what the slice already uses.
	Violations []string
	// Message is the platform's own words, for any other refusal.
	Message string
}

func (r *ResizeRefused) Error() string {
	var parts []string
	if r.AskedCents >= 0 {
		parts = append(parts, fmt.Sprintf("the platform prices this shape at %s a month", euros(r.AskedCents)))
	}
	if len(r.Destroys) > 0 {
		parts = append(parts, "it takes away: "+strings.Join(r.Destroys, "; "))
	}
	if len(r.Violations) > 0 {
		parts = append(parts, "it is smaller than what the slice holds: "+strings.Join(r.Violations, "; "))
	}
	if r.Message != "" {
		parts = append(parts, r.Message)
	}
	if len(parts) == 0 {
		return "the platform refused the resize and said nothing about why"
	}
	return "the platform refused the resize: " + strings.Join(parts, "; ")
}

// ResizeTo resizes a slice to a compiled shape. ackCents is the monthly
// figure agreed to, or -1 when none was; confirm is the slice's name when its
// removals were agreed to, or "". Neither is sent unless given, so nothing is
// agreed to in advance.
func ResizeTo(name string, shape any, ackCents int, confirm string) error {
	payload := map[string]any{
		"name":                  name,
		"config":                shape,
		"billing_period_months": 1,
		"payment_token":         "cli",
	}
	if ackCents >= 0 {
		payload["acknowledge_monthly_cents"] = ackCents
	}
	if confirm != "" {
		payload["confirm_slice_name"] = confirm
	}
	ok, refusal, err := postResize(payload)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	out := &ResizeRefused{AskedCents: -1}
	if refusal == nil {
		return out
	}
	if refusal.asksPrice() {
		out.AskedCents = refusal.PriceChange.NewMonthlyCents
	}
	if refusal.asksRemoval() {
		for _, d := range refusal.ConfirmationRequired.Destroys {
			out.Destroys = append(out.Destroys, describeDestroy(d))
		}
		sort.Strings(out.Destroys)
	}
	for _, v := range refusal.Violations {
		out.Violations = append(out.Violations, fmt.Sprintf("%s holds %d, the shape allows %d", v.Resource, v.Used, v.NewLimit))
	}
	if !refusal.asksPrice() && !refusal.asksRemoval() && len(refusal.Violations) == 0 {
		out.Message = refusal.Error
	}
	return out
}
