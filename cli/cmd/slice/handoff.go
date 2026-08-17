// Package lifecycle — reading back what the configurator made.
//
// The handoff flow itself lives in `common` (common/handoff.go), because it is
// not this command's property: the configurator owns a slice's shape, so every
// verb that would once have written one hands off there — including
// `drift file benchmark`, which sits in `cmd/project` and cannot import this
// package, since this one imports it.
//
// What stays here is what only a `drift slice` verb does with the result: read
// the name the form may have collected, and print the confirmation line.
//
// Both create and resize also take a non-interactive route: --free on create,
// and --from <Driftfile> on resize (see resize.go), which is the only way to
// drive one from a shell with no browser (CI, tests, scripts piped through ssh).
// It is intentionally less ergonomic than the browser flow — the user
// hand-authors the target shape.
package slice

import (
	"encoding/json"
	"fmt"
)

// sliceNameFrom reads the name out of the api's Slice document. The create
// form may collect the name itself, so this is the only place the CLI learns
// what the slice ended up being called.
func sliceNameFrom(raw json.RawMessage) string {
	var s struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s.Name
}

// printSliceSummary prints a one-line confirmation for the user.
func printSliceSummary(verb string, raw json.RawMessage) {
	var s struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.Name == "" {
		fmt.Printf("Slice %s.\n", verb)
		return
	}
	label := "configured"
	if s.Tier == "hacker" {
		label = "free"
	}
	fmt.Printf("Slice '%s' %s (%s).\n", s.Name, verb, label)
}
