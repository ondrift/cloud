package project

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// liveWith builds the live slice document the check reads: the four named sets
// the slice's config actually carries.
func liveWith(functions []string, collections, buckets, databases []string) *LiveSlice {
	l := &LiveSlice{}
	for _, f := range functions {
		l.Config.Atomic.Functions = append(l.Config.Atomic.Functions,
			AtomicFunction{Name: f, MemoryBytes: 16 << 20})
	}
	if len(collections) > 0 {
		l.Config.Backbone.NoSQL.Collections = map[string]int{}
		for _, c := range collections {
			l.Config.Backbone.NoSQL.Collections[c] = 5 << 20
		}
	}
	if len(buckets) > 0 {
		l.Config.Backbone.Blobs.Buckets = map[string]int{}
		for _, b := range buckets {
			l.Config.Backbone.Blobs.Buckets[b] = 5 << 20
		}
	}
	if len(databases) > 0 {
		l.Config.Backbone.SQL.Databases = map[string]int{}
		for _, d := range databases {
			l.Config.Backbone.SQL.Databases[d] = 5 << 20
		}
	}
	return l
}

func manifestNaming(functions []string, collections, buckets, databases []string) *Manifest {
	fns := make([]any, 0, len(functions))
	for _, f := range functions {
		fns = append(fns, map[string]any{"name": f, "handler": "H", "memory": "16MB"})
	}
	// A NoSQL entry names the `slot` it seeds; a bucket and a database still
	// carry `name`. These are resolved nodes, so each is written in the spelling
	// its own reader reads.
	named := func(key string, names []string) []any {
		out := make([]any, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]any{key: n, "size": "5MB"})
		}
		return out
	}
	// Built as a resolved node, so the key walker never runs over it — the
	// identity has to be written in the spelling the readers read.
	slice := Node{
		"slice":  "demo",
		"atomic": map[string]any{"functions": fns},
		"backbone": map[string]any{
			"nosql": named("slot", collections),
			"blobs": named("name", buckets),
			"sql":   named("name", databases),
		},
	}
	return manifestFrom(slice)
}

// A function the slice does not declare draws on the shared pool with no error
// and no log line, met as degraded concurrency under load. Naming it here is the
// difference between a deploy-time refusal and a production mystery.
func TestCheckSliceReferences_NamesTheMissingFunctionOnly(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge", "get:groups/:id"}, nil, nil, nil)
	live := liveWith([]string{"post:auth/challenge"}, nil, nil, nil)

	err := CheckSliceReferences(m, live)
	if err == nil {
		t.Fatal("a function the slice does not declare must be refused")
	}
	if !strings.Contains(err.Error(), "get:groups/:id") {
		t.Errorf("the refusal must name the missing function, got %q", err)
	}
	if strings.Contains(err.Error(), "post:auth/challenge") {
		t.Errorf("the refusal must not name a function that IS declared, got %q", err)
	}
}

// A slice created before per-function memory existed declares none and draws on
// the shared pool. Refusing those would strand every one of them on its next
// deploy, so an empty declared set means the class is not under a declaration
// model at all.
func TestCheckSliceReferences_AnEmptyDeclaredSetSkipsItsClass(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge", "get:groups/:id"}, nil, nil, nil)
	live := liveWith(nil, nil, nil, nil)

	if err := CheckSliceReferences(m, live); err != nil {
		t.Errorf("a slice declaring no functions is not under the model and must deploy: %v", err)
	}
}

// The runtime looks a pool up by exact string against a lowercase-method key, so
// a name matching only in case is a pool no invocation reaches. Calling it
// declared would hide exactly the failure this check exists to surface.
func TestCheckSliceReferences_CaseIsNotAMatch(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge"}, nil, nil, nil)
	live := liveWith([]string{"POST:AUTH/CHALLENGE"}, nil, nil, nil)

	err := CheckSliceReferences(m, live)
	if err == nil {
		t.Fatal("a name matching only in case must be refused — the pool it names is never reached")
	}
	if !strings.Contains(err.Error(), "POST:AUTH/CHALLENGE") {
		t.Errorf("the refusal must name the near miss so the case difference is visible, got %q", err)
	}
	if !strings.Contains(err.Error(), "differs only in case") {
		t.Errorf("the refusal must say WHY the near miss does not count, got %q", err)
	}
}

// A collection the slice does not declare is refused by the slice with a 400 at
// the first write, on a live slice, after the upload.
func TestCheckSliceReferences_NamesTheMissingCollection(t *testing.T) {
	m := manifestNaming(nil, []string{"receipts"}, nil, nil)
	live := liveWith(nil, []string{"ops", "groups"}, nil, nil)

	err := CheckSliceReferences(m, live)
	if err == nil {
		t.Fatal("a collection the slice does not declare must be refused")
	}
	if !strings.Contains(err.Error(), "receipts") {
		t.Errorf("the refusal must name the missing collection, got %q", err)
	}
}

func TestCheckSliceReferences_EmptyCollectionsSkipTheClass(t *testing.T) {
	m := manifestNaming(nil, []string{"receipts"}, nil, nil)
	live := liveWith(nil, nil, nil, nil)

	if err := CheckSliceReferences(m, live); err != nil {
		t.Errorf("a slice declaring no collections is not under the model: %v", err)
	}
}

// Every miss in one block. A check that stops at the first turns one fix into as
// many deploy attempts as there are missing names.
func TestCheckSliceReferences_ReportsEveryMissAtOnce(t *testing.T) {
	m := manifestNaming([]string{"get:missing"}, []string{"receipts"}, []string{"uploads"}, []string{"ledger"})
	live := liveWith([]string{"get:present"}, []string{"ops"}, []string{"assets"}, []string{"orders"})

	err := CheckSliceReferences(m, live)
	if err == nil {
		t.Fatal("four missing names across four classes must be refused")
	}
	for _, want := range []string{"get:missing", "receipts", "uploads", "ledger"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must list %q — one block, not one name per attempt: %q", want, err)
		}
	}
	if !strings.Contains(err.Error(), common.ConfiguratorBaseURL) {
		t.Errorf("the refusal must say where the resources are added, got %q", err)
	}
}

// The control: a manifest naming only what the slice has deploys.
func TestCheckSliceReferences_AMatchingManifestPasses(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge"}, []string{"ops"}, []string{"receipts"}, []string{"ledger"})
	live := liveWith([]string{"post:auth/challenge"}, []string{"ops"}, []string{"receipts"}, []string{"ledger"})

	if err := CheckSliceReferences(m, live); err != nil {
		t.Errorf("a manifest naming only what the slice holds must deploy: %v", err)
	}
}

// A slice that does not exist is a refusal naming it, rather than a run that
// fails one /ops call at a time and renders as a platform fault.
func TestCheckSliceReferences_AMissingSliceIsNamed(t *testing.T) {
	m := manifestNaming([]string{"get:ping"}, nil, nil, nil)

	err := CheckSliceReferences(m, nil)
	if err == nil {
		t.Fatal("deploying into a slice that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "demo") {
		t.Errorf("the refusal must name the slice, got %q", err)
	}
}
