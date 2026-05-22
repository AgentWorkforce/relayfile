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

// forceDaemonStop terminates a daemon that did not exit after the first stop
// request. Windows has no stronger portable signal than TerminateProcess.
func forceDaemonStop(process *os.Process) error {
	return process.Kill()
}

// processExecutablePath is a no-op on Windows: resolving another
// process's image path needs OpenProcess/QueryFullProcessImageName and
// the extra privileges/handling are not worth it for this guard. Callers
// fall back to the workspace-id and local-dir identity checks.
func processExecutablePath(pid int) (path string, known bool) {
	return "", false
}
