// queues.go — the Driftfile's declared queues, made to exist wherever a
// function runs against a local Backbone.
//
// On a slice, `drift apply` creates every declared queue before any slot is
// deployed, so a consumer polling a declared queue that nothing has pushed
// to yet reads it empty. A queue nothing was ever pushed to and that nobody
// declared does not exist, and the slice answers 404. A local run has to
// answer the same way, or a Worker that polls its queue from its first tick
// works on a slice and fails on a laptop.
//
// There are two local Backbones, and each learns the declared queues its own
// way:
//
//   - `drift file run` runs a real slice in a container. The slice restores
//     every directory under `<BACKBONE_DATA_DIR>/queues/` as an existing, empty
//     queue at boot, so the CLI writes one directory per declared queue there
//     before the slice starts (QueueDirsArchive).
//   - `drift atomic run` runs the function with no BACKBONE_URL, so the SDK's
//     in-process store answers it. The SDK has no Driftfile, so the CLI hands
//     it the declared names in DeclaredQueuesEnvVar, and every SDK's local
//     store creates those queues before its first queue call.
//
// A package-level registry for the same reason as schedules.go: the paths that
// need it already read the Driftfile through FunctionSpecsInDir or ParseDriftfile.

package atomic_cmd

import (
	"archive/tar"
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DeclaredQueuesEnvVar names the declared queues, comma-separated, for an SDK's
// in-process local store. Every SDK reads it only when BACKBONE_URL is unset.
const DeclaredQueuesEnvVar = "DRIFT_DECLARED_QUEUES"

var (
	queuesMu       sync.RWMutex
	declaredQueues []string
)

// SetDeclaredQueues records the Driftfile's declared queue names. A nil or
// empty list clears it.
func SetDeclaredQueues(names []string) {
	queuesMu.Lock()
	defer queuesMu.Unlock()
	declaredQueues = normaliseQueueNames(names)
}

// DeclaredQueues returns the recorded names, sorted and without duplicates.
func DeclaredQueues() []string {
	queuesMu.RLock()
	defer queuesMu.RUnlock()
	return append([]string(nil), declaredQueues...)
}

// DeclaredQueuesEnv is the environment entry that hands the recorded names to
// an SDK's local store, or "" when none are declared.
func DeclaredQueuesEnv() string {
	names := DeclaredQueues()
	if len(names) == 0 {
		return ""
	}
	return DeclaredQueuesEnvVar + "=" + strings.Join(names, ",")
}

// QueueDirsArchive is a tar archive holding `queues/<name>/` for each name,
// owned by uid:gid, for extraction into a slice's BACKBONE_DATA_DIR. The slice
// restores each directory as an existing queue. An archive of directories
// alone adds to what a persisted data volume already holds and replaces none
// of its messages.
func QueueDirsArchive(names []string, uid, gid int) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	now := time.Now()
	dir := func(name string) error {
		return tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o750,
			Uid: uid, Gid: gid, ModTime: now,
		})
	}
	if err := dir("queues"); err != nil {
		return nil, err
	}
	for _, n := range normaliseQueueNames(names) {
		// The name becomes a path inside the container, so it must be one the
		// slice itself would accept: never `..`, never a slash.
		if !validQueueName(n) {
			return nil, fmt.Errorf("queue %q is not a valid queue name: it must start with a letter or digit "+
				"and hold only letters, digits, '.', '_' and '-'", n)
		}
		if err := dir("queues/" + n); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validQueueName is the slice's rule for a queue name: an alphanumeric first
// byte, then alphanumerics and `. _ -`, at most 255 bytes.
func validQueueName(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return false
		}
	}
	return true
}

// normaliseQueueNames trims, drops empties and duplicates, and sorts.
func normaliseQueueNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
