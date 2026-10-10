package atomic_cmd

import (
	"slices"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// Every language's function process carries the declared queues, so the SDK's
// in-process store creates them before the function's first queue call, as
// `drift apply` creates them on a slice.
func TestProcessEnv_CarriesTheDeclaredQueues(t *testing.T) {
	t.Cleanup(func() { atomic_cmd.SetDeclaredQueues(nil) })
	r := &devRunner{srcDir: t.TempDir(), port: 3000}

	atomic_cmd.SetDeclaredQueues([]string{"txs"})
	if env := r.processEnv(); !slices.Contains(env, atomic_cmd.DeclaredQueuesEnvVar+"=txs") {
		t.Errorf("the function's environment does not name the declared queue: %v", env)
	}

	atomic_cmd.SetDeclaredQueues(nil)
	for _, e := range r.processEnv() {
		if strings.HasPrefix(e, atomic_cmd.DeclaredQueuesEnvVar+"=") {
			t.Errorf("nothing is declared, yet the environment carries %q", e)
		}
	}
}
