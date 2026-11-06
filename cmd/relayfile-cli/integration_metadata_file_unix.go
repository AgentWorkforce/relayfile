//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func openIntegrationMetadataFile(path string) (*os.File, error) {
	// Open without waiting for a FIFO writer, then validate the opened object.
	// A path replacement cannot substitute an unchecked descriptor after Stat.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("metadata JSON must be a regular file")
	}
	if err == nil && info.Mode().Perm()&0077 != 0 {
		err = errors.New("metadata JSON file must be private; run chmod 600 on the file")
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
