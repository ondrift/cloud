//go:build !unix

package genmain

// peakRSSBytes reports nothing on a platform without getrusage; a reply then
// carries no rss_bytes, which the slice reads as unmeasured.
func peakRSSBytes() int64 { return 0 }
