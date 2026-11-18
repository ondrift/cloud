package common

import "strconv"

// FormatBytes prints a size in bytes as the bare integer.
func FormatBytes(n int64) string { return strconv.FormatInt(n, 10) }
