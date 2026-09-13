//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mountsync

import (
	"os"
	"syscall"
)

// openLocalRegularNoFollow prevents a link swap between Lstat and the content
// read from turning a local symlink into a provider write of its target.
func openLocalRegularNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
