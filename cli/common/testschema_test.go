package common

import (
	"encoding/json"
	"os"
	"testing"
)

// The schema the CLI's tests run against is committed (testdata/driftfile.schema.json)
// so that a pipeline never depends on what alpha has deployed. It must therefore
// move with the CLI: a bump of ImplementedDriftfileFormat that did not refresh it
// would test the new compiler against the old format.
func TestTheTestSchemaIsAtLeastTheFormatTheCLIImplements(t *testing.T) {
	raw, err := os.ReadFile("../testdata/driftfile.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the committed schema does not parse: %v", err)
	}
	if doc.Version == "" {
		t.Fatal("the committed schema states no version")
	}
	if DriftfileMajor(doc.Version) != DriftfileMajor(ImplementedDriftfileFormat) ||
		driftfileMinor(doc.Version) < driftfileMinor(ImplementedDriftfileFormat) {
		t.Errorf("cli/testdata/driftfile.schema.json is format %s and the CLI implements %s: copy the platform's "+
			"src/common/driftfile/driftfile.schema.json over it", doc.Version, ImplementedDriftfileFormat)
	}
}
