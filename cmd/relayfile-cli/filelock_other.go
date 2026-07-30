//go:build !unix && !windows

package main

import "os"

func mountStartLockDirectoryTrusted(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0, nil
}

func lockFileExclusive(file *os.File) (func(), error) {
	return func() {}, nil
}

func tryLockFileExclusive(file *os.File) (func(), bool, error) {
	return func() {}, true, nil
}
