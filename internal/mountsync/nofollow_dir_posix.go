//go:build aix || darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package mountsync

import "golang.org/x/sys/unix"

// These POSIX platforms do not provide Linux's O_PATH. O_RDONLY directory
// descriptors preserve the no-follow guarantee; execute-only ancestors may
// be unreadable here and fail closed rather than falling back to path opens.
func directoryOpenFlags() int {
	return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
