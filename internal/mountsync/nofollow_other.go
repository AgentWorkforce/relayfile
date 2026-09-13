//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package mountsync

import "os"

// Platforms without O_NOFOLLOW still get the Lstat check in readLocalSnapshot;
// their filesystem-specific no-follow primitive can be added without changing
// the snapshot contract.
func openLocalRegularNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return os.Open(path)
}
