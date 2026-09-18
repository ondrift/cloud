package azure

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestDriftConstantsPinned is the tripwire for the pricing mirror. These values
// are copied from src/common/tier/pricing.go because the CLI can't import
// core. If the platform changes a price, this test fails — update the mirror
// in pricing_drift.go and this pin together (and the pricing-ladder doc).
func TestDriftConstantsPinned(t *testing.T) {
	pins := []struct {
		name      string
		got, want int
	}{
		{"CentsPerScheduledJob", driftCentsPerScheduledJob, 30},
		{"CentsPerMiBMemory", driftCentsPerMiBMemory, 3},
		{"CentsPerRealtimeConn", driftCentsPerRealtimeConn, 1},
		{"CentsPerGiBStorage", driftCentsPerGiBStorage, 25},
	}
	for _, p := range pins {
		if p.got != p.want {
			t.Errorf("drift price %s drifted from the platform: got %d, want %d — re-sync pricing_drift.go with src/common/tier/pricing.go", p.name, p.got, p.want)
		}
	}
}

func TestPriceDrift(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	// Functions cost nothing, however many are declared — a function itself
	// is free on the platform side; you pay for what it consumes.
	if got := priceDrift(driftResources{Functions: 2}).MonthlyCents; got != 0 {
		t.Errorf("priceDrift(2 fn) = %d, want 0 (functions are free)", got)
	}
	// 64 MiB memory + 1 GiB storage: 64*3 + 25 = 217.
	if got := priceDrift(driftResources{MemoryMiB: 64, StorageBytes: 1 * gib}).MonthlyCents; got != 217 {
		t.Errorf("priceDrift(64 MiB, 1 GiB) = %d, want 217", got)
	}
	// empty slice costs nothing — no base fee anymore.
	if got := priceDrift(driftResources{}).MonthlyCents; got != 0 {
		t.Errorf("priceDrift(empty) = %d, want 0", got)
	}
}

// platformPricingConstant matches one of tier/pricing.go's own const
// declarations — e.g. "\tCentsPerScheduledJob = 30". A plain line scan
// rather than parsing Go, because that is all a text file crossing a repo
// boundary can honestly be read as; this repo cannot import the other one's
// package to ask it directly.
var platformPricingConstant = regexp.MustCompile(`^\s*(CentsPerScheduledJob|CentsPerMiBMemory|CentsPerRealtimeConn|CentsPerGiBStorage)\s*=\s*(\d+)\s*(?://.*)?$`)

// readPlatformPricingConstants scans path for the four CentsPer* declarations
// tier/pricing.go carries and returns whichever it finds, by name.
func readPlatformPricingConstants(path string) (map[string]int, error) {
	f, err := os.Open(path) // #nosec G304 -- a sibling repo path this test resolves itself, never user input
	if err != nil {
		return nil, err
	}
	defer f.Close()

	found := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := platformPricingConstant.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue // can't happen — the pattern only captures digits
		}
		found[m[1]] = n
	}
	return found, sc.Err()
}

// TestPricingMirrorAgainstPlatform is the automatic half of the tripwire
// TestDriftConstantsPinned's own doc comment describes as manual. The two
// repos are separate — cloud/public cannot import cloud/platform's Go
// package — but on a checkout where cloud-platform sits beside cloud/public
// as a sibling (this workspace's own layout: ~/drift/cloud/{public,platform}),
// nothing stops READING its source as text at test time.
//
// Where that sibling does not exist — any CI runner that checks out only
// this repo — this SKIPS rather than fails, naming the SHA pinned in
// pricing_drift.go and asking a human to re-verify by hand. That skip is the
// weaker half of the tripwire, chosen deliberately over fetching the private
// repo's source into this public repo's CI with a stored credential: pricing
// changes are rare and deliberate, and this closes the actual gap — today
// nothing catches drift at all — without a new, ongoing access boundary.
func TestPricingMirrorAgainstPlatform(t *testing.T) {
	// cli/cmd/migrate/azure -> cli/cmd/migrate -> cli/cmd -> cli ->
	// cloud/public (4 up) -> cloud (1 more) -> platform/src/common/tier.
	platformPricing := filepath.Join("..", "..", "..", "..", "..", "platform", "src", "common", "tier", "pricing.go")

	if _, err := os.Stat(platformPricing); err != nil {
		t.Skipf("cloud-platform is not checked out beside this repo (%v) — "+
			"cannot verify automatically. Re-check pricing_drift.go's constants "+
			"by hand against src/common/tier/pricing.go and update the "+
			"'verified-against' SHA there when they last matched.", err)
	}

	real, err := readPlatformPricingConstants(platformPricing)
	if err != nil {
		t.Fatalf("read %s: %v", platformPricing, err)
	}

	pins := []struct {
		platformName string
		mirrored     int
	}{
		{"CentsPerScheduledJob", driftCentsPerScheduledJob},
		{"CentsPerMiBMemory", driftCentsPerMiBMemory},
		{"CentsPerRealtimeConn", driftCentsPerRealtimeConn},
		{"CentsPerGiBStorage", driftCentsPerGiBStorage},
	}
	for _, p := range pins {
		got, found := real[p.platformName]
		if !found {
			t.Errorf("tier.%s not found in %s — it may have been renamed; "+
				"update pricing_drift.go and this test together", p.platformName, platformPricing)
			continue
		}
		if got != p.mirrored {
			t.Errorf("tier.%s = %d in the platform, but this CLI mirrors %d — "+
				"update pricing_drift.go's driftCentsPer* constants and its "+
				"'verified-against' SHA", p.platformName, got, p.mirrored)
		}
	}
}
