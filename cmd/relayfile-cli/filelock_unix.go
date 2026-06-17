//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFileExclusive(file *os.File) (func(), error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}, nil
}
