// slicehost.go — the address a slice answers at. One implementation, read by
// `drift apply` for the address it prints, by the dashboard for the slice it
// opens, and by `drift slice transfer` for the address a slice leaves and the one
// it takes.
package common

import "strings"

// SliceHost is the default address of the slice `slice` when the account
// `account` controls it, without a scheme: `<account>-<slice>.<root>`.
//
// <root> is the API's own host without its leading `api.` label, so the API at
// api.ondrift.eu gives slices at <account>-<slice>.ondrift.eu, and a local API at
// http://api.localhost:30036 gives <account>-<slice>.localhost:30036. Every slice
// is reached this way, including one named "default".
//
// The address is named after the account that controls the slice, so a slice
// that changes hands changes address; the platform decides when, and this
// function only spells the result for an account and a slice name the caller
// already holds.
func SliceHost(account, slice string) string {
	root := strings.TrimPrefix(strings.TrimPrefix(APIBaseURL, "https://"), "http://")
	root = strings.TrimPrefix(root, "api.")
	return account + "-" + slice + "." + root
}

// SliceURL is SliceHost with the scheme the API itself is reached on.
func SliceURL(account, slice string) string {
	scheme := "http://"
	if strings.HasPrefix(APIBaseURL, "https://") {
		scheme = "https://"
	}
	return scheme + SliceHost(account, slice)
}
