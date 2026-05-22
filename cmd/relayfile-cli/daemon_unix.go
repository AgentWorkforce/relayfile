//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

// processExecutablePath best-effort resolves the on-disk executable for a
// running PID. Linux exposes /proc/<pid>/exe; other Unix platforms (e.g.
// macOS) have no portable equivalent without extra deps, so known=false
// and callers fall back to the remaining identity checks.
func processExecutablePath(pid int) (path string, known bool) {
	if pid <= 0 {
		return "", false
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || target == "" {
		return "", false
	}
	return target, true
}

// signalDaemonStop asks a background mount to terminate gracefully. Unix
// uses SIGTERM so the daemon can flush its state file before exiting.
func signalDaemonStop(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

// forceDaemonStop terminates a daemon that ignored the graceful stop signal.
func forceDaemonStop(process *os.Process) error {
	return process.Kill()
}
