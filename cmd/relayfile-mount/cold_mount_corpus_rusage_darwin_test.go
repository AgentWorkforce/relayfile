//go:build darwin

package main

import "syscall"

// macOS reports ru_maxrss in bytes.
func rusageMaxRSSBytes(usage syscall.Rusage) int64 {
	return usage.Maxrss
}
