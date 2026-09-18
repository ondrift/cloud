package azure

// Drift-side pricing — a deliberate MIRROR of the platform's billing constants
// at src/common/tier/pricing.go. The CLI is its own module and must not
// import core internals, so the numbers are copied here and pinned by a
// tripwire test (pricing_drift_test.go). If the platform's prices change, that
// test fails and reminds us to update this mirror.
//
// Source of truth: docs/memos/done/pricing-v2-ram-storage-model.md (and the
// constants in src/common/tier/pricing.go). Integer cents, never floats (same
// discipline as the platform). The model charges for the box: RAM (function
// memory, realtime) + storage (per GiB); collections / queues / SQL dbs /
// blob count are FREE. No flat base fee, and no per-function charge either —
// both removed platform-side (see pricing.go's own comments) since neither
// mapped to a configured resource: a function itself is free, you pay for
// what it consumes (the memory it books, the disk its artifact occupies).
// verified-against: cloud-platform@3618395 — the four values below matched
// src/common/tier/pricing.go's CentsPer* constants exactly at that commit.
// The two repos cannot share a Go import, so this is the manual half of the
// tripwire: TestPricingMirrorAgainstPlatform (pricing_drift_test.go) checks
// automatically whenever cloud-platform is checked out as a sibling of this
// repo (this workspace's own layout) and reminds a human to re-pin otherwise.
// Re-run it and update this SHA whenever any of the four changes on purpose.
const (
	driftCentsPerScheduledJob = 30 // per scheduled (cron) job (background load)
	driftCentsPerMiBMemory    = 3  // per MiB of function-memory cap (RAM, primary lever)
	driftCentsPerRealtimeConn = 1  // per concurrent realtime connection (RAM)
	driftCentsPerGiBStorage   = 25 // per GiB of pooled storage (nosql+blob+sql+canvas)
	bytesPerGiB               = 1024 * 1024 * 1024
)

// driftResources is the priced shape of a synthesized slice — only the inputs
// that move the bill under the RAM+storage model. Collections / queues / SQL
// databases / blob count are free (quota-only), so they aren't priced here.
type driftResources struct {
	Functions     int
	ScheduledJobs int
	MemoryMiB     int   // function-memory cap; 0 = not charged
	StorageBytes  int64 // pooled nosql + blob + sql + canvas bytes
	RealtimeConns int
}

type driftLine struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Quantity      int    `json:"quantity"`
	UnitCents     int    `json:"unit_cents"`
	SubtotalCents int    `json:"subtotal_cents"`
}

type driftBreakdown struct {
	Lines        []driftLine `json:"lines"`
	MonthlyCents int         `json:"monthly_cents"`
}

func clampNonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// priceDrift mirrors tier.PriceConfig: RAM + storage + tokens → monthly cents.
// Kept structurally aligned so the tripwire test can compare totals.
//
// r.Functions is not priced here — it never has been a chargeable resource
// on the platform side, and is carried on driftResources only as a count the
// estimate's output surfaces (movable Function Apps found), not as a pricing
// input.
func priceDrift(r driftResources) driftBreakdown {
	sched := clampNonNeg(r.ScheduledJobs)
	mem := clampNonNeg(r.MemoryMiB)
	conns := clampNonNeg(r.RealtimeConns)
	storage := r.StorageBytes
	if storage < 0 {
		storage = 0
	}
	// Byte-accurate, rounded half-up to the cent (same as tier.PriceConfig).
	storageCents := int((storage*int64(driftCentsPerGiBStorage) + bytesPerGiB/2) / bytesPerGiB)

	lines := []driftLine{
		{"atomic_scheduled", "Scheduled jobs", sched, driftCentsPerScheduledJob, sched * driftCentsPerScheduledJob},
		{"realtime_connections", "Realtime connections", conns, driftCentsPerRealtimeConn, conns * driftCentsPerRealtimeConn},
	}
	if mem > 0 {
		lines = append(lines, driftLine{"atomic_memory", "Function memory (MiB)", mem, driftCentsPerMiBMemory, mem * driftCentsPerMiBMemory})
	}
	// One storage line, deliberately. The platform bills storage as three —
	// Atomic, Backbone and Canvas — but a migration estimate reads one lump of
	// bytes off an Azure account with nothing to attribute them to, and all
	// three carry the same rate, so the total is identical either way. The key
	// is not `bb_storage`: that one now means Backbone alone.
	lines = append(lines, driftLine{"storage", "Storage (per GiB)", int(storage / (1024 * 1024)), driftCentsPerGiBStorage, storageCents})

	total := 0
	for _, l := range lines {
		total += l.SubtotalCents
	}
	return driftBreakdown{Lines: lines, MonthlyCents: total}
}
