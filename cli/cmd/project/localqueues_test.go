package project

import (
	"path/filepath"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// On a slice, `drift apply` creates every declared queue before any slot runs,
// so a consumer polling one that nothing has pushed to reads it empty.
// `drift atomic run` reads the Driftfile through FunctionSpecsInDir, and that
// read has to publish the declared queues for the SDK's local store, or the
// same consumer fails on a laptop.
func TestFunctionSpecsInDir_PublishesTheDeclaredQueues(t *testing.T) {
	withRealSchema(t)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "atomic"))
	mustWrite(t, filepath.Join(root, "atomic", "main.go"),
		"package main\n\nimport \"context\"\n\nfunc Sequencer(ctx context.Context) error { return nil }\n")
	mustWrite(t, filepath.Join(root, "Driftfile"), "slice \"demo\" { billing = \"monthly\" }\n"+
		"atomic {\n  element \"default\" {\n    field \"sequencer\" {\n      handler = \"Sequencer\"\n      memory = \"64MiB\"\n    }\n  }\n}\n"+
		"backbone {\n  queue \"txs\" {}\n  queue \"blocks\" {}\n}\n")
	t.Cleanup(func() { atomic_cmd.SetDeclaredQueues(nil) })

	if _, err := FunctionSpecsInDir(filepath.Join(root, "atomic")); err != nil {
		t.Fatal(err)
	}
	if got := atomic_cmd.DeclaredQueuesEnv(); got != atomic_cmd.DeclaredQueuesEnvVar+"=blocks,txs" {
		t.Errorf("the local run would hand the SDK %q, want both declared queues", got)
	}
}

// The data directory and the owner come from the runner image, read as
// `docker image inspect` prints them.
func TestParseImageDataDirAndUser(t *testing.T) {
	dir, uid, gid, err := parseImageDataDirAndUser(
		`["PATH=/usr/bin","BACKBONE_DATA_DIR=/data","RUNNER_DIR=/var/atomic"] "1000:2000"` + "\n")
	if err != nil || dir != "/data" || uid != 1000 || gid != 2000 {
		t.Errorf("got (%q, %d, %d, %v), want (/data, 1000, 2000, nil)", dir, uid, gid, err)
	}
	if _, _, _, err := parseImageDataDirAndUser(`["PATH=/usr/bin"] "1000"`); err == nil {
		t.Error("an image with no BACKBONE_DATA_DIR must be refused, not seeded somewhere guessed")
	}
	if _, _, _, err := parseImageDataDirAndUser(`["BACKBONE_DATA_DIR=/data"] "slice"`); err == nil {
		t.Error("a named user cannot own a tar entry, so it must be refused")
	}
}

// The container is created rather than run, so the queues are in place before
// the slice boots and restores them.
func TestLocalRunArgs_CreatesTheContainerWithoutStartingIt(t *testing.T) {
	args := localRunArgs(manifestFrom(Node{"slice": "lab"}), "drift-lab", "drift-run-lab", 8002, false, "")
	if args[0] != "create" || containsArg(args, "-d") {
		t.Errorf("the container must be created and started later, got %v", args)
	}
}
