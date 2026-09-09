//go:build linux

package main

import "syscall"

// Linux reports ru_maxrss in KiB. Keep this conversion in an explicit
// build-tagged helper rather than relying on runtime.GOOS.
func rusageMaxRSSBytes(usage syscall.Rusage) int64 {
	return usage.Maxrss * 1024
}
