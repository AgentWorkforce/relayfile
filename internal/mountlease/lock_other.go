//go:build !unix && !windows

package mountlease

import (
	"errors"
	"os"
)

func tryLockFile(file *os.File) (func(), bool, error) {
	return nil, false, errors.New("workspace mount leases are unsupported on this platform")
}
