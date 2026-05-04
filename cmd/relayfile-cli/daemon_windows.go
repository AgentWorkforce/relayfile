//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDetachedProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	return nil
}

// signalDaemonStop asks a background mount to terminate. On Windows the
// kernel only routes os.Kill through Process.Signal, so we use Kill()
// (which calls TerminateProcess) instead of SIGTERM. The mount loop's
// state file is flushed on every cycle, so terminating between cycles
// is safe even though it is not as graceful as the Unix path.
func signalDaemonStop(process *os.Process) error {
	return process.Kill()
}
