//go:build unix

package genmain

import "syscall"

// peakRSSBytes is this process's high-water resident size. ru_maxrss is in
// kilobytes on Linux, which is where a deployed function runs.
func peakRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return int64(ru.Maxrss) * 1024
}
