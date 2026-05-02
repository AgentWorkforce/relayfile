//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | syscall.DETACHED_PROCESS,
	}
	return nil
}
