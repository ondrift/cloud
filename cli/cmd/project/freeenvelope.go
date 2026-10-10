package project

// freeenvelope.go — does a compiled Driftfile shape fit the free slice?
//
// `billing = "free"` asks for the free slice, and the platform provisions one
// only when the shape is inside what the free slice grants. The plan answers
// that question offline, before anything is sent, with the rule that refuses
// it: the same bounds, in the same order, as tier.FreeEnvelopeViolation in
// the platform (src/common/tier/envelope.go).
//
// THE PLATFORM OWNS THESE NUMBERS. They are restated here because the plan
// must answer offline and the platform does not yet publish the free preset
// beside the Driftfile schema. tier.HackerPreset (src/common/tier/tiers.go) is
// the authority; a preset change there must land here in the same change,
// and the api still refuses a create that does not fit, whatever this says.

import (
	"fmt"
	"sort"
)

// freePreset is tier.HackerConfig as the platform defines it: the free
// slice's scalars, and the counts and totals its declared sets are bounded by.
var freePreset = SliceConfig{
	Canvas: CanvasLimits{TotalMaxSizeInBytes: 50 * bytesPerMiB},
	Atomic: AtomicLimits{
		MaxNumberOfFunctions:            5,
		MaxFunctionRuntimeInSeconds:     10,
		MaxNumberOfDeploymentsInHistory: 1,
		MaxNumberOfHoursForLogRetention: 24,
		MaxNumberOfRequestsPerMinute:    60,
		MaxNumberOfScheduledJobs:        1,
		MaxFunctionMemoryBytes:          32 * bytesPerMiB,
		MaxStorageBytes:                 100 * bytesPerMiB,
	},
	Backbone: BackboneLimits{
		Secrets: BackboneSecretsLimits{MaxCount: 5, MaxSizeInBytesEach: 1024},
		Blobs:   BackboneBlobsLimits{MaxSizeInBytesEach: 5 * bytesPerMiB},
		Queues:  BackboneQueuesLimits{MaxDepthEach: 500},
		Realtime: BackboneRealtimeLimits{
			MaxConcurrentConnections: 50,
		},
		Locks:               BackboneLocksLimits{MaxConcurrent: 100000},
		SQL:                 BackboneSQLLimits{MaxDatabases: 1},
		BackupRetentionDays: 3,
	},
	Deed: DeedLimits{
		Vault:  DeedVaultLimits{MaxSizeInBytesEach: 4 * 1024, MaxEntriesPerUID: 100},
		Pocket: DeedPocketLimits{MaxSizeInBytesEach: 16 * 1024},
	},
}

// freeSets is the free slice's declared sets as counts and totals: five slots
// summing to 80 MiB, two collections of 20 MiB, ten buckets of 5 MiB, one
// database of 10 MiB, and one queue.
var freeSets = struct {
	slots, slotMiB               int
	collections, collectionBytes int
	buckets, bucketBytes         int
	databases, databaseBytes     int
	queues                       int
}{
	slots: 5, slotMiB: 80,
	collections: 2, collectionBytes: 40 * bytesPerMiB,
	buckets: 10, bucketBytes: 50 * bytesPerMiB,
	databases: 1, databaseBytes: 10 * bytesPerMiB,
	queues: 1,
}

const bytesPerMiB = 1024 * 1024

// freeLimit is one scalar the preset pins, by the name the Driftfile writes it
// under and the reader that finds it on a shape.
type freeLimit struct {
	name string
	at   func(*SliceConfig) *int
}

