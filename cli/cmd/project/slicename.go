package project

// slicename.go — the slice a Driftfile declares, for the commands that do not
// read the file themselves.
//
// `drift atomic list`, `drift backbone secret set` and the rest act on one slice,
// and inside a project that slice is the one the Driftfile names. This reads only
// the `slice "<name>"` block's label, from the syntax tree and without
// evaluating anything: a file whose other attributes read variables or
// `env("…")` still names its slice, and a command that never needed those values
// is not refused for lacking them.

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/ondrift/cloud/cli/common"
)

func init() {
	common.DriftfileSlice = driftfileSlice
}

func driftfileSlice(dir string) (name, path string, found bool, err error) {
	path, ferr := FindDriftfileFor(dir)
	if ferr != nil {
		return "", "", false, nil
	}
	src, rerr := os.ReadFile(path) // #nosec G304 -- the user's own Driftfile
	if rerr != nil {
		return "", path, true, rerr
	}
	name, err = sliceLabel(path, src)
	return name, path, true, err
}

// sliceLabel is the name of the one `slice` block a Driftfile declares.
func sliceLabel(path string, src []byte) (string, error) {
	file, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return "", fmt.Errorf("it does not parse: %s", diags.Errs()[0])
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return "", fmt.Errorf("it is not a Driftfile")
	}
	var names []string
	for _, b := range body.Blocks {
		if b.Type == "slice" && len(b.Labels) == 1 {
			names = append(names, b.Labels[0])
		}
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("it has no slice block")
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("it declares %d slice blocks", len(names))
}
