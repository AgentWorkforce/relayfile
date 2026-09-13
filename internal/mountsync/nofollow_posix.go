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
	current, base, err := openLocalParentNoFollow(root, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = current.Close() }()
	fd, err := unix.Openat(int(current.Fd()), base, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
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

func openLocalParentNoFollow(root, path string) (*os.File, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, "", err
	}
	if !filepath.IsAbs(abs) || abs == string(filepath.Separator) {
		return nil, "", errors.New("invalid local file path")
	}
	anchor := string(filepath.Separator)
	rel := strings.TrimPrefix(abs, string(filepath.Separator))
	if strings.TrimSpace(root) != "" {
		rootAbs, rootErr := filepath.Abs(filepath.Clean(root))
		if rootErr != nil {
			return nil, "", rootErr
		}
		rel, rootErr = filepath.Rel(rootAbs, abs)
		if rootErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, "", errors.New("local file escapes mount root")
		}
		// Keep the mount root lexical and open every component from / with
		// O_NOFOLLOW below. EvalSymlinks(rootAbs) would resolve a swapped
		// mount root before the anchored walk and could redirect the read.
		anchor = rootAbs
		rel = filepath.ToSlash(rel)
	} else {
		anchor = filepath.Dir(abs)
		rel = filepath.Base(abs)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(rel), "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" || parts[len(parts)-1] == "." || parts[len(parts)-1] == ".." {
		return nil, "", errors.New("invalid local file path")
	}
	current, err := openDirectoryNoFollow(anchor)
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			_ = current.Close()
			return nil, "", errors.New("invalid local file path")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		if openErr != nil {
			_ = current.Close()
			return nil, "", openErr
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		_ = current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}

// readLocalSymlinkNoFollow reads a symlink through an anchored parent
// directory fd. Readlinkat never follows the target, and the parent walk
// rejects swapped ancestors with O_NOFOLLOW.
func readLocalSymlinkNoFollow(root, path string, maxBytes int64) (string, error) {
	parent, base, err := openLocalParentNoFollow(root, path)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	capacity := 256
	if maxBytes > 0 && maxBytes < int64(capacity)-1 {
		capacity = int(maxBytes + 1)
	}
	for {
		buffer := make([]byte, capacity)
		n, readErr := unix.Readlinkat(int(parent.Fd()), base, buffer)
		if readErr != nil {
			return "", readErr
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
		if maxBytes > 0 && int64(len(buffer)) > maxBytes {
			return string(buffer[:n]), nil
		}
		if capacity >= 1<<20 {
			return "", errors.New("symlink target exceeds anchored read limit")
		}
		capacity *= 2
	}
}

// removeLocalNoFollow unlinks one mount entry through its anchored parent
// descriptor. Unlinkat never follows a final symlink, and opening the parent
// with no-follow component walks prevents an ancestor swap from redirecting
// the deletion outside localRoot.
func removeLocalNoFollow(root, path string) error {
	parent, base, err := openLocalParentNoFollow(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := unix.Unlinkat(int(parent.Fd()), base, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return os.ErrNotExist
		}
		return err
	}
	return nil
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
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(abs), "/"), "/")
	for index := 0; index < len(parts); index++ {
		part := parts[index]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = current.Close()
			return nil, errors.New("invalid local directory path")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		if openErr != nil {
			// macOS exposes /var and /tmp as absolute symlinks. Resolve an
			// intermediate link from the already-open parent descriptor, then
			// restart the remaining walk from / using the captured target. The
			// final component is always rejected when it is a symlink, so a
			// mount root cannot be redirected by this compatibility path.
			if (!errors.Is(openErr, unix.ELOOP) && !errors.Is(openErr, unix.ENOTDIR)) || index == len(parts)-1 {
				_ = current.Close()
				return nil, openErr
			}
			target, linkErr := readlinkAtBounded(int(current.Fd()), part)
			if linkErr != nil || index != 0 || current.Name() != string(filepath.Separator) || !allowIntermediateDirectorySymlink(part, target) {
				_ = current.Close()
				if linkErr != nil {
					return nil, linkErr
				}
				return nil, errors.New("relative intermediate symlink " + part + " -> " + target)
			}
			target = string(filepath.Separator) + target
			_ = current.Close()
			rootFD, rootErr := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if rootErr != nil {
				return nil, rootErr
			}
			current = os.NewFile(uintptr(rootFD), string(filepath.Separator))
			targetParts := strings.Split(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(target)), "/"), "/")
			parts = append(append([]string{}, targetParts...), parts[index+1:]...)
			index = -1
			continue
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		_ = current.Close()
		current = next
	}
	return current, nil
}

func readlinkAtBounded(parentFD int, name string) (string, error) {
	for capacity := 256; capacity <= 1<<20; capacity *= 2 {
		buffer := make([]byte, capacity)
		n, err := unix.Readlinkat(parentFD, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
	}
	return "", errors.New("symlink target exceeds anchored read limit")
}
