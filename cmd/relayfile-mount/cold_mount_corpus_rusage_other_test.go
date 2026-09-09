//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package main

import "os/exec"

// awaitColdCorpusChild is the fallback for platforms without the unix
// syscall.Wait4 rusage path (windows, js/wasm, wasip1, plan9): the exit code
// comes from cmd.Wait while child CPU and peak RSS are reported unavailable
// (-1). The corpus qualification test skips these platforms up front, so this
// stub exists to keep the package compiling everywhere.
func awaitColdCorpusChild(cmd *exec.Cmd) (exitCode int, cpuMs int64, peakRSSBytes int64) {
	err := cmd.Wait()
	if err == nil {
		return 0, -1, -1
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), -1, -1
	}
	return -1, -1, -1
}
