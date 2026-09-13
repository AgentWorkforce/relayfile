//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package mountsync

import (
	"errors"
	"os"
)

var errLocalSnapshotSafetyUnsupported = errors.New("anchored local file reads are unsupported on this platform")

// Platforms without a directory-FD no-follow primitive fail closed. A
// check-then-open fallback would permit an ancestor or final-component swap.
func openLocalRegularNoFollow(root, path string) (*os.File, error) {
	return nil, errLocalSnapshotSafetyUnsupported
}

func readLocalSymlinkNoFollow(root, path string, maxBytes int64) (string, error) {
	return "", errLocalSnapshotSafetyUnsupported
}

func removeLocalNoFollow(root, path string) error {
	return errLocalSnapshotSafetyUnsupported
}
