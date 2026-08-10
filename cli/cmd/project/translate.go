package project

// translate.go converts a parsed Driftfile (Manifest) into the
// SliceConfig wire shape the platform's pricing + resize APIs
// understand. This is the single source of truth for "what does this
// Driftfile mean in the platform's resource taxonomy?".
//
// Two kinds of translation happen here:
//
//   1. Resource counts: count the lists in the Manifest and write
//      them into the corresponding "Max…" fields. The user never
//      writes counts in the Driftfile — they're always derived.
//
//   2. Envelope knobs: parse the duration / size / rate strings into
//      the int fields the platform stores. Empty strings (omitted
//      knobs) leave the corresponding field at zero, signalling
//      "use the platform default" to downstream code.
//
// What this file does NOT do:
//
//   - Reach over the network (the Driftfile alone is enough).
//   - Apply Hacker-envelope defaults to omitted knobs. The platform's
//     pricing endpoint and configurator already do that; calling it
//     here would double up.
//
// SliceConfig types are CLI-local mirrors of the platform's
// drift-common/models.SliceConfig wire shape. The CLI is intentionally
// self-contained (no drift-common dependency) so it can be `go install`ed
// without pulling in core code.

import (
	"fmt"
	"strconv"
	"strings"
)

// SliceConfig mirrors the JSON wire shape of drift-common/models.SliceConfig.
// Field names match the platform's encoded shape exactly: parent fields
// live under lowercase JSON keys (canvas, atomic, backbone, secrets, blobs,
// nosql, queues, locks); leaf fields use the Go field names because the
// platform's models package omits json tags on those.
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

	// Functions is the declared function list — each name with the memory it
	// books. Mandatory in the Driftfile from schema 1.2.0; a function's memory
	// is its own pool AND what it is priced at, so nothing here is defaulted.
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
	// without it the value marshals as "BackupRetentionDays" and the server
	// silently reads 0.
	BackupRetentionDays int `json:"backup_retention_days"`
}

type BackboneSQLLimits struct {
	MaxDatabases int
	// Databases carries each declared database's own storage quota, keyed
	// by database name — the billing/enforcement driver. No more slice-wide
	// envelope: every database sets its own size in the Driftfile.
	Databases map[string]int
}

type BackboneSecretsLimits struct {
	MaxCount           int
	MaxSizeInBytesEach int
}

type BackboneBlobsLimits struct {
	MaxCount           int
	MaxSizeInBytesEach int
	// Buckets carries each declared bucket's own storage quota, keyed by
	// bucket name — the billing/enforcement driver.
	Buckets map[string]int
}

type BackboneNoSQLLimits struct {
	MaxCollections int
	// Collections carries each declared collection's own storage quota,
	// keyed by collection name — the billing/enforcement driver.
	Collections map[string]int
}

type BackboneQueuesLimits struct {
	MaxQueues    int
	MaxDepthEach int
}

type BackboneLocksLimits struct {
	MaxConcurrent int
}

type BackboneRealtimeLimits struct {
	MaxConcurrentConnections int
}

