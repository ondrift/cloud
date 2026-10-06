package atomic_cmd

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// The archive `drift file run` extracts into the slice's data directory: one
// directory per declared queue under queues/, owned by the user the slice runs
// as, and nothing else, so a persisted volume keeps the messages it holds.
func TestQueueDirsArchive_HoldsOneOwnedDirectoryPerDeclaredQueue(t *testing.T) {
	raw, err := QueueDirsArchive([]string{"txs", " orders.v2 ", "txs", ""}, 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeDir {
			t.Errorf("%s is not a directory: an archive of files would overwrite a persisted queue", h.Name)
		}
		if h.Uid != 1000 || h.Gid != 2000 {
			t.Errorf("%s is owned by %d:%d, want the slice's 1000:2000", h.Name, h.Uid, h.Gid)
		}
		names = append(names, h.Name)
	}
	if got := strings.Join(names, ","); got != "queues/,queues/orders.v2/,queues/txs/" {
		t.Errorf("the archive holds %s, want queues/ and one directory per distinct declared queue", got)
	}
}

// A name becomes a path in the container, so one the slice would refuse is
// refused here rather than written somewhere it should not be.
func TestQueueDirsArchive_RefusesANameTheSliceWouldRefuse(t *testing.T) {
	for _, bad := range []string{"../etc", "a/b", "-flag", ".hidden"} {
		if _, err := QueueDirsArchive([]string{bad}, 1000, 2000); err == nil {
			t.Errorf("%q was archived; it is not a queue name the slice accepts", bad)
		}
	}
}

// The SDK's local store learns the declared queues from one variable, and no
// declaration means no variable rather than an empty one.
func TestDeclaredQueuesEnv(t *testing.T) {
	t.Cleanup(func() { SetDeclaredQueues(nil) })
	SetDeclaredQueues([]string{"txs", "jobs", "txs"})
	if got := DeclaredQueuesEnv(); got != DeclaredQueuesEnvVar+"=jobs,txs" {
		t.Errorf("DeclaredQueuesEnv() = %q", got)
	}
	SetDeclaredQueues(nil)
	if got := DeclaredQueuesEnv(); got != "" {
		t.Errorf("with nothing declared, DeclaredQueuesEnv() = %q, want empty", got)
	}
}
