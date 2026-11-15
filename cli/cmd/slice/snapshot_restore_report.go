// snapshot_restore_report.go holds what `drift slice snapshot restore` prints once
// the platform has answered a restore.

package slice

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// restoreAnswer is the platform's answer to a restore: what came back, the work
// that is still the caller's, and what did not come back.
type restoreAnswer struct {
	Restored struct {
		Secrets          int `json:"secrets"`
		NoSQLCollections int `json:"nosql_collections"`
		Blobs            int `json:"blobs"`
		Queues           int `json:"queues"`
		SQLDatabases     int `json:"sql_databases"`
		Functions        int `json:"functions"`
		Canvas           int `json:"canvas"`
		VaultEntries     int `json:"vault_entries"`
		LinkIdentities   int `json:"link_identities"`
		PocketItems      int `json:"pocket_items"`
		// Notes is work the restore could not do and the caller has to finish,
		// such as installing the dependencies of an interpreted backend, which the
		// archive does not hold. A note is not a failure: everything the archive
		// holds landed, so a restore with notes is a success.
		Notes []string `json:"notes"`
	} `json:"restored"`
	// Errors has one entry for each component that did not come back.
	Errors []string `json:"errors"`
}

// printRestoreReport writes the report of a restore to w, from the platform's
// answer in body, and returns the error the command ends with.
//
// The report is the headline and the counts, then the notes under Next: and the
// errors under Errors, each only when the answer has some. An answer that lists
// errors ends the command with the "restore incomplete" error, so that a script
// that restores and carries on cannot mistake a partial restore for a whole one.
// Notes do not change the outcome: they are what is left to do about a restore
// that worked.
func printRestoreReport(w io.Writer, id string, body []byte) error {
	var result restoreAnswer
	// A body that does not decode is reported as an answer with nothing in it.
	json.Unmarshal(body, &result) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

	// The headline reflects the RESULT, not the HTTP status: a partial restore is
	// still a 200, with the per-component errors in the body, and a green check
	// above a list of components that did not come back is the most misleading
	// thing this command could say to someone whose snapshot is the only copy left.
	if len(result.Errors) > 0 {
		fmt.Fprintf(w, "%s Snapshot %s restored WITH ERRORS — %d component(s) did not come back.\n",
			common.Cross(), id, len(result.Errors))
	} else {
		fmt.Fprintf(w, "%s Snapshot %s restored.\n", common.Check(), id)
	}
	fmt.Fprintln(w)
	r := result.Restored
	fmt.Fprintf(w, "  Secrets:     %d\n", r.Secrets)
	fmt.Fprintf(w, "  NoSQL:       %d collections\n", r.NoSQLCollections)
	fmt.Fprintf(w, "  Blobs:       %d\n", r.Blobs)
	fmt.Fprintf(w, "  Queues:      %d\n", r.Queues)
	fmt.Fprintf(w, "  SQL:         %d databases\n", r.SQLDatabases)
	fmt.Fprintf(w, "  Vault:       %d entries\n", r.VaultEntries)
	fmt.Fprintf(w, "  Link:        %d identities\n", r.LinkIdentities)
	fmt.Fprintf(w, "  Pocket:      %d items\n", r.PocketItems)
	fmt.Fprintf(w, "  Functions:   %d\n", r.Functions)
	fmt.Fprintf(w, "  Canvas:      %d sites\n", r.Canvas)

	// The notes follow the counts and come before the errors: they are what is left
	// to do about a restore that worked, and the errors are what did not.
	if notes := restoreNotes(r.Notes); len(notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Next:")
		for _, n := range notes {
			fmt.Fprintf(w, "    - %s\n", n)
		}
	}

	if len(result.Errors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Errors (%d):\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(w, "    - %s\n", e)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  A function listed above is NOT running. Check `restorable` in the")
		fmt.Fprintln(w, "  snapshot's atomic-manifest.json — a function deployed before its entry")
		fmt.Fprintln(w, "  point was recorded cannot be rebuilt from the archive, and redeploying")
		fmt.Fprintln(w, "  it from source is the fix.")
		return fmt.Errorf("restore incomplete: %d component(s) failed", len(result.Errors))
	}

	return nil
}

// restoreNotes returns the notes worth printing, trimmed. A blank note says
// nothing, and a Next: heading over nothing would read as work to do.
func restoreNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