// ManifestToSliceConfig builds a SliceConfig from a parsed Driftfile.
// Unset envelope knobs leave the corresponding field at zero, which
// the platform reads as "use the slice envelope's default for this
// field" (the same convention the configurator uses today).
//
// Returns a translation-error block if any envelope knob fails to
// parse despite passing the upstream Driftfile validation. That
// would be an internal bug, not user error, but we surface it
// rather than swallowing it.
func ManifestToSliceConfig(m *Manifest) (SliceConfig, error) {
	var (
		cfg  SliceConfig
		errs []string
	)

	// ── Resource counts ─────────────────────────────────────────────
	// An Atomic function is a callable in user source with an `@atomic`
	// annotation directly above it. Walk every directory listed under
	// atomic.functions in the Driftfile and count decorated callables.
	// Helpers (no annotation) don't count.
	if hc, hcErr := CountAtomicFunctions(m); hcErr == nil {
		cfg.Atomic.MaxNumberOfFunctions = hc
	} else {
		// Source not readable (e.g. manifest preflight before deploy);
		// fall back to one function per directory entry.
		cfg.Atomic.MaxNumberOfFunctions = len(m.Slice().Entries("name", "atomic", "functions"))
	}

	// Scheduled-job count spans BOTH declaration sites — `atomic.functions[].cron`
	// in the Driftfile and `@atomic cron=` in source. CountScheduledFunctions
	// counts the Driftfile half without touching the tree, so even a preflight
	// that cannot read source still sizes the envelope for every declared
	// schedule; its error return carries that partial count rather than zero.
	sc, _ := CountScheduledFunctions(m)
	cfg.Atomic.MaxNumberOfScheduledJobs = sc
	if v := m.Slice().Int("atomic", "deploy_history"); v > 0 {
		cfg.Atomic.MaxNumberOfDeploymentsInHistory = v
	}

	cfg.Backbone.NoSQL.MaxCollections = len(m.Slice().Entries("name", "backbone", "nosql"))
	cfg.Backbone.SQL.MaxDatabases = len(m.Slice().Entries("name", "backbone", "sql"))
	cfg.Backbone.Queues.MaxQueues = len(m.Slice().Entries("name", "backbone", "queues"))
	cfg.Backbone.Secrets.MaxCount = len(m.Slice().Sub("backbone", "secrets"))

	// Realtime is a scalar knob (a connection budget), not a list of named
	// resources — declared directly. Omitted → 0 → realtime disabled.
	cfg.Backbone.Realtime.MaxConcurrentConnections = m.Slice().Int("backbone", "realtime_connections")

	// ── Atomic envelope knobs ───────────────────────────────────────
	if v := m.Slice().Str("atomic", "function_memory"); v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("atomic.function_memory: %v", err))
		} else {
			cfg.Atomic.MaxFunctionMemoryBytes = bytes
		}
	}

	// ── The declared functions, each with the memory it books ───────
	//
	// Both fields are required and neither is defaulted (schema 1.2.0). A
	// missing memory is reported rather than filled in: the figure is the
	// function's pool AND its price, so a value the platform invented is one the
	// tenant never agreed to and would first meet on an invoice.
	for i, fn := range m.Slice().Nodes("atomic", "functions") {
		name := fn.Str("name")
		if name == "" {
			errs = append(errs, fmt.Sprintf("atomic.functions[%d]: needs a name", i))
			continue
		}
		raw := fn.Str("memory")
		if raw == "" {
			errs = append(errs, fmt.Sprintf(
				"atomic.functions[%d] (%s): needs `memory` — every function declares what it books, "+
					"and there is no default", i, name))
			continue
		}
		bytes, err := parseSizeBytes(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("atomic.functions[%d] (%s).memory: %v", i, name, err))
			continue
		}
		cfg.Atomic.Functions = append(cfg.Atomic.Functions, AtomicFunction{
			Name:        name,
			MemoryBytes: bytes,
		})
	}
	if v := m.Slice().Str("atomic", "atomic_size"); v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("atomic.atomic_size: %v", err))
		} else {
			cfg.Atomic.MaxStorageBytes = bytes
		}
	}
	if v := m.Slice().Str("atomic", "function_timeout"); v != "" {
		secs, err := parseDurationSeconds(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("atomic.function_timeout: %v", err))
		} else {
			cfg.Atomic.MaxFunctionRuntimeInSeconds = secs
		}
	}
	if v := m.Slice().Str("atomic", "rate_limit"); v != "" {
		rpm, err := parseRatePerMinute(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("atomic.rate_limit: %v", err))
		} else {
			cfg.Atomic.MaxNumberOfRequestsPerMinute = rpm
		}
	}

	// ── Backbone envelope knobs ────────────────────────────────────
	// Per-item storage: each declared collection/database/bucket carries its
	// own `size`, keyed by name — there is no slice-wide envelope any more.
	// Validation already guarantees every entry has a non-empty Size by the
	// time translation runs; a parse failure here would be an internal bug
	// (a size that passed sizeRe but parseSizeBytes rejects), not user error.
	if len(m.Slice().Entries("name", "backbone", "nosql")) > 0 {
		cfg.Backbone.NoSQL.Collections = make(map[string]int, len(m.Slice().Entries("name", "backbone", "nosql")))
		for _, c := range m.Slice().Entries("name", "backbone", "nosql") {
			bytes, err := parseSizeBytes(c.Str("size"))
			if err != nil {
				errs = append(errs, fmt.Sprintf("backbone.nosql[%s].size: %v", c.Str("name"), err))
				continue
			}
			cfg.Backbone.NoSQL.Collections[c.Str("name")] = bytes
		}
	}
	if v := m.Slice().Str("backbone", "blob_max_size"); v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("backbone.blob_max_size: %v", err))
		} else {
			cfg.Backbone.Blobs.MaxSizeInBytesEach = bytes
		}
	}
	if blobs := m.Slice().Entries("name", "backbone", "blobs"); len(blobs) > 0 {
		cfg.Backbone.Blobs.Buckets = make(map[string]int, len(blobs))
		for _, bk := range blobs {
			bytes, err := parseSizeBytes(bk.Str("size"))
			if err != nil {
				errs = append(errs, fmt.Sprintf("backbone.blobs[%s].size: %v", bk.Str("name"), err))
				continue
			}
			cfg.Backbone.Blobs.Buckets[bk.Str("name")] = bytes
		}
	}
	if v := m.Slice().Int("backbone", "blob_max_count"); v > 0 {
		cfg.Backbone.Blobs.MaxCount = v
	}
	if v := m.Slice().Int("backbone", "queue_max_depth"); v > 0 {
		cfg.Backbone.Queues.MaxDepthEach = v
	}
	if len(m.Slice().Entries("name", "backbone", "sql")) > 0 {
		cfg.Backbone.SQL.Databases = make(map[string]int, len(m.Slice().Entries("name", "backbone", "sql")))
		for _, d := range m.Slice().Entries("name", "backbone", "sql") {
			bytes, err := parseSizeBytes(d.Str("size"))
			if err != nil {
				errs = append(errs, fmt.Sprintf("backbone.sql[%s].size: %v", d.Str("name"), err))
				continue
			}
			cfg.Backbone.SQL.Databases[d.Str("name")] = bytes
		}
	}
	if v := m.Slice().Str("backbone", "secret_max_size"); v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("backbone.secret_max_size: %v", err))
		} else {
			cfg.Backbone.Secrets.MaxSizeInBytesEach = bytes
		}
	}
	if v := m.Slice().Int("backbone", "locks"); v > 0 {
		cfg.Backbone.Locks.MaxConcurrent = v
	}

	// ── Canvas envelope ─────────────────────────────────────────────
	if v := m.Slice().Str("canvas", "canvas_size"); v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("canvas.canvas_size: %v", err))
		} else {
			cfg.Canvas.TotalMaxSizeInBytes = bytes
		}
	}

	// ── Operational ─────────────────────────────────────────────────
	if v := m.Slice().Str("log_retention"); v != "" {
		hours, err := parseDurationHours(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("log_retention: %v", err))
		} else {
			cfg.Atomic.MaxNumberOfHoursForLogRetention = hours
		}
	}
	if v := m.Slice().Str("backup_retention"); v != "" {
		days, err := parseDurationDays(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("backup_retention: %v", err))
		} else {
			cfg.Backbone.BackupRetentionDays = days
		}
	}

	if len(errs) > 0 {
		return cfg, fmt.Errorf("translation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// parseSizeBytes turns "64MB", "1GB", "500KB" into a byte count.
// The Driftfile parser already validates the format; this is a
// belt-and-braces re-check that fails noisily if someone bypasses it.
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

// parseDurationSeconds turns "30s", "5m", "1h" into a number of seconds.
// Days are not allowed here — function timeouts that long are nonsense.
func parseDurationSeconds(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"s", 1},
		{"m", 60},
		{"h", 60 * 60},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, or h", s)
}

// parseDurationHours turns a `<int><s|m|h|d>` string into hours,
// rounded down. Used by log retention which is stored as integer hours.
func parseDurationHours(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"s", 0}, // sub-hour values truncate to 0
		{"m", 0}, // sub-hour values truncate to 0
		{"h", 1},
		{"d", 24},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h/d suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, h, or d", s)
}

