package common

import (
	"strconv"
	"strings"
	"testing"
)

// A size is said in the unit a person reads it in: whole bytes below a KiB, then KiB, MiB and
// GiB. They are the units a Driftfile writes and the platform's sentences use, and they are
// binary, so a KiB is 1024 bytes. A size that is a whole number of its unit prints without a
// decimal and any other size prints with one, so "1.0 KiB" says a little over one KiB where
// "1 KiB" says exactly one.
func TestFormatBytes_SaysASizeInBytesKiBMiBOrGiB(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		// Below a KiB, whole bytes.
		{0, "0 bytes"},
		{1, "1 byte"},
		{2, "2 bytes"},
		{1023, "1023 bytes"},

		// The edge between bytes and KiB, and sizes in KiB.
		{1024, "1 KiB"},
		{1025, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 << 10, "5 KiB"},
		{100 << 10, "100 KiB"},

		// The edge between KiB and MiB: a size that rounds up to a whole MiB is said in MiB.
		{1<<20 - 1, "1.0 MiB"},
		{1 << 20, "1 MiB"},
		{1<<20 + 1, "1.0 MiB"},
		{1572864, "1.5 MiB"},
		{5242880, "5 MiB"},
		{100 << 20, "100 MiB"},

		// The edge between MiB and GiB.
		{1<<30 - 1, "1.0 GiB"},
		{1073741824, "1 GiB"},
		{1<<30 + 1, "1.0 GiB"},
		{3 << 29, "1.5 GiB"},
		{5 << 30, "5 GiB"},

		// Past a GiB there is no larger unit, so the figure grows instead.
		{1 << 40, "1024 GiB"},
	} {
		if got := FormatBytes(c.n); got != c.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// A size just under a unit's edge rounds up to a whole of the next unit and is said in it, so no
// figure below GiB reads 1024: "1024.0 KiB" and "1.0 MiB" are one size said two ways, and the
// second is the one a person expects. The window below each edge is wider than the rounding
// reaches, and every size in it is held to the rule.
func TestFormatBytes_ASizeJustUnderAnEdgeIsSaidInTheNextUnit(t *testing.T) {
	for _, edge := range []int64{1 << 10, 1 << 20, 1 << 30} {
		for n := max(0, edge-100_000); n < edge; n++ {
			got := FormatBytes(n)
			fields := strings.Fields(got)
			if len(fields) != 2 {
				t.Fatalf("FormatBytes(%d) = %q, want a figure and a unit", n, got)
			}
			figure, err := strconv.ParseFloat(fields[0], 64)
			if err != nil || figure >= 1024 {
				t.Fatalf("FormatBytes(%d) = %q: a figure of 1024 or more belongs to the next unit", n, got)
			}
		}
	}
}

// Whole in its unit is no decimal, and anything else is one decimal, in every unit.
func TestFormatBytes_AWholeNumberOfAUnitHasNoDecimalAndNothingElseDoesNot(t *testing.T) {
	for _, unit := range []struct {
		size int64
		name string
	}{
		{1 << 10, "KiB"},
		{1 << 20, "MiB"},
		{1 << 30, "GiB"},
	} {
		for _, k := range []int64{1, 2, 5, 100, 1023} {
			whole := FormatBytes(k * unit.size)
			if want := strconv.FormatInt(k, 10) + " " + unit.name; whole != want {
				t.Errorf("FormatBytes(%d) = %q, want %q", k*unit.size, whole, want)
			}
			if over := FormatBytes(k*unit.size + 1); !strings.Contains(over, ".") || !strings.HasSuffix(over, " "+unit.name) {
				t.Errorf("FormatBytes(%d) = %q, want one decimal in %s", k*unit.size+1, over, unit.name)
			}
		}
	}
}
