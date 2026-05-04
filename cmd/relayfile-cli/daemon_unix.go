//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

// signalDaemonStop asks a background mount to terminate gracefully. Unix
// uses SIGTERM so the daemon can flush its state file before exiting.
func signalDaemonStop(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
