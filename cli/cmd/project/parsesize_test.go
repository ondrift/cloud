package project

import "testing"

// A Driftfile may state a store of tens of GiB: the pricing page prices a
// municipal portal's attachments at 40 GiB, and a size is read as bytes in an int.
func TestParseSizeReadsSizesPastTwoGiB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"64MiB", 64 << 20},
		{"1GiB", 1 << 30},
		{"2GiB", 2 << 30},
		{"40GiB", 40 << 30},
		{"72GiB", 72 << 30},
		{"1536KiB", 1536 << 10},
	} {
		got, err := parseSize(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestParseSizeRefusesWhatIsNotASize(t *testing.T) {
	for _, in := range []string{"64MB", "1TiB1", "", "-1GiB", "9999999999999999GiB", "99999999999999999999MiB"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) was accepted", in)
		}
	}
}
