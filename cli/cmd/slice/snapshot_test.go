package slice

import (
	"encoding/json"
	"strings"
	"testing"
)

// The bug: the operator's restore response has carried a per-component
// `notes` field — work the restore could not do itself, like an interpreted
// function whose dependencies are deliberately not in the archive — since
// restoreSummary.Notes shipped, but this CLI's decode struct had no field
// for it. Decoding silently drops an unknown JSON key, so the note the
// operator computed was never printed anywhere (SNP-40).
func TestSnapshotRestoreResult_DecodesTheOperatorsNotes(t *testing.T) {
	raw := `{
		"restored": {
			"secrets": 3,
			"functions": 1,
			"notes": [
				"atomic: the default element is python and its dependencies (requirements.txt) are not in the archive by design — if this slice has not already deployed it, run ` + "`drift project deploy`" + ` from the restored source to install them, or its functions will fail at first invocation"
			]
		},
		"errors": []
	}`

	var result snapshotRestoreResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(result.Restored.Notes) != 1 {
		t.Fatalf("want 1 note decoded, got %d", len(result.Restored.Notes))
	}
	if want := "run `drift project deploy`"; !strings.Contains(result.Restored.Notes[0], want) {
		t.Errorf("the decoded note does not contain %q: %q", want, result.Restored.Notes[0])
	}
}

// The control: a restore with no notes decodes to none, not to a
// slice holding one empty string.
func TestSnapshotRestoreResult_NoNotesWhenTheOperatorSendsNone(t *testing.T) {
	raw := `{"restored": {"secrets": 3}, "errors": []}`

	var result snapshotRestoreResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(result.Restored.Notes) != 0 {
		t.Errorf("want no notes, got %v", result.Restored.Notes)
	}
}
