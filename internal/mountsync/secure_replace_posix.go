//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package mountsync

// POSIX materialization uses directory file descriptors and *at operations.
// Once the parent directory is opened with O_NOFOLLOW, an attacker replacing
// an ancestor path cannot redirect the temp-file create or final rename.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var errAncestorSafetyUnsupported = errors.New("secure ancestor operations are unsupported on this platform")
var errStagingCleanupLimit = errors.New("staging cleanup entry limit exceeded")

func ensureSecureParentDirectory(root, target string) error {
	parent, _, err := secureParentDir(root, target)
	if parent != nil {
		_ = parent.Close()
	}
	return err
}

func secureParentDir(root, target string) (*os.File, string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, "", err
	}
	targetAbs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, "", fmt.Errorf("path %s escapes local root %s", target, root)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" || parts[len(parts)-1] == "." {
		return nil, "", fmt.Errorf("invalid target path %s", target)
	}
	current, err := openDirectoryNoFollow(rootAbs)
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			_ = current.Close()
			return nil, "", fmt.Errorf("invalid path component %q", part)
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), part, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = current.Close()
				return nil, "", mkdirErr
			}
			nextFD, openErr = unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		}
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

func secureTempName(base string) string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ".relayfile.tmp-fallback"
	}
	short := base
	if len(short) > 40 {
		short = short[:40]
	}
	return "." + short + ".tmp-" + hex.EncodeToString(random[:])
}

func openSecureTemp(parent *os.File, base string, mode uint32) (*os.File, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		name := secureTempName(base)
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), name, nil
	}
	return nil, "", errors.New("could not allocate secure temporary file")
}

func writeFileAtomicSecure(root, target string, data []byte, mode os.FileMode) error {
	parent, base, err := secureParentDir(root, target)
	if err != nil {
		return err
	}
	defer parent.Close()
	tmp, name, err := openSecureTemp(parent, base, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), base); err != nil {
		return err
	}
	committed = true
	return nil
}

func writeSymlinkAtomicSecure(root, targetPath, target string) error {
	parent, base, err := secureParentDir(root, targetPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := secureTempName(base)
	if err := unix.Symlinkat(target, int(parent.Fd()), name); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		}
	}()
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), base); err != nil {
		return err
	}
	committed = true
	return nil
}

// movePathAtomicSecure publishes an already-verified staged file or symlink.
// It applies the authoritative mode before the destination becomes visible.
// A bind/FUSE mount can put targetRoot on a different filesystem even when the
// staging directory is its sibling; EXDEV therefore falls back to a bounded
// stream into an atomic temporary file in the destination directory.
func movePathAtomicSecure(sourceRoot, sourcePath, targetRoot, targetPath string, targetMode os.FileMode) error {
	return movePathAtomicSecureWithRename(sourceRoot, sourcePath, targetRoot, targetPath, targetMode, unix.Renameat)
}

func movePathAtomicSecureWithRename(sourceRoot, sourcePath, targetRoot, targetPath string, targetMode os.FileMode, renameat func(int, string, int, string) error) error {
	sourceParent, sourceBase, err := secureParentDir(sourceRoot, sourcePath)
	if err != nil {
		return err
	}
	defer sourceParent.Close()
	targetParent, targetBase, err := secureParentDir(targetRoot, targetPath)
	if err != nil {
		return err
	}
	defer targetParent.Close()

	var stat unix.Stat_t
	if err := unix.Fstatat(int(sourceParent.Fd()), sourceBase, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	isRegular := stat.Mode&unix.S_IFMT == unix.S_IFREG
	isSymlink := stat.Mode&unix.S_IFMT == unix.S_IFLNK
	if !isRegular && !isSymlink {
		return fmt.Errorf("staged path %s has unsupported mode %#o", sourcePath, stat.Mode)
	}

	var sourceFile *os.File
	if isRegular {
		fd, openErr := unix.Openat(int(sourceParent.Fd()), sourceBase, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return openErr
		}
		sourceFile = os.NewFile(uintptr(fd), sourcePath)
		defer sourceFile.Close()
		if err := sourceFile.Chmod(targetMode.Perm()); err != nil {
			return err
		}
	}

	err = renameat(
		int(sourceParent.Fd()),
		sourceBase,
		int(targetParent.Fd()),
		targetBase,
	)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) {
		return err
	}

	if isRegular {
		if _, err := sourceFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		tmp, name, err := openSecureTemp(targetParent, targetBase, 0o600)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = unix.Unlinkat(int(targetParent.Fd()), name, 0)
			}
		}()
		if _, err := io.Copy(tmp, sourceFile); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Chmod(targetMode.Perm()); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := renameat(int(targetParent.Fd()), name, int(targetParent.Fd()), targetBase); err != nil {
			return err
		}
		committed = true
	} else {
		target, err := readlinkat(int(sourceParent.Fd()), sourceBase)
		if err != nil {
			return err
		}
		name := secureTempName(targetBase)
		if err := unix.Symlinkat(target, int(targetParent.Fd()), name); err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = unix.Unlinkat(int(targetParent.Fd()), name, 0)
			}
		}()
		if err := renameat(int(targetParent.Fd()), name, int(targetParent.Fd()), targetBase); err != nil {
			return err
		}
		committed = true
	}
	// Publication is already complete. Staging cleanup is best-effort here and
	// again at the caller's deferred tree removal; do not report a failed
	// publish after the authoritative destination has become visible.
	_ = unix.Unlinkat(int(sourceParent.Fd()), sourceBase, 0)
	return nil
}

func readlinkat(parentFD int, name string) (string, error) {
	buffer := make([]byte, 256)
	for len(buffer) <= 1<<20 {
		n, err := unix.Readlinkat(parentFD, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
	return "", errors.New("staged symlink target exceeds 1 MiB")
}

// removeOwnedStagingTreeBounded prevents a predictable staging path from
// turning startup into an unbounded recursive delete. Directories are read in
// capped batches without following symlinks; ownership and the entry cap are
// verified before removal begins.
func removeOwnedStagingTreeBounded(root string, maxEntries int) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clean non-directory staging path %s", root)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("refusing to clean staging directory not owned by uid %d: %s", os.Geteuid(), root)
	}
	if maxEntries < 1 {
		return errStagingCleanupLimit
	}
	entries := 1 // root
	directories := []string{root}
	for len(directories) > 0 {
		last := len(directories) - 1
		directoryPath := directories[last]
		directories = directories[:last]
		directory, err := openReadableDirectoryNoFollow(directoryPath)
		if err != nil {
			return err
		}
		for {
			remaining := maxEntries - entries + 1
			if remaining > 256 {
				remaining = 256
			}
			children, readErr := directory.ReadDir(remaining)
			entries += len(children)
			if entries > maxEntries {
				_ = directory.Close()
				return errStagingCleanupLimit
			}
			for _, child := range children {
				if child.IsDir() {
					directories = append(directories, filepath.Join(directoryPath, child.Name()))
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = directory.Close()
				return readErr
			}
		}
		if err := directory.Close(); err != nil {
			return err
		}
	}
	if entries > maxEntries {
		return errStagingCleanupLimit
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return nil
}

func openReadableDirectoryNoFollow(directoryPath string) (*os.File, error) {
	abs, err := filepath.Abs(filepath.Clean(directoryPath))
	if err != nil {
		return nil, err
	}
	parent, base, err := secureParentDir(filepath.Dir(abs), abs)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), abs), nil
}
