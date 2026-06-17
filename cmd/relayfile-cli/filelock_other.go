//go:build !unix && !windows

package main

import "os"

func lockFileExclusive(file *os.File) (func(), error) {
	return func() {}, nil
}
