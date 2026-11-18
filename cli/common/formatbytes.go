package common

import (
	"fmt"
	"strconv"
)

// FormatBytes says a size in bytes the way a person reads it: whole bytes below a
// KiB, then KiB, MiB and GiB. The units are binary, a KiB being 1024 bytes, which
// are the ones a Driftfile writes and the platform's sentences use.
//
// A size that is a whole number of its unit has no decimal ("1 KiB") and any other
// has one, rounded to the nearest tenth ("1.5 KiB", and "1.0 KiB" for a size a
// little over one), so a decimal says the figure is not exact. A size that rounds up
// to a whole of the next unit is said in that unit, so no figure below GiB reads
// 1024: 1048575 bytes is "1.0 MiB" and not "1024.0 KiB". Past a GiB there is no
// larger unit and the figure grows.
func FormatBytes(n int64) string {
	if n < 1<<10 {
		if n == 1 {
			return "1 byte"
		}
		return strconv.FormatInt(n, 10) + " bytes"
	}

	units := [...]struct {
		size int64
		name string
	}{{1 << 10, "KiB"}, {1 << 20, "MiB"}, {1 << 30, "GiB"}}

	// The largest unit the size reaches.
	i := len(units) - 1
	for n < units[i].size {
		i--
	}
	u := units[i]

	whole, rest := n/u.size, n%u.size
	if rest == 0 {
		return strconv.FormatInt(whole, 10) + " " + u.name
	}
	// Tenths of the unit to the nearest. The rest is below one unit, so rest*10 cannot
	// overflow whatever the size.
	tenths := whole*10 + (rest*10+u.size/2)/u.size
	if tenths >= 1024*10 && i+1 < len(units) {
		return "1.0 " + units[i+1].name
	}
	return fmt.Sprintf("%d.%d %s", tenths/10, tenths%10, u.name)
}
