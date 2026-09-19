package portal

import "testing"

// downloadSnapshotForm shells out to the real `drift slice snapshot
// download` (via suspendAndRun) rather than streaming the archive
// in-process, because that command is the only place the step-up handshake
// a snapshot download always demands is implemented — an in-process request
// had no way to ask for the account password at all, so every attempt
// through the portal failed outright (CNV-45).
//
// snapshotDownloadArgs is the one piece of that worth pinning without
// spawning a process: the exact argv, and specifically that -o names the
// caller's chosen file rather than, say, being dropped or misordered.
func TestSnapshotDownloadArgs_NamesTheOutputFile(t *testing.T) {
	got := snapshotDownloadArgs("snap-123", "backup.tar.gz")
	want := []string{"slice", "snapshot", "download", "snap-123", "-o", "backup.tar.gz"}

	if len(got) != len(want) {
		t.Fatalf("snapshotDownloadArgs(...) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
