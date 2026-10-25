package slice

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// `slice create` still works, because the dashboard, scripts and the external
// test suites call it, and it says once what to do instead: a Driftfile
// declares the slice and `drift apply` creates it.
func TestSliceCreate_SaysItIsDeprecatedAndPointsAtTheFile(t *testing.T) {
	common.ResetDeprecationState()
	var warned bytes.Buffer
	defer common.RedirectDeprecationWarnings(&warned)()

	cmd := getCreateCmd()
	if cmd.PersistentPreRunE == nil {
		t.Fatal("`drift slice create` carries no deprecation notice")
	}
	if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"drift slice create", "is deprecated", "drift file new", "drift apply"} {
		if !strings.Contains(warned.String(), want) {
			t.Errorf("the notice does not say %q: %q", want, warned.String())
		}
	}
}

// `shrink` was a deprecated spelling of `resize` and is gone; `resize` is the one
// verb.
func TestSlice_ShrinkIsGone(t *testing.T) {
	for _, c := range GetCmd().Commands() {
		if c.Name() == "shrink" {
			t.Error("`drift slice shrink` is still registered")
		}
	}
}
