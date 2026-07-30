//go:build unix

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func mountStartLockDirectoryTrusted(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()), nil
}

func lockFileExclusive(file *os.File) (func(), error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}, nil
}

func tryLockFileExclusive(file *os.File) (func(), bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}, true, nil
}
