//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mountsync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openLocalRegularNoFollow opens a regular file through directory file
// descriptors rooted at /, so replacing an ancestor after the caller's Lstat
// cannot redirect the read outside the path that was checked. The final
// descriptor is also validated with fstat; O_NOFOLLOW alone would still allow
// a FIFO or device and could block the mount or source unintended bytes.
func openLocalRegularNoFollow(root, path string) (*os.File, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(abs) || abs == string(filepath.Separator) {
		return nil, errors.New("invalid local file path")
	}
	anchor := string(filepath.Separator)
	rel := strings.TrimPrefix(abs, string(filepath.Separator))
	if strings.TrimSpace(root) != "" {
		rootAbs, rootErr := filepath.Abs(filepath.Clean(root))
		if rootErr != nil {
			return nil, rootErr
		}
		rootReal, rootErr := filepath.EvalSymlinks(rootAbs)
		if rootErr != nil {
			return nil, rootErr
		}
		rel, rootErr = filepath.Rel(rootAbs, abs)
		if rootErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, errors.New("local file escapes mount root")
		}
		anchor = rootReal
		rel = filepath.ToSlash(rel)
	} else {
		parentReal, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if parentErr != nil {
			return nil, parentErr
		}
		anchor = parentReal
		rel = filepath.Base(abs)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(rel), "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" || parts[len(parts)-1] == "." || parts[len(parts)-1] == ".." {
		return nil, errors.New("invalid local file path")
	}
	current, err := openDirectoryNoFollow(anchor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = current.Close() }()
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid local file path")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		_ = current.Close()
		current = next
	}
	fd, err := unix.Openat(int(current.Fd()), parts[len(parts)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), abs)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, os.ErrInvalid
	}
	return file, nil
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(abs) {
		return nil, errors.New("invalid local directory path")
	}
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(rootFD), string(filepath.Separator))
	for _, part := range strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		_ = current.Close()
		current = next
	}
	return current, nil
}
