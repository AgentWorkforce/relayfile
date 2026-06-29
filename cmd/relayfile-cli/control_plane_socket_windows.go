//go:build windows

package main

import "net"

func listenControlPlaneSocket(sock string) (net.Listener, error) {
	return net.Listen("unix", sock)
}
