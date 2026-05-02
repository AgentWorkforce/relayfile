//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
