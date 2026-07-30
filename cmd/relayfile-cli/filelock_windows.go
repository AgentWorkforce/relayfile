//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func mountStartLockDirectoryTrusted(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0, nil
}

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

func tryLockFileExclusive(file *os.File) (func(), bool, error) {
	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err == windows.ERROR_LOCK_VIOLATION {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}, true, nil
}
