//go:build linux

package mountsync

import "golang.org/x/sys/unix"

// O_PATH permits directory search through execute-only ancestors on Linux.
// The descriptor remains anchored and every component is still protected by
// O_NOFOLLOW; it is used only as a directory fd for *at operations.
func directoryOpenFlags() int {
	return unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
}
