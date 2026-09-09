//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

package main

import "syscall"

// The exact ru_maxrss unit is not portable across these Unix targets. Keep
// them buildable, but report unavailable rather than risking a false resource
// qualification. The corpus test explicitly runs only on Linux and macOS.
func rusageMaxRSSBytes(syscall.Rusage) int64 {
	return -1
}
