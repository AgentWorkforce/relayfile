//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os/exec"
	"syscall"
)

// awaitColdCorpusChild reaps one relayfile-mount child and returns its exit
// code, CPU milliseconds, and peak RSS bytes from the child's rusage — the
// same quantities the Cloud acceptance reads from cgroup-v2 cpu.stat and
// memory.peak.
func awaitColdCorpusChild(cmd *exec.Cmd) (exitCode int, cpuMs int64, peakRSSBytes int64) {
	var status syscall.WaitStatus
	var usage syscall.Rusage
	if _, err := syscall.Wait4(cmd.Process.Pid, &status, 0, &usage); err != nil {
		return -1, -1, -1
	}
	if status.Exited() {
		exitCode = status.ExitStatus()
	} else {
		exitCode = -1
	}
	cpuMs = rusageCPUMs(usage)
	peakRSSBytes = rusageMaxRSSBytes(usage)
	return exitCode, cpuMs, peakRSSBytes
}

func rusageCPUMs(usage syscall.Rusage) int64 {
	user := float64(usage.Utime.Sec)*1000 + float64(usage.Utime.Usec)/1000
	sys := float64(usage.Stime.Sec)*1000 + float64(usage.Stime.Usec)/1000
	return int64(user + sys)
}
