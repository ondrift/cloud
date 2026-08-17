package project

// config_wire.go — the shape of a slice as the platform reports it, for READING.
//
// These types are a CLI-local mirror of drift-common/models.SliceConfig. The CLI
// is deliberately self-contained — no drift-common dependency — so it can be
// `go install`ed without pulling in core code.
//
// # Read-only, and that is the whole point
//
// The CLI decodes a live slice's `config` into these (FetchLiveSlice, api.go) so
// it can SAY what a slice holds: which collections and buckets a manifest may
// reference, what `drift file explain` prints, what a plan reports. Nothing here
// builds a config to send.
//
// The configurator declares what a slice IS; a Driftfile declares what runs on
// it. There used to be a second implementation of the first half here — a
// translation from a Manifest into this shape, posted to create and resize — and
// two writers of one fact is the thing that change removed. A mirror the CLI
// only ever decodes into cannot drift into being a second owner.
//
// Field names match the platform's encoded shape exactly: parent fields live
// under lowercase JSON keys, while leaf fields use the Go field names, because
// the platform's models package omits json tags on those.

import (
	"fmt"
	"strconv"
	"strings"
)

// SliceConfig mirrors the JSON wire shape of drift-common/models.SliceConfig.
type SliceConfig struct {
	Canvas   CanvasLimits   `json:"canvas"`
	Atomic   AtomicLimits   `json:"atomic"`
	Backbone BackboneLimits `json:"backbone"`
}

type CanvasLimits struct {
	TotalMaxSizeInBytes int
}

type AtomicLimits struct {
	MaxNumberOfFunctions            int
	MaxFunctionRuntimeInSeconds     int
	MaxNumberOfDeploymentsInHistory int
	MaxNumberOfHoursForLogRetention int
	MaxNumberOfRequestsPerMinute    int
	MaxNumberOfScheduledJobs        int
	MaxFunctionMemoryBytes          int
	MaxStorageBytes                 int

	// Functions is the slot list the slice was sold — each identity with the
	// memory it books. A function's memory is its own admission pool AND what it
	// is priced at, so nothing here is defaulted.
	Functions []AtomicFunction `json:"functions,omitempty"`
}

// AtomicFunction mirrors drift-common/models.AtomicFunction's wire shape.
type AtomicFunction struct {
	Name        string `json:"name"`
	MemoryBytes int    `json:"memory_bytes"`
}

type BackboneLimits struct {
	Secrets  BackboneSecretsLimits  `json:"secrets"`
	Blobs    BackboneBlobsLimits    `json:"blobs"`
	NoSQL    BackboneNoSQLLimits    `json:"nosql"`
	SQL      BackboneSQLLimits      `json:"sql"`
	Queues   BackboneQueuesLimits   `json:"queues"`
	Realtime BackboneRealtimeLimits `json:"realtime"`
	Locks    BackboneLocksLimits    `json:"locks"`
	// BackupRetentionDays needs its json tag — the platform model tags this
	// leaf "backup_retention_days" (unlike the other Go-named leaves), so
	// without it the value decodes from the wrong key and reads 0.
	BackupRetentionDays int `json:"backup_retention_days"`
}

type BackboneSQLLimits struct {
	MaxDatabases int
	// Databases carries each declared database's own storage quota, keyed by
	// database name — the billing and enforcement driver.
	Databases map[string]int
}

type BackboneSecretsLimits struct {
	MaxCount           int
	MaxSizeInBytesEach int
}

type BackboneBlobsLimits struct {
	MaxSizeInBytesEach int
	// Buckets carries each declared bucket's own storage quota, keyed by bucket
	// name. Bytes are what is billed, so there is no object COUNT beside it: the
	// platform's model carries none, and a field only this side holds is one the
	// api decodes away.
	Buckets map[string]int
}

type BackboneNoSQLLimits struct {
	// Collections carries each declared collection's own storage quota, keyed by
	// collection name. The COUNT is len(this map) rather than a field of its
	// own: two fields for one fact is two fields that can disagree, and the
	// count that used to sit beside it was billed while reaching no enforcement
	// path at all.
	Collections map[string]int
}

type BackboneQueuesLimits struct {
	MaxDepthEach int
}

type BackboneLocksLimits struct {
	MaxConcurrent int
}

type BackboneRealtimeLimits struct {
	MaxConcurrentConnections int
}

// ─── the two size parsers a Driftfile still needs ───────────────────
//
// Both survive because they read a value the manifest genuinely still carries.
// The duration and rate parsers that sat beside them existed only to fill the
// envelope fields above, and went with the translation.

// parseSizeBytes turns "64MB", "1GB", "500KB" into a byte count.
//
// Used by the compiled-memory floor check, which compares a function's declared
// booking against the floor the schema publishes.
func parseSizeBytes(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"KB", 1024},
		{"MB", 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with KB/MB/GB suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in KB, MB, or GB", s)
}

// parseTTLSeconds turns a `<int><s|m|h|d>` string into seconds.
//
// Used by the nosql deploy path, which passes it to the slice. The slice
// enforces at second granularity — its maintenance sweep runs roughly every
// 30s — which is why this is second-based rather than hour- or day-based.
func parseTTLSeconds(s string) (int64, error) {
	for _, suf := range []struct {
		ext  string
		secs int64
	}{
		{"s", 1},
		{"m", 60},
		{"h", 60 * 60},
		{"d", 60 * 60 * 24},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, suf.ext), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h/d suffix", s)
			}
			return n * suf.secs, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, h, or d", s)
}
