//go:build unix

package mountlease

import (
	"os"

	"golang.org/x/sys/unix"
)

func tryLockFile(file *os.File) (func(), bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, true, nil
}