// freeLimits is every scalar the free slice bounds, in the platform's order.
var freeLimits = []freeLimit{
	{"slots (MaxNumberOfFunctions)", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfFunctions }},
	{"limits.timeout", func(c *SliceConfig) *int { return &c.Atomic.MaxFunctionRuntimeInSeconds }},
	{"limits.deploy_history", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfDeploymentsInHistory }},
	{"limits.log_retention", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfHoursForLogRetention }},
	{"limits.requests_per_minute", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfRequestsPerMinute }},
	{"limits.scheduled_jobs", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfScheduledJobs }},
	{"the shared function pool", func(c *SliceConfig) *int { return &c.Atomic.MaxFunctionMemoryBytes }},
	{"limits.atomic_storage", func(c *SliceConfig) *int { return &c.Atomic.MaxStorageBytes }},
	{"limits.canvas_storage", func(c *SliceConfig) *int { return &c.Canvas.TotalMaxSizeInBytes }},
	{"limits.secrets", func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxCount }},
	{"limits.secret_size", func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxSizeInBytesEach }},
	{"limits.blob_object_size", func(c *SliceConfig) *int { return &c.Backbone.Blobs.MaxSizeInBytesEach }},
	{"limits.queue_depth", func(c *SliceConfig) *int { return &c.Backbone.Queues.MaxDepthEach }},
	{"limits.locks", func(c *SliceConfig) *int { return &c.Backbone.Locks.MaxConcurrent }},
	{"databases (MaxDatabases)", func(c *SliceConfig) *int { return &c.Backbone.SQL.MaxDatabases }},
	{"limits.realtime_connections", func(c *SliceConfig) *int { return &c.Backbone.Realtime.MaxConcurrentConnections }},
	{"limits.backup_retention", func(c *SliceConfig) *int { return &c.Backbone.BackupRetentionDays }},
	{"limits.vault_entry_size", func(c *SliceConfig) *int { return &c.Deed.Vault.MaxSizeInBytesEach }},
	{"limits.vault_entries", func(c *SliceConfig) *int { return &c.Deed.Vault.MaxEntriesPerUID }},
	{"limits.pocket_record_size", func(c *SliceConfig) *int { return &c.Deed.Pocket.MaxSizeInBytesEach }},
}

// freeEnvelopeViolation names the first bound a shape crosses, or nil when it
// is the free shape. domains is how many custom domains the file declares:
// the free slice grants none.
func freeEnvelopeViolation(c SliceConfig, domains int) error {
	free := freePreset
	for _, l := range freeLimits {
		v, grant := *l.at(&c), *l.at(&free)
		if v < 0 {
			return fmt.Errorf("%s is unlimited, and the free slice grants %d", l.name, grant)
		}
		if v > grant {
			return fmt.Errorf("%s is %d, and the free slice grants %d", l.name, v, grant)
		}
	}

	if n := len(c.Atomic.Functions); n > freeSets.slots {
		return fmt.Errorf("the file declares %d pulses and fields, and the free slice grants %d", n, freeSets.slots)
	}
	total := 0
	for _, f := range c.Atomic.Functions {
		if f.MemoryBytes > 0 {
			total += f.MemoryBytes
		}
	}
	if mib := total / bytesPerMiB; mib > freeSets.slotMiB {
		return fmt.Errorf("the pulses and fields book %d MiB in total, and the free slice grants %d MiB "+
			"across up to %d of them, in any sizes", mib, freeSets.slotMiB, freeSets.slots)
	}

	for _, s := range []struct {
		noun       string
		set        map[string]int
		count, sum int
	}{
		{"nosql collection", c.Backbone.NoSQL.Collections, freeSets.collections, freeSets.collectionBytes},
		{"blobs bucket", c.Backbone.Blobs.Buckets, freeSets.buckets, freeSets.bucketBytes},
		{"sql database", c.Backbone.SQL.Databases, freeSets.databases, freeSets.databaseBytes},
	} {
		if len(s.set) > s.count {
			return fmt.Errorf("the file declares %d %ss, and the free slice grants %d", len(s.set), s.noun, s.count)
		}
		sum := 0
		for _, v := range s.set {
			if v > 0 {
				sum += v
			}
		}
		if sum > s.sum {
			return fmt.Errorf("the %ss total %s, and the free slice grants %s across all of them",
				s.noun, sizeLabel(sum), sizeLabel(s.sum))
		}
	}

	if n := len(c.Backbone.Queues.Queues); n > freeSets.queues {
		return fmt.Errorf("the file declares %d queues, and the free slice grants %d", n, freeSets.queues)
	}
	names := make([]string, 0, len(c.Backbone.Queues.Queues))
	for n := range c.Backbone.Queues.Queues {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if d := c.Backbone.Queues.Queues[n]; d > free.Backbone.Queues.MaxDepthEach {
			return fmt.Errorf("queue %q is %d deep, and the free slice grants %d", n, d, free.Backbone.Queues.MaxDepthEach)
		}
	}

	if domains > 0 {
		return fmt.Errorf("the file declares %d custom domain(s), and the free slice grants none", domains)
	}
	return nil
}
