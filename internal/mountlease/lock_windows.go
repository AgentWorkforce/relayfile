//go:build windows

package mountlease

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryLockFile(file *os.File) (func(), bool, error) {
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
	return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped) }, true, nil
}
