package slice

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The quota envelope is a wire contract the slice parses by field name, and the
// CLI reads only the part a person asks about. Every figure is a number, so a
// key the platform renames decodes as zero, which reads as "unbounded".
const envelopeBody = `{
  "declaration_model": true,
  "atomic_function_slots": {"get:ping": 33554432, "post:rpc": 67108864, "worker:sequencer": 33554432},
  "blob_bucket_quotas": {"uploads": 268435456},
  "queue_max_depth": 100,
  "queue_quotas": {"orders": 0, "notify": 200},
  "no_sql_collection_quotas": {"sessions": 33554432},
  "sql_max_databases": 1,
  "sql_database_quotas": {"ledger": 10485760},
  "realtime_max_connections": 50,
  "atomic_max_storage_bytes": 1073741824,
  "log_retention_hours": 24
}`

func TestEnvelope_DecodesTheQuotaDocument(t *testing.T) {
	var e Envelope
	if err := json.Unmarshal([]byte(envelopeBody), &e); err != nil {
		t.Fatalf("the envelope must decode: %v", err)
	}
	if len(e.Slots) != 3 || e.Slots["post:rpc"] != 67108864 {
		t.Errorf("slots decoded wrong: %v", e.Slots)
	}
	if e.Buckets["uploads"] != 268435456 || e.Collections["sessions"] != 33554432 || e.Databases["ledger"] != 10485760 {
		t.Errorf("stores decoded wrong: %+v", e)
	}
	if e.Queues["orders"] != 0 || e.Queues["notify"] != 200 || e.QueueDepth != 100 {
		t.Errorf("queues decoded wrong: %+v", e)
	}
	if e.RealtimeConnections != 50 || e.AtomicStorageBytes != 1073741824 || e.LogRetentionHours != 24 {
		t.Errorf("slice-wide figures decoded wrong: %+v", e)
	}
}

func renderShape(t *testing.T, price string) string {
	t.Helper()
	var e Envelope
	if err := json.Unmarshal([]byte(envelopeBody), &e); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printShapeSection(&out, &e, price)
	return out.String()
}

// The section answers what the card asked: what the slice holds, what each part
// is allowed, and what it costs.
func TestShapeSection_SaysWhatTheSliceHoldsAndCosts(t *testing.T) {
	got := renderShape(t, "EUR 7.68/mo")
	for _, want := range []string{
		"EUR 7.68/mo",
		"3 booked, 128 MiB",
		"ledger",
		"10 MiB",
		"sessions",
		"32 MiB",
		"uploads",
		"256 MiB",
		"orders",
		"the slice's depth, 100 messages",
		"notify",
		"200 messages",
		"50 connections",
		"1024 MiB",
		"24h",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the shape section lacks %q:\n%s", want, got)
		}
	}
}

// A queue with no depth of its own is the slice's depth, and a slice with no
// price recorded says so: zero is a figure that was never recorded, and printing
// it as a price would be a billing claim the CLI has no basis to make.
func TestShapeSection_DoesNotInventAPrice(t *testing.T) {
	got := renderShape(t, "")
	if strings.Contains(got, "EUR") || strings.Contains(got, "free") {
		t.Errorf("a slice with no recorded price was given one:\n%s", got)
	}
	if !strings.Contains(got, "price") || !strings.Contains(got, "not recorded") {
		t.Errorf("the section does not say the price is not recorded:\n%s", got)
	}
}

// A slice that declares nothing of a kind prints nothing of that kind.
func TestShapeSection_OmitsWhatTheSliceDoesNotHold(t *testing.T) {
	var out bytes.Buffer
	printShapeSection(&out, &Envelope{}, "free")
	got := out.String()
	for _, absent := range []string{"databases", "collections", "buckets", "queues", "slots"} {
		if strings.Contains(got, absent) {
			t.Errorf("an empty envelope printed a %s line:\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "free") {
		t.Errorf("the price line is missing:\n%s", got)
	}
}
