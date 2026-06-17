//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFileExclusive(file *os.File) (func(), error) {
	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}, nil
}