// parseDurationDays turns a `<int><s|m|h|d>` string into days,
// rounded down. Used by backup retention which is stored as integer days.
func parseDurationDays(s string) (int, error) {
	for _, suf := range []struct {
		ext  string
		secs int
	}{
		{"s", 1},
		{"m", 60},
		{"h", 60 * 60},
		{"d", 60 * 60 * 24},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h/d suffix", s)
			}
			totalSecs := n * suf.secs
			days := totalSecs / (60 * 60 * 24)
			return days, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, h, or d", s)
}

// parseTTLSeconds turns a `<int><s|m|h|d>` string into seconds. Used by
// nosql collection ttl, which the slice enforces at second granularity (its
// maintenance sweep runs roughly every 30s) rather than the hour/day
// granularity log/backup retention use. (Distinct from parseDurationSeconds
// above, which is function-timeout-specific and deliberately rejects `d`.)
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

// parseRatePerMinute turns "1000/min", "10/s", "60000/h" into requests
// per minute. The platform stores rate limits in per-minute granularity
// regardless of the Driftfile's expressed unit.
func parseRatePerMinute(s string) (int, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("%q must be of the form <integer>/<s|min|h>", s)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%q has a non-integer count", s)
	}
	switch parts[1] {
	case "s":
		return n * 60, nil
	case "min":
		return n, nil
	case "h":
		return n / 60, nil
	default:
		return 0, fmt.Errorf("%q must end in /s, /min, or /h", s)
	}
}
