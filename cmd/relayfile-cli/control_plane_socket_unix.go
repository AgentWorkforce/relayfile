//go:build !windows

package main

import (
	"net"
	"syscall"
)

func listenControlPlaneSocket(sock string) (net.Listener, error) {
	oldUmask := syscall.Umask(0o177)
	defer syscall.Umask(oldUmask)
	return net.Listen("unix", sock)
}
