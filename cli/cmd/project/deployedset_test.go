package project

import (
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// deployedSet is a slice's deployed functions as DeployedFunctions reads them,
// built from booking keys: `method:route` for an HTTP function or a worker, and a
// bare name for a queue handler.
func deployedSet(keys ...string) []atomic_cmd.DeployedFunction {
	out := make([]atomic_cmd.DeployedFunction, 0, len(keys))
	for _, k := range keys {
		method, path, found := strings.Cut(k, ":")
		if !found {
			out = append(out, atomic_cmd.DeployedFunction{Key: k, Name: k, Method: "queue", Digest: "d"})
			continue
		}
		out = append(out, atomic_cmd.DeployedFunction{
			Key: k, Name: path, Method: method, Digest: "d",
		})
	}
	return out
}
