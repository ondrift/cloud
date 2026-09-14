package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// requireDriftfile resolves the Driftfile in the working directory and reports a
// refusal that names the command which writes one.
//
// # Why this is a function and not six error strings
//
// It was six. `apply`, `simulate`, `benchmark`, `run`, `test` and `diff` each
// carried `fmt.Errorf("no Driftfile in the current directory (looked for %s)")`,
// so improving the message meant finding all six — and the one that mattered
// most was the one nobody would think to look at.
//
// THE ONE THAT MATTERS: "Deploy your app :: drift file apply" is the second line
// a brand-new account is told to type. In a fresh directory it lands exactly
// here, which makes this string the first wall of the product. It named the file
// it could not find and stopped, and somebody who has never seen a Driftfile
// cannot act on that — the next command exists, and the message did not say it.
func requireDriftfile() (string, error) {
	path, err := filepath.Abs(filepath.Join(".", driftfileName))
	if err != nil {
		return "", fmt.Errorf("resolve manifest path: %w", err)
	}
	if _, serr := os.Stat(path); serr != nil {
		return "", fmt.Errorf("no Driftfile in the current directory (looked for %s)\n"+
			"  run `drift file new` to write one — it fills in the knobs that are required in practice", path)
	}
	return path, nil
}
