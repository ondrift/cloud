package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// requireDriftfile resolves the Driftfile in the working directory and reports a
// refusal that names the command which writes one.
//
// # Why this is a function and not one error string per command
//
// `plan`, `apply`, `size`, `benchmark`, `run` and `test` all need the Driftfile,
// and one message keeps them agreeing. In a fresh directory a new account lands
// here, which makes this string the first wall of the product: it names the
// command that writes a Driftfile, because somebody who has never seen one
// cannot act on "not found".
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
